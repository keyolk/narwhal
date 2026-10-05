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
	for _, want := range []string{"Timeline", "instruction", "@schema add the LockKey field", "urgent", "nonce passes twice"} {
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
