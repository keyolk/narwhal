package main

import (
	"strings"
	"testing"
	"time"

	"github.com/keyolk/narwhal/internal/broker"
)

// inspectorModel is a three-task run: two investigators and a synthesis
// that waits on both, with the graph focused.
func inspectorModel(t *testing.T) tuiModel {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	m := testModel(0, 0)
	m.width, m.height = 120, 30
	m.focus = focusTasks
	m.snap.RunID = "run-inspect"
	m.snap.Tasks = []broker.TaskSnapshot{
		{ID: "api", Name: "api-audit", State: broker.TaskDispatched, Dispatches: 2, Model: "haiku"},
		{ID: "auth", State: broker.TaskCompleted, Dispatches: 1, Model: "haiku"},
		{ID: "synthesis", State: broker.TaskDispatched, Dispatches: 1, Model: "opus",
			Deps: []string{"api", "auth"}},
	}
	return m
}

func TestInspectorFollowsTheGraphCursor(t *testing.T) {
	// The whole point: moving the cursor in the graph used to change
	// nothing else on screen, so reading a node meant opening a detail
	// view and backing out of it.
	m := inspectorModel(t)

	m.taskCur = 0
	first := m.viewInspector(70, 9)
	if !strings.Contains(first, "api") {
		t.Fatalf("inspector does not show the selected node:\n%s", first)
	}

	m.taskCur = 2
	second := m.viewInspector(70, 9)
	if first == second {
		t.Fatal("the inspector did not change when the cursor moved")
	}
	if !strings.Contains(second, "synthesis") {
		t.Errorf("inspector does not show the newly selected node:\n%s", second)
	}
}

func TestInspectorNamesTheUnfinishedDependency(t *testing.T) {
	// "waits on api" is the answer when a task is sitting idle. Listing
	// every edge instead would repeat what the graph already draws.
	m := inspectorModel(t)
	m.taskCur = 2 // synthesis, waiting on api (running) and auth (done)

	out := m.viewInspector(70, 9)
	if !strings.Contains(out, "waits") {
		t.Fatalf("no waits row:\n%s", out)
	}
	line := lineContaining(out, "waits")
	if !strings.Contains(line, "api") {
		t.Errorf("the unfinished dep is not named: %q", line)
	}
	if strings.Contains(line, "auth") {
		t.Errorf("a finished dep is still listed as waited on: %q", line)
	}
	if !strings.Contains(line, "1/2") {
		t.Errorf("no progress against the full dep set: %q", line)
	}
}

func TestInspectorShowsWhatATaskBlocks(t *testing.T) {
	// The snapshot only carries outgoing edges, so the reverse direction
	// has to be derived — and it is the more useful one when deciding
	// whether a stuck task matters.
	m := inspectorModel(t)
	m.taskCur = 0 // api

	line := lineContaining(m.viewInspector(70, 9), "blocks")
	if !strings.Contains(line, "synthesis") {
		t.Errorf("api does not report blocking synthesis: %q", line)
	}
}

func TestInspectorShowsRetriesOnlyWhenRetried(t *testing.T) {
	// One dispatch is the normal case; showing it on every node would
	// spend a line of a nine-line pane saying nothing.
	m := inspectorModel(t)

	m.taskCur = 1 // auth, one dispatch
	if strings.Contains(m.viewInspector(70, 9), "tries") {
		t.Error("a task with a single dispatch reported retries")
	}

	m.taskCur = 0 // api, two dispatches
	line := lineContaining(m.viewInspector(70, 9), "tries")
	if !strings.Contains(line, "2") {
		t.Errorf("a retried task does not report its attempts: %q", line)
	}
}

func TestInspectorReportsHeldFiles(t *testing.T) {
	// File claims live in the coordinator, which the monitor cannot reach,
	// so the current set is replayed from the radio.
	m := inspectorModel(t)
	m.taskCur = 0
	m.snap.Messages = []*broker.Message{
		{Seq: 1, Sender: "worker-api", Content: broker.FormatFileClaimRequest(
			broker.FileClaimPrefix, "api", []string{"internal/api/router.go"})},
	}

	line := lineContaining(m.viewInspector(70, 9), "holds")
	if !strings.Contains(line, "router.go") {
		t.Errorf("held file not reported: %q", line)
	}

	// A release must take it back off, or the pane reports a claim the
	// worker gave up hours ago.
	m.snap.Messages = append(m.snap.Messages, &broker.Message{
		Seq: 2, Sender: "worker-api", Content: broker.FormatFileClaimRequest(
			broker.FileReleasePrefix, "api", []string{"internal/api/router.go"})})
	if strings.Contains(m.viewInspector(70, 9), "holds") {
		t.Error("a released file is still reported as held")
	}
}

