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
	"fmt"
	"os"
	"os/exec"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

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

// tmuxPlan is the tmux commands that open the run's working tasks, one
// pane each, in a new window named after the run. Without working tasks
// the plan is empty.
func tmuxPlan(self, runID, title, cwd string, tasks []broker.TaskSnapshot) [][]string {
	var ids []string
	for _, t := range tasks {
		if t.State == broker.TaskDispatched {
			ids = append(ids, t.ID)
		}
	}
	if len(ids) > tmuxPlanCap {
		ids = ids[:tmuxPlanCap]
	}
	if len(ids) == 0 {
		return nil
	}
	follow := func(id string) string {
		return shellQuote(self) + " monitor --run " + shellQuote(runID) + " --task " + shellQuote(id)
	}
	name := "nw:" + title
	if r := []rune(name); len(r) > 24 {
		name = string(r[:24])
	}
	cmds := [][]string{{"tmux", "new-window", "-n", name, "-c", cwd, follow(ids[0])}}
	for _, id := range ids[1:] {
		cmds = append(cmds, []string{"tmux", "split-window", "-t", name, "-c", cwd, follow(id)})
		// Retile after each split, or a fourth split runs out of room.
		cmds = append(cmds, []string{"tmux", "select-layout", "-t", name, "tiled"})
	}
	return cmds
}

// shellQuote quotes a word for sh, the way tmux runs a pane's command.
func shellQuote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n'\"\\$`;&|<>()*?[]#~") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// openTmuxPanes runs the plan for the watched run.
func (m tuiModel) openTmuxPanes() tea.Cmd {
	if os.Getenv("TMUX") == "" {
		return func() tea.Msg {
			return noticeMsg{err: fmt.Errorf("not inside tmux — w opens panes in the current tmux session")}
		}
	}
	self, err := os.Executable()
	if err != nil {
		self = "narwhal"
	}
	title := runLabel(m.live)
	if f := strings.Fields(title); len(f) > 0 {
		title = f[0]
	}
	plan := tmuxPlan(self, m.runID(), title, m.snap.CWD, m.snap.Tasks)
	if len(plan) == 0 {
		return func() tea.Msg { return noticeMsg{err: fmt.Errorf("no task is working — nothing to open")} }
	}
	name := plan[0][3]
	return func() tea.Msg {
		// The window may still be open from an earlier w. A second one of
		// the same name would leave every -t ambiguous, so go to it.
		if out, err := runTmux([]string{"tmux", "list-windows", "-F", "#{window_name}"}); err == nil {
			for _, w := range strings.Split(out, "\n") {
				if strings.TrimSpace(w) == name {
					if out, err := runTmux([]string{"tmux", "select-window", "-t", name}); err != nil {
						return noticeMsg{err: fmt.Errorf("select-window: %v %s", err, strings.TrimSpace(out))}
					}
					return noticeMsg{text: "window " + name + " is already open — switched to it (close it and press w to rebuild)"}
				}
			}
		}
		for _, c := range plan {
			if out, err := runTmux(c); err != nil {
				return noticeMsg{err: fmt.Errorf("%s: %v %s", strings.Join(c[:2], " "), err, strings.TrimSpace(out))}
			}
		}
		n := (len(plan) + 1) / 2
		return noticeMsg{text: fmt.Sprintf("opened %d panes in window %s", n, plan[0][3])}
	}
}

// runTmux runs one tmux command. A variable so tests can record instead.
var runTmux = func(c []string) (string, error) {
	out, err := exec.Command(c[0], c[1:]...).CombinedOutput()
	return string(out), err
}
