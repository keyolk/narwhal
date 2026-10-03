package main

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"github.com/keyolk/narwhal/internal/broker"
)

func verdictModel(t *testing.T) tuiModel {
	t.Helper()
	m := inspectorModel(t)
	m.snap.Verdicts = []broker.Verdict{
		{TaskID: "api", Question: broker.QuestionNoResult, P: 0.82, Decided: true,
			Action: broker.ActionRetry, Latency: 640},
		{TaskID: "api", Question: broker.QuestionSameCause, P: 0.91, Decided: true,
			Action: broker.ActionEscalate},
		{TaskID: "auth", Question: broker.QuestionNoResult, P: 0.3, Decided: true,
			Action: broker.ActionComplete},
		{TaskID: "auth", Question: broker.QuestionNoResult, Action: broker.ActionFallback},
	}
	return m
}

// A verdict that moved a task must be visible on that task. A retry or a
// model change with no reason on screen reads as a worker that failed, or a
// planner that changed its mind, for nothing.
func TestInspectorShowsTheSelectedTasksVerdicts(t *testing.T) {
	m := verdictModel(t)
	m.taskCur = 0 // api

	got := m.viewInspector(90, 14)
	for _, want := range []string{"no-result", "0.82 → retry", "640ms", "same-cause", "0.91 → escalate"} {
		if !strings.Contains(got, want) {
			t.Errorf("inspector is missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "0.30") {
		t.Errorf("inspector shows another task's verdict:\n%s", got)
	}
}

func TestUndecidedVerdictSaysTheDispatcherKeptItsCall(t *testing.T) {
	v := broker.Verdict{Question: broker.QuestionNoResult, Action: broker.ActionFallback}
	if got := verdictRow(v, 80); !strings.Contains(got, "undecided → kept") {
		t.Errorf("row = %q", got)
	}
}

func TestVerdictRowsCountTowardTheInspectorsFixedRows(t *testing.T) {
	// The feed's scroll window is sized from inspectorFields. A verdict row
	// drawn but not counted would push the feed's last line off the pane.
	m := verdictModel(t)
	m.taskCur = 0
	without := inspectorModel(t)
	without.taskCur = 0
	if got, base := m.nodeFieldRows(), without.nodeFieldRows(); got != base+2 {
		t.Errorf("nodeFieldRows = %d, want %d (two verdicts on api)", got, base+2)
	}
}

func TestHeaderSummarizesVerdicts(t *testing.T) {
	m := verdictModel(t)
	got := m.viewHeader()
	for _, want := range []string{"jev 4", "3 sharp", "1 undecided"} {
		if !strings.Contains(got, want) {
			t.Errorf("header is missing %q: %q", want, got)
		}
	}
}

func TestHeaderIsQuietWithoutVerdicts(t *testing.T) {
	m := inspectorModel(t)
	if got := m.viewHeader(); strings.Contains(got, "jev") {
		t.Errorf("header mentions jev on a run nobody judged: %q", got)
	}
}

func TestVerdictBarFillsToP(t *testing.T) {
	cases := map[float64]string{
		0:    "░░░░░░░░░░",
		0.5:  "█████░░░░░",
		0.82: "████████░░",
		1:    "██████████",
		1.5:  "██████████",
	}
	for p, want := range cases {
		if got := verdictBar(p); got != want {
			t.Errorf("verdictBar(%v) = %q, want %q", p, got, want)
		}
	}
}

// The bar's colour is the point: 0.42 means nothing until you know the
// dispatcher kept its old call because of it.
func TestVerdictColourIsWhatTheDispatcherDid(t *testing.T) {
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI256)
	t.Cleanup(func() { lipgloss.SetColorProfile(prev) })

	sharp := verdictRow(broker.Verdict{Question: "q", P: 0.9, Decided: true, Action: broker.ActionRetry}, 80)
	kept := verdictRow(broker.Verdict{Question: "q", P: 0.4, Decided: true, Action: broker.ActionFallback}, 80)
	if !strings.Contains(sharp, styGreen.Render(verdictBar(0.9))) {
		t.Errorf("a sharp verdict is not green: %q", sharp)
	}
	if !strings.Contains(kept, styYellow.Render(verdictBar(0.4))) {
		t.Errorf("a kept verdict is not yellow: %q", kept)
	}
}

// A worker's own question shows the question — that is what the operator
// needs to read — and counts apart from the dispatcher's verdicts.
func TestWorkerQuestionShowsWhatWasAsked(t *testing.T) {
	m := verdictModel(t)
	m.snap.Verdicts = append(m.snap.Verdicts, broker.Verdict{
		TaskID: "api", Asker: "worker-api", Question: broker.QuestionWorker,
		Ask: "Is the TestReplay failure flaky?", P: 0.83, Decided: true, Action: broker.ActionAnswered,
	})
	m.taskCur = 0
	got := ansi.Strip(m.viewInspector(100, 16))
	if !strings.Contains(got, "asked") || !strings.Contains(got, "0.83  Is the TestReplay failure flaky?") {
		t.Errorf("worker question not shown:\n%s", got)
	}
	if h := ansi.Strip(m.viewHeader()); !strings.Contains(h, "1 asked") {
		t.Errorf("header does not count the question apart: %q", h)
	}
}

func TestUnansweredWorkerQuestionSaysSo(t *testing.T) {
	v := broker.Verdict{Asker: "worker-x", Ask: "is it mine?", Question: broker.QuestionWorker}
	if got := ansi.Strip(verdictRow(v, 80)); !strings.Contains(got, "no answer") || !strings.Contains(got, "is it mine?") {
		t.Errorf("row = %q", got)
	}
}
