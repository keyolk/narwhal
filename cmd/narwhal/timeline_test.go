package main

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

func TestFiveShowsTheTimelineAndThreeGoesBack(t *testing.T) {
	m := press(briefModel(t), "5")
	if m.lower != lowerTimeline || m.focus != focusRadio {
		t.Fatalf("5: lower=%v focus=%v", m.lower, m.focus)
	}
	got := ansi.Strip(m.viewChannel(110, 20))
	for _, want := range []string{"Timeline", "» operator @schema add the LockKey field", "! api", "nonce passes twice"} {
		if !strings.Contains(got, want) {
			t.Errorf("timeline pane lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "LockKey added in types.go") {
		t.Errorf("an ordinary worker post is on the timeline:\n%s", got)
	}
	if m = press(m, "3"); m.lower != lowerRadio {
		t.Errorf("3 did not return to the radio: %v", m.lower)
	}
}

// The pane follows the run: when events outnumber rows, the newest stay.
func TestTimelineKeepsTheNewestThatFit(t *testing.T) {
	m := press(briefModel(t), "5")
	got := ansi.Strip(m.viewTimeline(110, 3))
	if !strings.Contains(got, "gofmt before committing") || strings.Contains(got, "nonce passes twice") {
		t.Errorf("not the newest two:\n%s", got)
	}
}

// The history is on screen without a key: under the graph, in the room the
// graph does not use.
func TestTheTimelineSitsUnderTheGraph(t *testing.T) {
	m := liveModel(t)
	m.width, m.height = 160, 40
	at := time.Date(2026, 10, 8, 10, 0, 0, 0, time.Local)
	m.snap.Tasks[2].StartedAt, m.snap.Tasks[2].EndedAt = at, at.Add(time.Minute)
	m.snap.Tasks[2].Outcome = "gamma found the leak"
	m.snap.Messages = briefModel(t).snap.Messages
	got := ansi.Strip(m.View())
	g, tl := strings.Index(got, "1 Graph"), strings.Index(got, "5 Timeline")
	if g < 0 || tl < 0 || tl < g {
		t.Fatalf("no timeline under the graph:\n%s", got)
	}
	if !strings.Contains(got, "gamma found the leak") {
		t.Errorf("task lifecycle missing from the timeline:\n%s", got)
	}
}

// With the radio beside it, the timeline under the graph does not repeat
// it: an instruction or urgent post appears once on screen, in the radio.
func TestTheTimelineBesideTheRadioDoesNotRepeatIt(t *testing.T) {
	m := liveModel(t)
	m.width, m.height = 160, 40
	m.snap.Messages = briefModel(t).snap.Messages
	got := ansi.Strip(m.View())
	for _, msg := range []string{"@schema add the LockKey field", "nonce passes twice on replay"} {
		if n := strings.Count(got, msg); n != 1 {
			t.Errorf("%q is on screen %d times, want once:\n%s", msg, n, got)
		}
	}
	// With the brief in the radio's place nothing else shows them, so the
	// timeline keeps them.
	m = press(m, "4")
	if got := ansi.Strip(m.View()); !strings.Contains(got, "» operator @schema add the LockKey field") {
		t.Errorf("the timeline beside the brief dropped the operator's instruction:\n%s", got)
	}
}

// A short terminal keeps the graph's whole column.
func TestAShortTerminalGivesTheGraphItsColumn(t *testing.T) {
	m := liveModel(t)
	m.width, m.height = 160, 14
	if got := ansi.Strip(m.View()); strings.Contains(got, "5 Timeline") {
		t.Errorf("timeline squeezed in on a short terminal:\n%s", got)
	}
}

// The footer always says the run can be steered, and what became of the
// last instruction.
func TestTheFooterCarriesTheSteerControl(t *testing.T) {
	m := briefModel(t)
	got := ansi.Strip(m.viewFooter())
	for _, want := range []string{"i » steer", "last: everyone: gofmt before committing", "✓"} {
		if !strings.Contains(got, want) {
			t.Errorf("footer lacks %q:\n%s", want, got)
		}
	}
	if lines := strings.Split(got, "\n"); len(lines) != 2 {
		t.Errorf("footer is %d lines, want 2:\n%s", len(lines), got)
	}
	if fresh := ansi.Strip(liveModel(t).viewFooter()); !strings.Contains(fresh, "i » steer") || strings.Contains(fresh, "last:") {
		t.Errorf("footer before any instruction:\n%s", fresh)
	}
}
