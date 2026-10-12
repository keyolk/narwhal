package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func openTaskOn(t *testing.T, id string) tuiModel {
	t.Helper()
	m := press(fixtureRun(t), "1")
	m.taskCur = indexOf(t, m, id)
	return press(m, "enter")
}

// A finished task opens on Summary: the request, the check, what reached
// it, the answer — together, without the tool calls between them.
func TestAFinishedTaskOpensOnItsSummary(t *testing.T) {
	m := openTaskOn(t, "client")
	got := viewText(m)
	for _, w := range []string{"▸ Summary", "Asked", "Store the server's lock response", "Result", "response mapping in one place"} {
		if !strings.Contains(got, w) {
			t.Errorf("summary lacks %q:\n%s", w, got)
		}
	}
	if strings.Contains(got, "→ Edit") {
		t.Errorf("tool calls on the summary:\n%s", got)
	}
}

// A running task opens on Activity, following.
func TestARunningTaskOpensOnItsActivity(t *testing.T) {
	m := openTaskOn(t, "api")
	if got := viewText(m); !strings.Contains(got, "▸ Activity") || !strings.Contains(got, "[following]") {
		t.Fatalf("running task:\n%s", got)
	}
}

// On Activity, a tool call opens to its full input and a result to all
// of itself; j/k move between entries and the bar marks the one selected.
func TestActivityEntriesOpenInPlace(t *testing.T) {
	m := openTaskOn(t, "api")
	got := viewText(m)
	if strings.Contains(got, "file_path: /repo/internal/api/router.go") {
		t.Fatal("a tool call's input is shown before it is opened")
	}
	// Up to the Read call, then open it.
	l := m.layoutTask(m.taskByID("api"), m.width-2)
	read := -1
	for i, e := range l.entries {
		if e.kind == "tool" && strings.HasPrefix(e.text, "Read") {
			read = i
		}
	}
	if read < 0 {
		t.Fatal("setup: no Read call")
	}
	m = press(m, "g")
	for i := 0; i < read; i++ {
		m = press(m, "j")
	}
	if m.taskSel != read {
		t.Fatalf("cursor on %d, want %d", m.taskSel, read)
	}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(tuiModel)
	got = viewText(m)
	if !strings.Contains(got, "│ file_path: /repo/internal/api/router.go") {
		t.Errorf("the opened call does not show its input:\n%s", got)
	}
	if !strings.Contains(got, "▌") {
		t.Error("no cursor bar")
	}
	// E opens everything; the 180-line result marker is gone.
	m = press(m, "E")
	if got := viewText(m); !strings.Contains(got, "// 180 lines") {
		t.Errorf("E did not open the results:\n%s", got)
	}
}

// Messages lists the radio traffic from and to the task, in full.
func TestTheMessagesTab(t *testing.T) {
	m := openTaskOn(t, "api")
	m = press(m, "tab")
	got := viewText(m)
	for _, w := range []string{"▸ Messages", "sent", "the same nonce passes twice", "from coordinator", "relayed your instruction to api"} {
		if !strings.Contains(got, w) {
			t.Errorf("messages lack %q:\n%s", w, got)
		}
	}
	// Tab wraps; shift-tab goes back.
	if m = press(m, "tab"); m.currentTaskTab(m.taskByID("api")) != taskTabSummary {
		t.Error("tab from the last tab did not wrap to Summary")
	}
}

// A long request on Summary folds, and enter opens it.
func TestALongRequestFoldsOnTheSummary(t *testing.T) {
	m := fixtureRun(t)
	m.snap.Tasks[2].Assignment = strings.Repeat("line of a long request\n", 30)
	m = press(m, "1")
	m.taskCur = indexOf(t, m, "client")
	m = press(m, "enter")
	if got := ansi.Strip(m.View()); !strings.Contains(got, "more lines — enter to open") {
		t.Fatalf("not folded:\n%s", got)
	}
	m = press(m, "enter")
	if got := ansi.Strip(m.View()); strings.Contains(got, "more lines — enter to open") {
		t.Errorf("enter did not open it:\n%s", got)
	}
}
