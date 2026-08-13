package main

import (
	"strings"
	"testing"

	"github.com/keyolk/narwhal/internal/broker"
	"github.com/keyolk/narwhal/internal/store"
)

// Every view built its output as "content, newline, hints" and stopped
// there, so the hints sat wherever the content happened to end — four rows
// down in a run picker with one run, with the rest of the screen empty
// below them. A hint line is furniture: it belongs at the bottom edge, in
// the same place every time, or the eye has to hunt for it.

// lastLine returns the final rendered line, and how many lines there were.
func lastLine(out string) (string, int) {
	lines := strings.Split(out, "\n")
	return lines[len(lines)-1], len(lines)
}

func TestPickerHintsSitOnTheLastRow(t *testing.T) {
	runs := []store.LiveRun{{RunID: "r1", BrokerURL: "http://x", CWD: "/tmp/x", Prompt: "audit"}}
	m := newTUIModel(runs, 0, 0, true)
	m.width, m.height = 100, 24

	last, n := lastLine(m.View())
	if n != 24 {
		t.Errorf("rendered %d lines for a 24-row terminal", n)
	}
	if !strings.Contains(last, "esc quit") {
		t.Errorf("last line is not the hints: %q", last)
	}
}

func TestDetailHintsSitOnTheLastRow(t *testing.T) {
	// Short content is the case that exposed this: a one-line assignment
	// put the hints five rows down a 24-row terminal.
	t.Setenv("HOME", t.TempDir())
	m := testModel(0, 0)
	m.width, m.height = 80, 24
	m.snap.RunID = "r1"
	m.snap.Tasks = []broker.TaskSnapshot{
		{ID: "a", State: broker.TaskCompleted, Assignment: "short"},
	}

	for _, tc := range []struct {
		name string
		mode detailMode
		hint string
	}{
		{"task", detailTask, "esc back"},
		{"session", detailSession, "esc back"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m.detail = tc.mode
			last, n := lastLine(m.View())
			if n != 24 {
				t.Errorf("rendered %d lines for a 24-row terminal", n)
			}
			if !strings.Contains(last, tc.hint) {
				t.Errorf("last line is not the hints: %q", last)
			}
		})
	}
}

func TestMessageDetailHintsSitOnTheLastRow(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := testModel(0, 1)
	m.width, m.height = 80, 24
	m.detail = detailMessage

	last, n := lastLine(m.View())
	if n != 24 {
		t.Errorf("rendered %d lines for a 24-row terminal", n)
	}
	if !strings.Contains(last, "esc back") {
		t.Errorf("last line is not the hints: %q", last)
	}
}

func TestContentTallerThanTheTerminalIsNotPadded(t *testing.T) {
	// Padding is for filling a short screen, not for pushing a full one
	// past its own bottom edge.
	long := strings.Repeat("line\n", 50)
	out := pinFooter(strings.TrimRight(long, "\n"), "hints", 10)
	if _, n := lastLine(out); n != 51 {
		t.Fatalf("rendered %d lines, want the content plus one hint line", n)
	}
	if last, _ := lastLine(out); last != "hints" {
		t.Errorf("last line = %q, want the hints", last)
	}
}
