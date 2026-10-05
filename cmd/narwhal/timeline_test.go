package main

import (
	"strings"
	"testing"

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
	m.snap.Messages = briefModel(t).snap.Messages
	got := ansi.Strip(m.View())
	g, tl := strings.Index(got, "1 Graph"), strings.Index(got, "5 Timeline")
	if g < 0 || tl < 0 || tl < g {
		t.Fatalf("no timeline under the graph:\n%s", got)
	}
	if !strings.Contains(got, "@schema add the LockKey field") {
		t.Errorf("timeline events missing:\n%s", got)
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
