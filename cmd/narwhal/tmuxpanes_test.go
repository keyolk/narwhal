package main

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/keyolk/tuikit/tmux"

	"github.com/keyolk/narwhal/internal/broker"
	"github.com/keyolk/narwhal/internal/store"
)

// One pane per working task, in a window named after the run, each
// running the monitor pinned to its task.
func TestTheTmuxLayoutHasAPanePerWorkingTask(t *testing.T) {
	tasks := []broker.TaskSnapshot{
		{ID: "api", State: broker.TaskDispatched},
		{ID: "done", State: broker.TaskCompleted},
		{ID: "it's", State: broker.TaskDispatched},
	}
	l := planPanes("/usr/local/bin/narwhal", "r1", "lock", "/src/my repo", tasks)
	want := []string{
		"/usr/local/bin/narwhal monitor --run r1 --task api",
		"/usr/local/bin/narwhal monitor --run r1 --task 'it'\\''s'",
	}
	if l.name != "nw:lock" || l.cwd != "/src/my repo" || strings.Join(l.panes, "\n") != strings.Join(want, "\n") {
		t.Errorf("layout %+v\nwant panes:\n%s", l, strings.Join(want, "\n"))
	}
	if l := planPanes("n", "r1", "x", "/", []broker.TaskSnapshot{{ID: "a", State: broker.TaskCompleted}}); len(l.panes) != 0 {
		t.Error("panes for a run with nothing working")
	}
	many := make([]broker.TaskSnapshot, 10)
	for i := range many {
		many[i] = broker.TaskSnapshot{ID: string(rune('a' + i)), State: broker.TaskDispatched}
	}
	if l := planPanes("n", "r", "x", "/", many); len(l.panes) != tmuxPlanCap {
		t.Errorf("%d panes for 10 tasks, want the cap of %d", len(l.panes), tmuxPlanCap)
	}
}

// toastText is every toast's text, newest last.
func toastText(m tuiModel) string {
	var b []string
	for _, t := range m.toasts.Items() {
		b = append(b, t.Text)
	}
	return strings.Join(b, "\n")
}

// paneRow is one list-panes row as tmux.PaneFormat asks tmux to print it.
func paneRow(id, window, session, name string) string {
	fixed := []string{id, window, session, "1", "0", "100", "1", "1", "80", "24"}
	var b strings.Builder
	b.WriteString(strings.Join(fixed, "\t"))
	for _, f := range []string{"main", name, "/", "zsh", ""} {
		fmt.Fprintf(&b, "\t%d:%s", len(f), f)
	}
	return b.String() + "\n"
}

// w opens the window off the UI loop, addresses every later call by the
// new pane's ID, retiles after each split, and says why not outside tmux.
func TestWOpensThePanes(t *testing.T) {
	m := fixtureRun(t)
	m.live = store.LiveRun{RunID: m.snap.RunID, Prompt: "replay-safe lock API"}
	m.snap.Tasks = append(m.snap.Tasks, broker.TaskSnapshot{ID: "second", State: broker.TaskDispatched})

	m.tm = tmux.Client{Runner: tmux.NewFake().On("display-message", "", tmux.ErrNotInTmux)}
	next, cmd := m.Update(keyMsg("w"))
	m = next.(tuiModel)
	next, _ = m.Update(cmd())
	if n := toastText(next.(tuiModel)); !strings.Contains(n, "not inside tmux") {
		t.Errorf("outside tmux: notice %q", n)
	}

	f := tmux.NewFake().
		On("display-message", "$1\t@1\t%1", nil).
		On("list-panes", paneRow("%1", "@1", "$1", "zsh"), nil).
		On("new-window", "%7\n", nil).
		On("split-window", "%8\n", nil).
		On("select-layout", "", nil).
		On("select-window", "", nil)
	m.tm = tmux.Client{Runner: f}
	next, cmd = m.Update(keyMsg("w"))
	m = next.(tuiModel)
	if len(f.Calls()) != 0 {
		t.Fatalf("w ran tmux on the UI loop: %q", f.CallLines())
	}
	msg := cmd().(tmuxPanesMsg)
	if msg.err != nil || !strings.Contains(msg.text, "opened 2 panes") {
		t.Fatalf("inside tmux: %+v", msg)
	}
	calls := f.Calls()
	var subs []string
	for _, c := range calls {
		subs = append(subs, c[0])
	}
	if got := strings.Join(subs, " "); got != "display-message list-panes new-window split-window select-layout select-window" {
		t.Fatalf("calls: %s", got)
	}
	if nw := strings.Join(calls[2], " "); !strings.Contains(nw, "-n nw:replay-safe") || !strings.Contains(nw, "--task") {
		t.Errorf("new-window: %s", nw)
	}
	for _, c := range calls[3:] {
		if !slices.Contains(c, "%7") {
			t.Errorf("%s does not address the new pane %%7: %q", c[0], c)
		}
	}

	// A stale result — a later press superseded it — is dropped.
	m2, _ := m.Update(keyMsg("w"))
	stale, _ := m2.(tuiModel).Update(msg)
	if n := toastText(stale.(tuiModel)); strings.Contains(n, "opened") {
		t.Errorf("a superseded w's result was shown: %q", n)
	}
	fresh, _ := m.Update(msg)
	if n := toastText(fresh.(tuiModel)); !strings.Contains(n, "opened 2 panes") {
		t.Errorf("the current w's result was not shown: %q", n)
	}

	// Pressed again while the window is open: switch to it, open nothing.
	f = tmux.NewFake().
		On("display-message", "$1\t@1\t%1", nil).
		On("list-panes", paneRow("%1", "@1", "$1", "zsh")+paneRow("%7", "@4", "$1", "nw:replay-safe"), nil).
		On("select-window", "", nil)
	m.tm = tmux.Client{Runner: f}
	_, cmd = m.Update(keyMsg("w"))
	msg = cmd().(tmuxPanesMsg)
	if got := f.CallLines(); len(got) != 3 || !strings.HasPrefix(got[2], "select-window -t @4") || !strings.Contains(msg.text, "already open") {
		t.Errorf("second w: %+v ran %q", msg, got)
	}
}

// A pane pinned to a task shows that task, follows it, and quits on esc.
func TestAPinnedPaneShowsItsTask(t *testing.T) {
	m := fixtureRun(t)
	m.followTask("schema")
	if s, ok := m.selectedTask(); !ok || s.ID != "schema" {
		t.Fatalf("pinned to %q", s.ID)
	}
	if got := viewText(m); !strings.Contains(got, "▸ Activity") || !strings.Contains(got, "schema") {
		t.Errorf("pinned view:\n%s", got)
	}
	next, cmd := m.Update(keyMsg("esc"))
	if !next.(tuiModel).quit || cmd == nil {
		t.Error("esc in a pinned pane did not quit")
	}
}

func keyMsg(k string) tea.KeyMsg {
	if k == "esc" {
		return tea.KeyMsg{Type: tea.KeyEsc}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
}
