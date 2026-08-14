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

func TestPickerDistinguishesLiveFromFinished(t *testing.T) {
	// The list is now mostly history. A wall of identically dim rows makes
	// the one live run — the only one you can still act on — disappear
	// into it, so live and finished must not render the same.
	forceColor(t)

	runs := []store.LiveRun{
		{RunID: "live", BrokerURL: "http://x", CWD: "/src/repo",
			Prompt: "still working", StartedAt: 1786600000},
		{RunID: "done", CWD: "/src/repo",
			Prompt: "already finished", StartedAt: 1786500000},
	}
	m := newTUIModel(runs, 0, 0, true)
	m.width, m.height = 100, 20
	// Move the cursor off both rows so the selection style is not what is
	// being compared.
	m.runCur = -1
	out := m.View()

	liveRow := lineContaining(out, "still working")
	doneRow := lineContaining(out, "already finished")
	if liveRow == "" || doneRow == "" {
		t.Fatalf("rows missing:\n%s", out)
	}
	if liveRow == doneRow {
		t.Fatal("live and finished prompts rendered identically")
	}
	// The live prompt is what you are choosing between; the finished one
	// is a label on something already done.
	if !strings.Contains(doneRow, "\x1b[") {
		t.Errorf("the finished prompt is not dimmed: %q", doneRow)
	}
	if strings.Contains(liveRow, "\x1b[2m") {
		t.Errorf("the live prompt was dimmed like history: %q", liveRow)
	}
}

func TestPickerMarksRunStateWithAGlyph(t *testing.T) {
	// Colour alone is not enough — it does not survive a screenshot, a
	// colourblind reader, or a terminal with a washed-out palette.
	runs := []store.LiveRun{
		{RunID: "live", BrokerURL: "http://x", CWD: "/src", Prompt: "working", StartedAt: 1786600000},
		{RunID: "done", CWD: "/src", Prompt: "finished", StartedAt: 1786500000},
	}
	m := newTUIModel(runs, 0, 0, true)
	m.width, m.height = 100, 20
	m.runCur = -1
	out := m.View()

	if !strings.Contains(lineContaining(out, "08-14"), icons.runActive) &&
		!strings.Contains(out, icons.runActive) {
		t.Errorf("no live glyph in the picker:\n%s", out)
	}
	if !strings.Contains(out, icons.runDone) {
		t.Errorf("no finished glyph in the picker:\n%s", out)
	}
}

func TestMainViewHintsSitOnTheLastRow(t *testing.T) {
	// The main view was the one place pinFooter was not applied. Its panes
	// render only the rows they have content for, so on a tall terminal
	// the body stopped short and the hints rode up with it — halfway up a
	// 40-row screen with a four-task graph.
	t.Setenv("HOME", t.TempDir())
	m := testModel(0, 0)
	m.width, m.height = 100, 40
	m.snap.RunID = "r4"
	m.snap.State = broker.RunActive
	m.snap.Tasks = []broker.TaskSnapshot{
		{ID: "t1", State: broker.TaskDispatched},
		{ID: "t2", State: broker.TaskReady, Deps: []string{"t1"}},
	}

	last, n := lastLine(m.View())
	if n != 40 {
		t.Errorf("rendered %d lines for a 40-row terminal", n)
	}
	if !strings.Contains(last, "q quit") {
		t.Errorf("last line is not the hints: %q", last)
	}
}
