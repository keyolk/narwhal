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
		section("The screen"),
		"  Top: " + styCyan.Render("1 Graph") + " the tasks and what waits on what · " +
			styCyan.Render("2 Node") + " the selected task, as it goes",
		"  Bottom: " + styCyan.Render("3 Timeline") + " what has happened · " +
			styCyan.Render("4 Radio") + " what the workers said, or " + styCyan.Render("Brief") + " where things stand",
		"",
		section("Move around"),
		key("1 2 3 4", "go to that pane    tab / shift-tab  next / previous"),
		key("j k  ↑ ↓", "move or scroll in the pane      g G  first / last"),
		key("h l  ← →", "in the graph, the box beside this one"),
		key("z", "this pane across the whole screen, and back"),
		key("4 (on 4)", "radio ↔ brief"),
		"",
		section("Open what you are looking at"),
		key("enter", "graph, node: the task — assignment, your instructions, the judge's"),
		key("", "calls and the worker's reasoning and tool calls, in order"),
		key("", "timeline: that moment in its task, or the message itself"),
		key("", "radio: the message; enter again goes to its task at that moment"),
		key("n p", "in a task or message, the next / previous one"),
		key("f", "follow the newest again    esc  back"),
		"",
		section("Act on the run"),
		key("i", "send the workers an instruction (@task to address one, ! urgent)"),
		key("a", "attach to the selected worker's live Claude session"),
		key("esc  [ ]", "the run list    previous / next run"),
		"",
		section("Layout"),
		key("b", "boxes ↔ lanes for the graph"),
		key("< >   + -", "graph width    top band height"),
		key("q", "quit"),
	}
	return pinFooter(strings.Join(lines, "\n"), styDim.Render("press any key to close"), m.height)
}
