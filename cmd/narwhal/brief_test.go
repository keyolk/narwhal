package main

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/keyolk/narwhal/internal/broker"
)

func briefModel(t *testing.T) tuiModel {
	t.Helper()
	m := testModel(0, 0)
	m.width, m.height = 120, 34
	at := time.Date(2026, 10, 4, 17, 30, 0, 0, time.Local)
	m.snap.Tasks = []broker.TaskSnapshot{
		{ID: "api", State: broker.TaskDispatched},
		{ID: "schema", State: broker.TaskDispatched},
		{ID: "client", State: broker.TaskCompleted, Outcome: "state.ts cleaned up"},
		{ID: "fuzz", State: broker.TaskFailed, Outcome: "replay test never passed"},
	}
	m.snap.Messages = []*broker.Message{
		{Seq: 1, Sender: "worker-client", Priority: broker.PriorityUrgent, Content: "old urgent from a finished task", CreatedAt: at},
		{Seq: 2, Sender: "worker-api", Priority: broker.PriorityUrgent, Content: "nonce passes twice on replay", CreatedAt: at},
		{Seq: 3, Sender: "main", Content: "@schema add the LockKey field", CreatedAt: at.Add(time.Minute)},
		{Seq: 4, Sender: "main", Content: "everyone: gofmt before committing", CreatedAt: at.Add(2 * time.Minute)},
		{Seq: 5, Sender: "worker-schema", Content: "LockKey added in types.go", CreatedAt: at.Add(3 * time.Minute)},
		{Seq: 6, Sender: "worker-api", Content: "FILE_CLAIM|api|handlers.go", CreatedAt: at.Add(4 * time.Minute)},
	}
	return m
}

func briefText(m tuiModel) string { return ansi.Strip(m.viewBrief(110, 30)) }

func TestFourShowsTheBriefAndThreeTheRadio(t *testing.T) {
	m := briefModel(t)
	m = press(m, "4")
	if !m.briefMode || m.focus != focusRadio {
		t.Fatalf("4: briefMode=%v focus=%v", m.briefMode, m.focus)
	}
	if !strings.Contains(ansi.Strip(m.viewChannel(100, 20)), "Brief") {
		t.Error("the lower pane is not the brief")
	}
	m = press(m, "3")
	if m.briefMode {
		t.Error("3 did not go back to the radio")
	}
}

// What needs a decision: a failure and an urgent post from a task still
// running. An urgent post from a task that has since finished does not.
func TestDecisionsAreOnlyWhatIsStillOpen(t *testing.T) {
	full := briefText(briefModel(t))
	// Only the decision section: the finished worker's line further down
	// legitimately quotes its last post.
	got := full[:strings.Index(full, "Workers")]
	for _, want := range []string{"failed: replay test never passed", "nonce passes twice on replay"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing decision %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "old urgent from a finished task") {
		t.Errorf("a finished task's urgent post is still listed:\n%s", got)
	}
}

// A worker's line is its latest real post, not a file claim.
func TestWorkerLineSkipsCoordination(t *testing.T) {
	m := briefModel(t)
	if got := m.briefWorkerLine(m.taskByID("api")); got != "nonce passes twice on replay" {
		t.Errorf("api line = %q, want its last real post", got)
	}
	if got := m.briefWorkerLine(m.taskByID("client")); got != "old urgent from a finished task" {
		t.Errorf("client line = %q", got)
	}
}

// An addressed instruction is answered when that worker posts after it; an
// instruction to everyone, when anyone does.
func TestInstructionsShowWhoAnswered(t *testing.T) {
	ins := briefInstructions(briefModel(t).snap)
	if len(ins) != 2 {
		t.Fatalf("instructions = %+v", ins)
	}
	if strings.Join(ins[0].answeredBy, ",") != "schema" {
		t.Errorf("@schema instruction answered by %v, want [schema]", ins[0].answeredBy)
	}
	if strings.Join(ins[1].answeredBy, ",") != "api,schema" {
		t.Errorf("broadcast answered by %v, want [api schema]", ins[1].answeredBy)
	}
}

func TestUnansweredInstructionSaysWaiting(t *testing.T) {
	m := briefModel(t)
	m.snap.Messages = append(m.snap.Messages, &broker.Message{Seq: 9, Sender: "main", Content: "@api stop and rebase"})
	if got := briefText(m); !strings.Contains(got, "waiting") || !strings.Contains(got, "stop and rebase") {
		t.Errorf("unanswered instruction not marked waiting:\n%s", got)
	}
}

func TestEmptyBriefPointsAtTheCommandLine(t *testing.T) {
	m := testModel(0, 0)
	got := ansi.Strip(m.viewBrief(80, 20))
	if !strings.Contains(got, "nothing") || !strings.Contains(got, "press i") {
		t.Errorf("empty brief:\n%s", got)
	}
}

// Styled lines are fitted without counting escape codes as cells or
// splitting them.
func TestBriefLinesFitTheirWidth(t *testing.T) {
	m := briefModel(t)
	m.snap.Messages = append(m.snap.Messages, &broker.Message{Seq: 10, Sender: "worker-api",
		Priority: broker.PriorityUrgent, Content: strings.Repeat("긴 메시지 ", 40)})
	for i, l := range strings.Split(m.viewBrief(60, 30), "\n") {
		if w := lipgloss.Width(l); w > 60 {
			t.Errorf("line %d is %d wide: %q", i, w, ansi.Strip(l))
		}
	}
}

func TestVerdictThatRetriedATaskNeedsAttention(t *testing.T) {
	m := briefModel(t)
	m.snap.Verdicts = []broker.Verdict{{TaskID: "api", Question: broker.QuestionNoResult,
		P: 0.71, Decided: true, Action: broker.ActionRetry}}
	if got := briefText(m); !strings.Contains(got, "jev no-result 0.71 → retry") {
		t.Errorf("verdict not listed:\n%s", got)
	}
}
