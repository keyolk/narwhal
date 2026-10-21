// keymap.go is the monitor's one binding table. Dispatch, every footer, the
// `?` help overlay and the `:` palette are all read from it, so a key cannot
// do one thing while the footer or the help says another.
//
// The help and footers used to be written by hand next to a string switch in
// each handler, and they drifted: help.go listed keys per screen in prose
// that nothing checked against the switch. Here each key is a named action in
// a context; the screen decides which contexts are active, innermost first,
// and a key no active binding claims passes outward (see activeStack).
//
// Users rebind actions in ~/.config/narwhal/keys.yaml (keys.ConfigPath),
// by action ("quit: [x]") or by context ("flow.flow.down: [n]").
package main

import "github.com/keyolk/tuikit/keys"

// Contexts. A screen is a stack of them, innermost first.
const (
	ctxGlobal keys.Ctx = "global"

	ctxHome keys.Ctx = "home"

	// The run view: the focused pane over the run's own keys.
	ctxRun      keys.Ctx = "run"
	ctxGraph    keys.Ctx = "graph"
	ctxNode     keys.Ctx = "node"
	ctxTimeline keys.Ctx = "timeline"
	ctxRadio    keys.Ctx = "radio"

	// The detail views share scrolling and back; each adds its own.
	ctxDetail   keys.Ctx = "detail"
	ctxMessage  keys.Ctx = "message"
	ctxResult   keys.Ctx = "result"
	ctxTask     keys.Ctx = "task"
	ctxTaskRead keys.Ctx = "task.read" // the Summary and Messages tabs
	ctxTaskAct  keys.Ctx = "task.activity"

	ctxFlow    keys.Ctx = "flow"
	ctxStepOut keys.Ctx = "flow.output" // the step's outputs, once they have the keys

	ctxOutputs   keys.Ctx = "outputs"
	ctxQuestions keys.Ctx = "questions"

	// Text inputs. They see keys raw (no Korean normalization) and claim
	// only their editing keys; anything else is typed into the line.
	ctxSteer   keys.Ctx = "steer"
	ctxCompose keys.Ctx = "compose"
	ctxAnswer  keys.Ctx = "answer"

	// Overlays.
	ctxHelp    keys.Ctx = "help"
	ctxPalette keys.Ctx = "palette"
)

// Help sections: one per screen, or per part of the run screen, so a key
// is found under the place it is pressed.
const (
	grpHome    = "Home"
	grpGraph   = "Run: the graph (pane 1)"
	grpPanes   = "Run: task, timeline and radio (panes 2-4)"
	grpMove    = "Run: moving around"
	grpOpen    = "Run: open what you are looking at"
	grpAct     = "Run: act on it"
	grpResults = "Run: results and the next step"
	grpLayout  = "Run: layout"
	grpRead    = "Reading a message or a task"
	grpTask    = "Task view"
	grpResult  = "Result"
	grpFlow    = "Flow"
	grpOutputs = "Outputs"
	grpQuest   = "Questions"
	grpLine    = "Typing a line"
	grpApp     = "Anywhere"
)

// keyStacks lists every stack the monitor can be in. The conflict test and
// LoadOverrides check against them, and the help overlay documents them.
var keyStacks = [][]keys.Ctx{
	{ctxHome, ctxGlobal},
	{ctxGraph, ctxRun, ctxGlobal},
	{ctxNode, ctxRun, ctxGlobal},
	{ctxTimeline, ctxRun, ctxGlobal},
	{ctxRadio, ctxRun, ctxGlobal},
	{ctxMessage, ctxDetail, ctxGlobal},
	{ctxResult, ctxDetail, ctxGlobal},
	{ctxTaskRead, ctxTask, ctxDetail, ctxGlobal},
	{ctxTaskAct, ctxTask, ctxDetail, ctxGlobal},
	{ctxTask, ctxDetail, ctxGlobal},
	{ctxStepOut, ctxFlow, ctxGlobal},
	{ctxFlow, ctxGlobal},
	{ctxOutputs, ctxGlobal},
	{ctxQuestions, ctxGlobal},
	{ctxSteer},
	{ctxCompose},
	{ctxAnswer},
	{ctxHelp},
	{ctxPalette},
}

