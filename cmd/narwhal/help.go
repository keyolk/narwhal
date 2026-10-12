// help.go is the key reference behind `?`.
//
// The footer names the few keys that matter in the focused pane, enter
// first. Everything else lives here, grouped by what you are trying to do
// rather than by pane, so a key can be found from the question.
package main

import (
	"strings"
)

func (m tuiModel) viewHelp() string {
	key := func(k, what string) string {
		return "  " + styCyanBold.Render(padRight(k, 14)) + what
	}
	section := func(name string) string { return styTitle.Render(name) }
	lines := []string{
		styCyanBold.Render("Narwhal monitor") + styDim.Render("  — any key closes"),
		"",
		section("How work is organised"),
		"  " + styCyan.Render("home") + " every piece of work  →  " + styCyan.Render("flow") + " one piece of work, its goal, notes and steps  →",
		"  " + styCyan.Render("run") + " one step: a plan and its tasks  →  " + styCyan.Render("task") + " one worker's part. esc goes back up.",
		"",
		section("The run screen"),
		"  Left: " + styCyan.Render("1 Graph") + " the tasks and what waits on what — pick one here;",
		"        under it, the run's result so far and what it produced (r reads it, o opens them)",
		"  Right, one at a time: " + styCyan.Render("2 Task") + " the selected task, as it goes · " +
			styCyan.Render("3 Timeline") + " what has happened · " +
			styCyan.Render("4 Radio") + " what the workers said, or " + styCyan.Render("Brief") + " where things stand",
		"",
		section("Move around"),
		key("1 2 3 4", "go to that pane    tab / shift-tab  next / previous"),
		key("j k  ↑ ↓", "move or scroll in the pane      g G  first / last"),
		key("h l  ← →", "in the graph, the box beside this one; on the right, h back to the graph, l the next tab"),
		key("z", "this pane across the whole screen, and back"),
		key("4 (on 4)", "radio ↔ brief"),
		"",
		section("Open what you are looking at"),
		key("enter", "graph, task: the task — assignment, your instructions, the judge's"),
		key("", "calls and the worker's reasoning and tool calls, in order"),
		key("", "timeline: that moment in its task, or the message itself"),
		key("", "radio: the message; enter again goes to its task at that moment"),
		key("n p", "in a task or message, the next / previous one"),
		key("f", "follow the newest again    esc  back"),
		"",
		section("Results and the next step"),
		key("o", "outputs: files, PRs, branches, commits, reports and answers, each read its own way"),
		key("", "there: o opens it as its kind opens (browser, diff, $PAGER) · e edits a file · enter on an answer goes to its task"),
		key("", "from a flow, every step's outputs. O from a task's Activity tab"),
		key("r", "the run's result: its answer, what it produced, each task's conclusion"),
		key("", "there: e write the report · y copy the answer · 1-9 open a PR or a file"),
		key("n", "in a run, result or flow: the next step, planned with what came before"),
		key("Q", "the workers' questions: answer them together (enter answer · y as assumed · S send all)"),
		key("i", "in a flow: resume the selected step — its workers continue with your instruction"),
		key("l", "in a flow: into the step's list — what it was asked and produced; j/k choose, J/K read"),
		key("", "unaddressed, a finished run's instruction goes to the task that wrote its answer"),
		key("u", "the flow a run belongs to: e edit its notes · x export it · c close it"),
		key("[ ]", "the previous / next step of the flow"),
		"",
		section("Act on the run"),
		key("i", "send the workers an instruction (@task to address one, ! urgent)"),
		key("", "@task on a finished task reopens it: its worker's session resumes with the instruction"),
		key("", "from a task's view, i starts the line already addressed to it"),
		key("a", "attach to the selected worker's live Claude session"),
		key("w", "inside tmux: a window with a pane per working task, each following it"),
		key("esc", "back up: task → run → flow → home"),
		key("n", "on home, start a new request in the directory narwhal was opened in"),
		key("e  ctrl-e", "write it in $VISUAL / $EDITOR instead (vi if neither is set)"),
		"",
		section("Layout"),
		key("b", "boxes ↔ lanes for the graph"),
		key("< >", "graph width"),
		key("q", "quit"),
	}
	return pinFooter(strings.Join(lines, "\n"), styDim.Render("press any key to close"), m.height)
}
