package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/keyolk/narwhal/internal/broker"
	"github.com/keyolk/narwhal/internal/store"
)

// One pane per working task, in a window named after the run, each
// running the monitor pinned to its task; the layout is retiled after
// every split.
func TestTheTmuxPlanOpensAPanePerWorkingTask(t *testing.T) {
	tasks := []broker.TaskSnapshot{
		{ID: "api", State: broker.TaskDispatched},
		{ID: "done", State: broker.TaskCompleted},
		{ID: "it's", State: broker.TaskDispatched},
	}
	plan := tmuxPlan("/usr/local/bin/narwhal", "r1", "lock", "/src/my repo", tasks)
	var lines []string
	for _, c := range plan {
		lines = append(lines, strings.Join(c, " | "))
	}
	want := []string{
		"tmux | new-window | -n | nw:lock | -c | /src/my repo | /usr/local/bin/narwhal monitor --run r1 --task api",
		"tmux | split-window | -t | nw:lock | -c | /src/my repo | /usr/local/bin/narwhal monitor --run r1 --task 'it'\\''s'",
		"tmux | select-layout | -t | nw:lock | tiled",
	}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Errorf("plan:\n%s\nwant:\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
	if tmuxPlan("n", "r1", "x", "/", []broker.TaskSnapshot{{ID: "a", State: broker.TaskCompleted}}) != nil {
		t.Error("a plan for a run with nothing working")
	}
	many := make([]broker.TaskSnapshot, 10)
	for i := range many {
		many[i] = broker.TaskSnapshot{ID: string(rune('a' + i)), State: broker.TaskDispatched}
	}
	if p := tmuxPlan("n", "r", "x", "/", many); len(p) != 1+2*(tmuxPlanCap-1) {
		t.Errorf("%d commands for 10 tasks, want the cap of %d panes", len(p), tmuxPlanCap)
	}
}

// w runs the plan inside tmux, and says why not outside it.
func TestWOpensThePanes(t *testing.T) {
	var ran [][]string
	saved := runTmux
	runTmux = func(c []string) (string, error) { ran = append(ran, c); return "", nil }
	t.Cleanup(func() { runTmux = saved })

	m := fixtureRun(t)
	m.live = store.LiveRun{RunID: m.snap.RunID, Prompt: "replay-safe lock API"}
	t.Setenv("TMUX", "")
	_, cmd := m.Update(keyMsg("w"))
	if msg := cmd().(noticeMsg); msg.err == nil || !strings.Contains(msg.err.Error(), "not inside tmux") {
		t.Errorf("outside tmux: %+v", msg)
	}
	t.Setenv("TMUX", "/tmp/x,1,0")
	_, cmd = m.Update(keyMsg("w"))
	msg := cmd().(noticeMsg)
	if msg.err != nil || len(ran) != 1 || ran[0][1] != "new-window" || !strings.Contains(ran[0][6], "--task api") {
		t.Errorf("inside tmux: %+v ran %v", msg, ran)
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