var digits = []string{"1", "2", "3", "4", "5", "6", "7", "8", "9"}

// numberedKeys binds 1…9 to one action that picks the n-th thing; the handler
// reads n from Binding.KeyIndex, so a rebinding to other keys still counts
// from one. The keys show as "1-9", and the palette, which has no key to
// give the action, leaves it out.
func numberedKeys(bd keys.Binding) keys.Binding {
	bd.Keys = digits
	bd.KeyLabel, bd.NoPalette = "1-9", true
	return bd
}

func b(ctx keys.Ctx, group, action, help string, ks ...string) keys.Binding {
	return keys.Binding{Action: action, Keys: ks, Ctx: ctx, Group: group, Help: help}
}

// foot puts a binding in the footer at priority p (lower is kept longer).
func foot(bd keys.Binding, p int) keys.Binding { bd.Footer = p; return bd }

func grouped(bd keys.Binding, p int, group string) keys.Binding {
	bd.Footer, bd.FooterGroup = p, group
	return bd
}

func labels(bd keys.Binding, l map[string]string) keys.Binding { bd.Labels = l; return bd }

func short(bd keys.Binding, s string) keys.Binding { bd.Short = s; return bd }

func noPalette(bd keys.Binding) keys.Binding { bd.NoPalette = true; return bd }

// defaultKeymap is the table. Footer priorities reproduce the order the
// hand-written footers had: what enter does first, then the pane's own keys,
// then the run's, with back last.
var defaultKeymap = keys.New(bindings()...)