func TestInspectorKeepsAFixedHeight(t *testing.T) {
	// Field rows only appear when they have something to say. Without
	// padding, the pane's height tracks the selected node and the radio
	// title below it slides up and down as the cursor moves.
	m := inspectorModel(t)

	heights := map[int]int{}
	for cur := range m.snap.Tasks {
		m.taskCur = cur
		heights[cur] = len(strings.Split(m.viewInspector(70, 9), "\n"))
	}
	for cur, h := range heights {
		if h != 9 {
			t.Errorf("node %d rendered %d lines, want a fixed 9", cur, h)
		}
	}
}

func TestInspectorIsDroppedOnAShortTerminal(t *testing.T) {
	// Squeezing the radio to nothing to keep a summary pane is the wrong
	// trade: the radio is the run's actual content.
	m := inspectorModel(t)
	if got := m.inspectorHeight(10); got != 0 {
		t.Errorf("inspectorHeight(10) = %d, want 0 on a short terminal", got)
	}
	if got := m.inspectorHeight(30); got == 0 {
		t.Error("inspectorHeight(30) = 0, want the inspector on a normal terminal")
	}
}

func TestInspectorIsAbsentWithNoTasks(t *testing.T) {
	m := inspectorModel(t)
	m.snap.Tasks = nil
	if got := m.inspectorHeight(30); got != 0 {
		t.Errorf("inspectorHeight = %d with no tasks, want 0", got)
	}
}

// lineContaining returns the first line of s holding sub, or "".
func lineContaining(s, sub string) string {
	for _, l := range strings.Split(s, "\n") {
		if strings.Contains(l, sub) {
			return l
		}
	}
	return ""
}

func TestRadioRowsCarryATimestamp(t *testing.T) {
	// A sequence number says the order; only the clock says whether two
	// findings landed together or an hour apart.
	m := inspectorModel(t)
	at := time.Date(2026, 8, 14, 3, 12, 45, 0, time.Local)
	msg := &broker.Message{Seq: 1, Sender: "worker-api", Priority: broker.PriorityNormal,
		Content: "found it", CreatedAt: at}

	row := m.radioRow(msg, 80, false)
	if !strings.Contains(row, "03:12:45") {
		t.Errorf("no timestamp in the radio row: %q", row)
	}
}

func TestRadioRendersProtocolMessagesAsSentences(t *testing.T) {
	m := inspectorModel(t)
	cases := []struct {
		name, content, want string
	}{
		{"file claim",
			broker.FormatFileClaimRequest(broker.FileClaimPrefix, "api", []string{"internal/api/router.go"}),
			"claims"},
		{"file release",
			broker.FormatFileClaimRequest(broker.FileReleasePrefix, "api", []string{"internal/api/router.go"}),
			"releases"},
		{"dep add",
			broker.FormatDepEdgeRequest(broker.DepAddPrefix, "api", []string{"auth"}),
			"waits on"},
		{"split",
			broker.FormatSplitRequest("extra", "extra-audit", "look here", nil),
			"new task"},
		{"escalate",
			broker.FormatModelEscalateRequest("api", "opus", "needs deeper reading"),
			"asks for opus"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			msg := &broker.Message{Seq: 1, Sender: "worker-api",
				Priority: broker.PriorityNormal, Content: c.content}
			row := m.radioRow(msg, 100, false)
			if strings.Contains(row, "|") {
				t.Errorf("the wire format leaked into the row: %q", row)
			}
			if !strings.Contains(row, c.want) {
				t.Errorf("row = %q, want it to read as %q", row, c.want)
			}
		})
	}
}

func TestRadioLeavesProseAlone(t *testing.T) {
	// Only protocol messages are rewritten; a worker's finding is shown as
	// written.
	m := inspectorModel(t)
	msg := &broker.Message{Seq: 1, Sender: "worker-api", Priority: broker.PriorityNormal,
		Content: "the provider lock releases before the retry loop finishes"}

	row := m.radioRow(msg, 100, false)
	if !strings.Contains(row, "provider lock releases before") {
		t.Errorf("prose was altered: %q", row)
	}
}

func TestRadioDoesNotRepeatTheSender(t *testing.T) {
	// Protocol summaries lead with the task they concern, which is usually
	// the sender: "api ⋮ api claims router.go" says it twice.
	m := inspectorModel(t)
	msg := &broker.Message{Seq: 1, Sender: "worker-api", Priority: broker.PriorityNormal,
		Content: broker.FormatFileClaimRequest(broker.FileClaimPrefix, "api",
			[]string{"internal/api/router.go"})}

	row := m.radioRow(msg, 100, false)
	if strings.Count(row, "api ") > 1 {
		t.Errorf("the sender is repeated in the summary: %q", row)
	}
}
