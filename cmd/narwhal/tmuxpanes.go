// tmuxpanes.go opens a run's working tasks as tmux panes.
//
// The monitor shows one task at a time. With several workers going, the
// question is what each is doing now, side by side — and tmux already
// lays out and switches between panes better than a TUI can inside one.
// w opens a window in the current tmux session, named after the run, with
// one pane per working task, each running this monitor pinned to that
// task's activity and following it. The panes only read; closing them
// does nothing to the workers.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/keyolk/tuikit/tmux"

	"github.com/keyolk/narwhal/internal/broker"
)

// tmuxPlanCap bounds the panes: past this many, tmux's tiled layout makes
// each too small to read, and the rest are a keystroke away in the TUI.
const tmuxPlanCap = 6

// followTask opens the model on one task's activity, following, the way a
// pane opened by w does. Leaving the task view quits rather than showing
// the rest of the monitor in a pane meant for one task.
func (m *tuiModel) followTask(id string) {
	m.picker, m.pinnedTask = false, id
	m.detail, m.sessionTail = detailSession, true
	m.resetTaskView()
	m.taskTab = taskTabActivity
}

// tmuxLayout is what w opens: a window named after the run, and the shell
// command of each pane in it, one per working task.
type tmuxLayout struct {
	name  string
	cwd   string
	panes []string
}

// planPanes lays out the run's working tasks, one pane each, in a window
// named after the run. Without working tasks there are no panes.
func planPanes(self, runID, title, cwd string, tasks []broker.TaskSnapshot) tmuxLayout {
	var ids []string
	for _, t := range tasks {
		if t.State == broker.TaskDispatched {
			ids = append(ids, t.ID)
		}
	}
	if len(ids) > tmuxPlanCap {
		ids = ids[:tmuxPlanCap]
	}
	name := "nw:" + title
	if r := []rune(name); len(r) > 24 {
		name = string(r[:24])
	}
	l := tmuxLayout{name: name, cwd: cwd}
	for _, id := range ids {
		l.panes = append(l.panes, tmux.ShellQuote(self)+" monitor --run "+tmux.ShellQuote(runID)+" --task "+tmux.ShellQuote(id))
	}
	return l
}

// tmuxPanesMsg is how opening the panes went. gen drops a result that a
// later press of w has superseded.
type tmuxPanesMsg struct {
	gen  uint64
	text string
	err  error
}

// openTmuxPanes opens the watched run's working tasks as panes. The tmux
// calls run off the UI loop, in one command, so a second press cannot land
// between the window and its splits and leave a half-built window behind.
func (m *tuiModel) openTmuxPanes() tea.Cmd {
	key := m.keyName("run.tmux")
	self, err := os.Executable()
	if err != nil {
		self = "narwhal"
	}
	title := runLabel(m.live)
	if f := strings.Fields(title); len(f) > 0 {
		title = f[0]
	}
	l := planPanes(self, m.runID(), title, m.snap.CWD, m.snap.Tasks)
	if len(l.panes) == 0 {
		return func() tea.Msg { return noticeMsg{err: fmt.Errorf("no task is working — nothing to open")} }
	}
	var gen uint64
	m.tmuxGen, gen = m.tmuxGen.Next()
	tm := m.tm
	return func() tea.Msg {
		text, err := openPanes(context.Background(), tm, l, key)
		if errors.Is(err, tmux.ErrNotInTmux) {
			err = fmt.Errorf("not inside tmux — %s opens panes in the current tmux session", key)
		}
		return tmuxPanesMsg{gen: gen, text: text, err: err}
	}
}

// openPanes opens l in the current tmux session and switches to it. Every
// call after the first addresses the new window by its pane's ID: the
// window's name may be shared with a window the user made.
func openPanes(ctx context.Context, tm tmux.Client, l tmuxLayout, key string) (string, error) {
	// The window may still be open from an earlier w. A second one of the
	// same name would be two of the same thing, so go to it.
	loc, err := tm.Current(ctx)
	if err != nil {
		return "", err
	}
	panes, err := tm.Panes(ctx, tmux.PaneFilter{Target: loc.SessionID})
	if err != nil {
		return "", err
	}
	for _, p := range panes {
		if p.WindowName == l.name {
			if err := tm.SelectWindow(ctx, p.WindowID); err != nil {
				return "", err
			}
			return "window " + l.name + " is already open — switched to it (close it and press " + key + " to rebuild)", nil
		}
	}
	first, err := tm.NewWindow(ctx, "", tmux.WindowOpts{Name: l.name, Cwd: l.cwd, Command: tmux.Command{Shell: l.panes[0]}})
	if err != nil {
		return "", err
	}
	for _, sh := range l.panes[1:] {
		if _, err := tm.SplitWindow(ctx, first, tmux.SplitOpts{Cwd: l.cwd, Command: tmux.Command{Shell: sh}}); err != nil {
			return "", err
		}
		// Retile after each split, or a fourth split runs out of room.
		// select-layout has no typed call; the target is still an ID.
		if _, err := tm.Run("select-layout", "-t", first, "tiled"); err != nil {
			return "", err
		}
	}
	// The window opened in the background (-d); bring it up as w always did.
	if err := tm.SelectWindow(ctx, first); err != nil {
		return "", err
	}
	return fmt.Sprintf("opened %d panes in window %s", len(l.panes), l.name), nil
}