func bindings() []keys.Binding {
	var bs []keys.Binding
	add := func(more ...keys.Binding) { bs = append(bs, more...) }

	// Everywhere outside a text line.
	help := foot(b(ctxGlobal, grpApp, "help", "this key reference", "?"), 1)
	help.Pinned, help.Short = true, "help"
	add(help,
		noPalette(b(ctxGlobal, grpApp, "palette", "find any action by name", ":")),
		b(ctxGlobal, grpApp, "quit", "quit", "ctrl+c"),
	)

	// Home.
	homeQuit := foot(b(ctxHome, grpApp, "quit", "quit", "q", "esc"), 2)
	homeQuit.Pinned = true
	add(
		foot(b(ctxHome, grpHome, "home.open", "open the work: a flow, a finished run's result, a running run's graph", "enter", "l", "right"), 1),
		short(foot(b(ctxHome, grpHome, "home.new", "start a new request in the directory narwhal was opened in", "n", "i", "/"), 2), "new request"),
		short(foot(b(ctxHome, grpHome, "home.editor", "write the request in $VISUAL / $EDITOR (vi if neither is set)", "e", "ctrl+e"), 3), "write in $EDITOR"),
		short(foot(b(ctxHome, grpHome, "home.flow", "the selected run's flow, where its notes are kept", "u"), 4), "flow"),
		short(foot(b(ctxHome, grpHome, "home.remove", "take the run off the list (twice); its record stays", "d"), 5), "remove"),
		grouped(b(ctxHome, grpHome, "home.down", "next run", "j", "down"), 6, "move"),
		grouped(b(ctxHome, grpHome, "home.up", "previous run", "k", "up"), 6, "move"),
		b(ctxHome, grpHome, "home.top", "first run", "g", "home"),
		b(ctxHome, grpHome, "home.bottom", "last run", "G", "end"),
		homeQuit,
	)

	// The run view's panes. Each claims its movement keys; the rest of the
	// run's keys are the same in every pane.
	add(
		grouped(b(ctxGraph, grpGraph, "graph.left", "the box left of this one", "h", "left"), 2, "move"),
		grouped(b(ctxGraph, grpGraph, "graph.down", "the box below", "j", "down"), 2, "move"),
		grouped(b(ctxGraph, grpGraph, "graph.up", "the box above", "k", "up"), 2, "move"),
		grouped(b(ctxGraph, grpGraph, "graph.right", "the box right of this one", "l", "right"), 2, "move"),

		grouped(b(ctxNode, grpPanes, "node.down", "scroll the task down", "j", "down"), 2, "scroll"),
		grouped(b(ctxNode, grpPanes, "node.up", "scroll the task up", "k", "up"), 2, "scroll"),
		b(ctxNode, grpPanes, "pane.graph", "back to the graph", "h", "left"),
		short(foot(b(ctxNode, grpPanes, "pane.nexttab", "the right column's next tab", "l", "right"), 3), "next tab"),

		short(foot(b(ctxTimeline, grpPanes, "timeline.scene", "open a scene into the events it was made of", " "), 2), "open scene"),
		grouped(b(ctxTimeline, grpPanes, "cursor.down", "next line", "j", "down"), 3, "line"),
		grouped(b(ctxTimeline, grpPanes, "cursor.up", "previous line", "k", "up"), 3, "line"),
		b(ctxTimeline, grpPanes, "pane.graph", "back to the graph", "h", "left"),
		short(foot(b(ctxTimeline, grpPanes, "pane.nexttab", "the right column's next tab", "l", "right"), 4), "next tab"),

		grouped(b(ctxRadio, grpPanes, "cursor.down", "next line", "j", "down"), 2, "move"),
		grouped(b(ctxRadio, grpPanes, "cursor.up", "previous line", "k", "up"), 2, "move"),
		b(ctxRadio, grpPanes, "pane.graph", "back to the graph", "h", "left"),
		b(ctxRadio, grpPanes, "pane.nexttab", "the right column's next tab", "l", "right"),
	)

	// Not in the run's footer: its width goes to the run's keys, and esc
	// leads home, whose footer has quit.
	runQuit := b(ctxRun, grpApp, "quit", "quit", "q")
	add(
		labels(foot(b(ctxRun, grpOpen, "run.open", "open what the cursor is on", "enter"), 1), map[string]string{
			"graph": "open task", "node": "open task", "timeline": "go to it", "radio": "open message",
		}),
		short(foot(b(ctxRun, grpMove, "run.follow", "follow the newest again", "f"), 3), "follow"),
		labels(foot(b(ctxRun, grpMove, "pane.4", "pane 4: radio, or on pane 4, radio ↔ brief", "4"), 4), map[string]string{
			"radio": "→ brief", "brief": "→ radio",
		}),
		b(ctxRun, grpMove, "pane.1", "pane 1: the graph", "1"),
		b(ctxRun, grpMove, "pane.2", "pane 2: the selected task", "2"),
		b(ctxRun, grpMove, "pane.3", "pane 3: the timeline", "3"),
		b(ctxRun, grpMove, "pane.next", "next pane", "tab"),
		b(ctxRun, grpMove, "pane.prev", "previous pane", "shift+tab"),
		b(ctxRun, grpMove, "run.top", "first", "g", "home"),
		b(ctxRun, grpMove, "run.bottom", "last", "G", "end"),
		b(ctxRun, grpMove, "run.pagedown", "page down", "ctrl+d", "pgdown"),
		b(ctxRun, grpMove, "run.pageup", "page up", "ctrl+u", "pgup"),
		labels(foot(b(ctxRun, grpLayout, "run.lanes", "boxes ↔ lanes for the graph", "b"), 4),
			map[string]string{"boxes": "lanes", "lanes": "boxes"}),
		labels(foot(b(ctxRun, grpLayout, "run.zoom", "this pane across the whole screen, and back", "z"), 10),
			map[string]string{"zoomed": "unzoom", "": "zoom"}),
		labels(foot(b(ctxRun, grpAct, "run.steer", "send the workers an instruction (@task to address one, ! urgent)", "i"), 11),
			map[string]string{"": "steer", "timeline": "correct this"}),
		short(foot(b(ctxRun, grpResults, "run.outputs", "outputs: files, PRs, branches, commits, reports and answers", "o", "O"), 12), "outputs"),
		short(foot(b(ctxRun, grpResults, "run.result", "the run's result: its answer, what it produced, each task's conclusion", "r"), 13), "result"),
		short(foot(b(ctxRun, grpResults, "run.next", "the next step of this run's flow, planned with what came before", "n"), 14), "next step"),
		short(foot(b(ctxRun, grpMove, "run.back", "back up: to the run's flow, or home", "esc"), 15), "back"),
		b(ctxRun, grpOpen, "run.task", "the selected task's view, from any pane", "s"),
		b(ctxRun, grpAct, "run.attach", "attach to the selected worker's live Claude session", "a"),
		b(ctxRun, grpResults, "run.questions", "the workers' questions, answered together", "Q"),
		b(ctxRun, grpAct, "run.tmux", "inside tmux: a window with a pane per working task", "w"),
		b(ctxRun, grpResults, "run.flow", "the flow this run belongs to", "u"),
		b(ctxRun, grpMove, "run.stepnext", "the next step of the flow, or the next run", "]", "shift+right"),
		b(ctxRun, grpMove, "run.stepprev", "the previous step of the flow, or the previous run", "[", "shift+left"),
		runQuit,
	)
	// The column boundary: < > = and a drag on the divider (split_band.go).
	add(splitBindings(ctxRun, grpLayout, 5)...)

	// Detail views: a message, a task, the result.
	add(
		foot(b(ctxDetail, grpRead, "detail.back", "back", "esc", "q"), 2),
		grouped(b(ctxDetail, grpRead, "detail.down", "scroll down", "j", "down"), 3, "scroll"),
		grouped(b(ctxDetail, grpRead, "detail.up", "scroll up", "k", "up"), 3, "scroll"),
		b(ctxDetail, grpRead, "detail.pagedown", "page down", "ctrl+d", "pgdown"),
		b(ctxDetail, grpRead, "detail.pageup", "page up", "ctrl+u", "pgup"),
		b(ctxDetail, grpRead, "detail.top", "first line", "g", "home"),
		b(ctxDetail, grpRead, "detail.bottom", "the newest, and stay there", "G", "end"),
		short(foot(b(ctxDetail, grpRead, "detail.attach", "attach to the task's live Claude session", "a"), 12), "attach"),

		short(foot(b(ctxMessage, grpRead, "message.task", "the task it came from or went to, at that moment", "enter", "s"), 1), "its task at this moment"),
		grouped(b(ctxMessage, grpRead, "detail.next", "the next message", "n"), 4, "message"),
		grouped(b(ctxMessage, grpRead, "detail.prev", "the previous message", "p"), 4, "message"),
		short(foot(messageLinkBinding(), 5), "open link"),

		short(foot(b(ctxResult, grpResult, "result.outputs", "outputs, each read its own way", "o", "O"), 1), "outputs"),
		short(foot(b(ctxResult, grpResult, "result.report", "write the run's report", "e"), 2), "write report"),
		short(foot(b(ctxResult, grpResult, "result.copy", "copy the answer", "y"), 3), "copy answer"),
		short(foot(numberedKeys(b(ctxResult, grpResult, "result.artifact", "open a PR or a file by its number")), 4), "open a PR or file"),
		short(foot(b(ctxResult, grpResult, "result.next", "the next step, planned with what came before", "n", "i"), 5), "next step"),

		short(foot(b(ctxTask, grpTask, "task.steer", "an instruction for this task (reopens it when finished)", "i"), 1), "instruct"),
		foot(b(ctxTask, grpTask, "task.nexttab", "next tab", "tab", "l", "right"), 5),
		b(ctxTask, grpTask, "task.prevtab", "previous tab", "shift+tab", "h", "left"),
		short(foot(b(ctxTask, grpTask, "task.outputs", "this run's outputs, this task's first", "O"), 6), "outputs"),
		foot(b(ctxTask, grpTask, "task.follow", "follow the newest output", "f"), 0),
		grouped(b(ctxTask, grpTask, "detail.next", "the next task", "n"), 9, "task"),
		grouped(b(ctxTask, grpTask, "detail.prev", "the previous task", "p"), 9, "task"),

		grouped(b(ctxTaskRead, grpTask, "read.down", "scroll down", "j", "down"), 2, "scroll"),
		grouped(b(ctxTaskRead, grpTask, "read.up", "scroll up", "k", "up"), 2, "scroll"),
		b(ctxTaskRead, grpTask, "read.pagedown", "page down", "ctrl+d", "pgdown", " "),
		b(ctxTaskRead, grpTask, "read.pageup", "page up", "ctrl+u", "pgup"),
		b(ctxTaskRead, grpTask, "read.top", "top", "g", "home"),
		b(ctxTaskRead, grpTask, "read.bottom", "bottom", "G", "end"),
		short(foot(b(ctxTaskRead, grpTask, "task.outputs", "this run's outputs, this task's first", "o"), 6), "outputs"),
		short(foot(b(ctxTaskRead, grpTask, "read.request", "open or fold the request", "enter"), 7), "the request"),
		short(foot(numberedKeys(b(ctxTaskRead, grpTask, "read.artifact", "open a PR or a file by its number")), 8), "open a PR or file"),

		grouped(b(ctxTaskAct, grpTask, "act.down", "next entry", "j", "down"), 2, "entry"),
		grouped(b(ctxTaskAct, grpTask, "act.up", "previous entry", "k", "up"), 2, "entry"),
		short(foot(b(ctxTaskAct, grpTask, "act.toggle", "open or fold the entry", "enter", " "), 3), "open/fold"),
		short(foot(b(ctxTaskAct, grpTask, "act.pager", "the entry in $PAGER", "o"), 4), "$PAGER"),
		short(foot(b(ctxTaskAct, grpTask, "act.all", "open or fold every entry", "E"), 6), "open all"),
		b(ctxTaskAct, grpTask, "act.pagedown", "page down", "ctrl+d", "pgdown"),
		b(ctxTaskAct, grpTask, "act.pageup", "page up", "ctrl+u", "pgup"),
		b(ctxTaskAct, grpTask, "act.top", "first entry", "g", "home"),
		b(ctxTaskAct, grpTask, "act.follow", "the newest, and stay there", "G", "end", "f"),
	)

	// Flow.
	flowQuit := b(ctxFlow, grpApp, "quit", "quit", "q")
	add(
		grouped(b(ctxFlow, grpFlow, "flow.down", "next step", "j", "down"), 1, "step"),
		grouped(b(ctxFlow, grpFlow, "flow.up", "previous step", "k", "up"), 1, "step"),
		short(foot(b(ctxFlow, grpFlow, "flow.steer", "resume the selected step: its workers continue with your instruction", "i"), 2), "resume the step"),
		short(foot(b(ctxFlow, grpFlow, "flow.compose", "the next step, planned with what came before", "n"), 3), "next step"),
		short(foot(b(ctxFlow, grpFlow, "flow.read", "into the step's outputs: what it was asked and produced", "l", "tab", "right"), 4), "read"),
		short(foot(b(ctxFlow, grpFlow, "flow.steps", "back to the step list", "h", "shift+tab", "left"), 4), "back to steps"),
		labels(foot(b(ctxFlow, grpFlow, "flow.zoom", "the step's detail across the whole width, and back", "z"), 5),
			map[string]string{"": "widen", "zoomed": "narrow"}),
		short(foot(b(ctxFlow, grpFlow, "flow.open", "open the step's run", "enter"), 6), "open run"),
		short(foot(b(ctxFlow, grpFlow, "flow.outputs", "every step's outputs, from this step's", "o", "O"), 7), "outputs"),
		short(foot(b(ctxFlow, grpFlow, "flow.result", "the step's result", "r"), 8), "result"),
		short(foot(b(ctxFlow, grpFlow, "flow.notes", "edit the flow's notes", "e"), 9), "notes"),
		short(foot(b(ctxFlow, grpFlow, "flow.export", "export the flow", "x"), 10), "export"),
		short(foot(b(ctxFlow, grpFlow, "flow.remove", "take the step out of the flow (twice); its record stays", "d"), 11), "remove step"),
		labels(foot(b(ctxFlow, grpFlow, "flow.close", "close or reopen the flow", "c"), 12),
			map[string]string{"": "close", "closed": "reopen"}),
		short(foot(b(ctxFlow, grpFlow, "flow.pagedown", "read on in the step's detail", "J", " ", "ctrl+d", "pgdown"), 6), "read"),
		b(ctxFlow, grpFlow, "flow.pageup", "read back in the step's detail", "K", "ctrl+u", "pgup"),
		b(ctxFlow, grpFlow, "flow.top", "first step", "g", "home"),
		b(ctxFlow, grpFlow, "flow.bottom", "last step", "G", "end"),
		numberedKeys(b(ctxFlow, grpFlow, "flow.artifact", "open the step's PR or file by its number")),
		labels(foot(b(ctxFlow, grpFlow, "flow.back", "back: the widened detail, the step list, then home", "esc"), 13),
			map[string]string{"": "home", "detail": "steps", "zoomed": "narrow"}),
		flowQuit,

		grouped(b(ctxStepOut, grpFlow, "stepout.down", "next output", "j", "down"), 1, "choose output"),
		grouped(b(ctxStepOut, grpFlow, "stepout.up", "previous output", "k", "up"), 1, "choose output"),
		b(ctxStepOut, grpFlow, "stepout.top", "top of the output", "g", "home"),
		b(ctxStepOut, grpFlow, "stepout.bottom", "end of the output", "G", "end"),
		short(foot(b(ctxStepOut, grpFlow, "stepout.open", "open the output as its kind opens", "o"), 3), "open"),
		short(foot(b(ctxStepOut, grpFlow, "stepout.edit", "edit the file", "e"), 4), "edit"),
		short(foot(b(ctxStepOut, grpFlow, "stepout.copy", "copy it", "y"), 5), "copy"),
		short(foot(b(ctxStepOut, grpFlow, "stepout.enter", "the task that gave it, or open it", "enter"), 6), "its task"),
	)

	// Outputs.
	add(
		grouped(b(ctxOutputs, grpOutputs, "outputs.down", "next output", "j", "down"), 1, "choose"),
		grouped(b(ctxOutputs, grpOutputs, "outputs.up", "previous output", "k", "up"), 1, "choose"),
		grouped(b(ctxOutputs, grpOutputs, "outputs.pagedown", "read on", "J", " ", "ctrl+d", "pgdown"), 2, "page"),
		grouped(b(ctxOutputs, grpOutputs, "outputs.pageup", "read back", "K", "ctrl+u", "pgup"), 2, "page"),
		b(ctxOutputs, grpOutputs, "outputs.top", "top", "g", "home"),
		b(ctxOutputs, grpOutputs, "outputs.bottom", "end", "G", "end"),
		labels(foot(b(ctxOutputs, grpOutputs, "outputs.open", "open it as its kind opens (browser, diff, $PAGER)", "o"), 3), map[string]string{
			"file": "$PAGER", "pr": "browser", "commit": "full diff", "branch": "log with files", "text": "$PAGER",
		}),
		short(foot(b(ctxOutputs, grpOutputs, "outputs.edit", "edit the file in $EDITOR", "e"), 4), "$EDITOR"),
		labels(foot(b(ctxOutputs, grpOutputs, "outputs.copy", "copy it", "y"), 5), map[string]string{
			"file": "copy contents", "pr": "copy URL", "commit": "copy hash", "branch": "copy name", "text": "copy",
		}),
		labels(foot(b(ctxOutputs, grpOutputs, "outputs.enter", "an answer: go to its task; anything else: open it", "enter"), 3),
			map[string]string{"text": "go to the task"}),
		foot(b(ctxOutputs, grpOutputs, "outputs.back", "back", "esc", "q", "O"), 10),
	)

	add(splitBindings(ctxFlow, grpFlow, 14)...)
	add(splitBindings(ctxOutputs, grpOutputs, 11)...)

	// Questions.
	add(
		grouped(b(ctxQuestions, grpQuest, "questions.down", "next question", "j", "down"), 1, "question"),
		grouped(b(ctxQuestions, grpQuest, "questions.up", "previous question", "k", "up"), 1, "question"),
		foot(numberedKeys(b(ctxQuestions, grpQuest, "questions.pick", "pick an option")), 2),
		foot(b(ctxQuestions, grpQuest, "questions.answer", "answer", "enter", "a"), 3),
		foot(b(ctxQuestions, grpQuest, "questions.assume", "as assumed", "y"), 4),
		foot(b(ctxQuestions, grpQuest, "questions.clear", "clear", "x", "backspace"), 5),
		// s as well as S: under a Korean input source shift+ㄴ still
		// arrives as ㄴ, which normalizes to s.
		foot(b(ctxQuestions, grpQuest, "questions.send", "send all", "S", "s"), 6),
		foot(b(ctxQuestions, grpQuest, "questions.back", "back", "esc", "q", "Q"), 10),
	)

	// Text lines. ctrl+c quits from them too.
	add(
		labels(foot(b(ctxSteer, grpLine, "steer.send", "send the instruction", "enter"), 1),
			map[string]string{"": "send", "resume": "resumes its worker with this"}),
		foot(b(ctxSteer, grpLine, "steer.cancel", "cancel", "esc"), 2),
		b(ctxSteer, grpApp, "quit", "quit", "ctrl+c"),

		foot(b(ctxCompose, grpLine, "compose.start", "start", "enter"), 1),
		foot(b(ctxCompose, grpLine, "compose.editor", "open in $EDITOR", "ctrl+e"), 2),
		foot(b(ctxCompose, grpLine, "compose.cancel", "cancel", "esc"), 3),
		b(ctxCompose, grpApp, "quit", "quit", "ctrl+c"),

		foot(b(ctxAnswer, grpLine, "answer.keep", "keep", "enter"), 1),
		foot(b(ctxAnswer, grpLine, "answer.cancel", "cancel", "esc"), 2),
		b(ctxAnswer, grpApp, "quit", "quit", "ctrl+c"),
	)

	// Overlays.
	add(
		foot(b(ctxHelp, grpApp, "help.close", "close", "esc", "q", "?"), 1),
		grouped(b(ctxHelp, grpApp, "help.down", "scroll down", "j", "down"), 2, "scroll"),
		grouped(b(ctxHelp, grpApp, "help.up", "scroll up", "k", "up"), 2, "scroll"),
		b(ctxHelp, grpApp, "help.pagedown", "page down", "ctrl+d", "pgdown", " "),
		b(ctxHelp, grpApp, "help.pageup", "page up", "ctrl+u", "pgup"),
		b(ctxHelp, grpApp, "quit", "quit", "ctrl+c"),

		foot(b(ctxPalette, grpApp, "palette.run", "run", "enter"), 1),
		grouped(b(ctxPalette, grpApp, "palette.down", "next", "down", "ctrl+n"), 2, "choose"),
		grouped(b(ctxPalette, grpApp, "palette.up", "previous", "up", "ctrl+p"), 2, "choose"),
		foot(b(ctxPalette, grpApp, "palette.close", "close", "esc"), 3),
		b(ctxPalette, grpApp, "quit", "quit", "ctrl+c"),
	)
	return bs
}
