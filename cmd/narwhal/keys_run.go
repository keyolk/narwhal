// keys_run.go turns a key into an action and runs it: the top of key
// handling, the keys that work on every screen, home, and the run view.
//
// Each screen's handler switches on the action keymap.go resolved, never on
// the key, so a rebinding in keys.yaml moves the key without touching code.
package main

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/keyolk/tuikit/keys"
	"github.com/keyolk/tuikit/toast"
)

// handleKey resolves msg in the active stack and runs the action.
func (m tuiModel) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case m.palette:
		return m.handlePaletteKey(msg)
	case m.help:
		return m.handleHelpKey(msg)
	case m.steering:
		return m.handleSteerKey(msg)
	case m.composing:
		return m.handleComposeKey(msg)
	case m.questionsView && m.answering:
		return m.handleAnswerKey(msg)
	}

	stack := m.activeStack()
	b, ok := m.keymap().Resolve(stack, msg, m.keyOpts())
	if m.removeStep != "" && (!ok || (b.Action != "home.remove" && b.Action != "flow.remove")) {
		// Any other key keeps a run or step a d asked to remove.
		m.removeStep = ""
		m.toasts = toast.Dismiss(m.toasts, "remove")
	}
	if !ok {
		return m, nil
	}
	return m.do(b, b.KeyIndex(msg)+1)
}

// do runs b, pressed as its n-th key (1-based; a numbered binding picks the
// n-th thing with it). The palette calls it too, as the first key.
func (m tuiModel) do(b keys.Binding, n int) (tea.Model, tea.Cmd) {
	switch b.Action {
	case "quit":
		m.quit = true
		return m, tea.Quit
	case "help":
		m.help, m.helpOff = true, 0
		return m, nil
	case "palette":
		m.openPalette()
		return m, nil
	}
	switch b.Ctx {
	case ctxHome:
		return m.doHome(b.Action)
	case ctxGraph, ctxNode, ctxTimeline, ctxRadio, ctxRun:
		return m.doRun(b.Action)
	case ctxResult:
		return m.doResult(b.Action, n)
	case ctxTask, ctxTaskRead, ctxTaskAct:
		return m.doTask(b.Action, n)
	case ctxMessage, ctxDetail:
		return m.doDetail(b.Action)
	case ctxFlow, ctxStepOut:
		return m.doFlow(b.Action, n)
	case ctxOutputs:
		return m.doOutputs(b.Action)
	case ctxQuestions:
		return m.doQuestions(b.Action, n)
	}
	return m, nil
}

// doHome drives the run list. It is a separate mode rather than a third
// pane because switching runs replaces everything on screen.
func (m tuiModel) doHome(action string) (tea.Model, tea.Cmd) {
	switch action {
	case "home.remove":
		return m.removeHomeRun()
	case "home.down":
		if m.runCur < len(m.runs)-1 {
			m.runCur++
		}
	case "home.up":
		if m.runCur > 0 {
			m.runCur--
		}
	case "home.top":
		m.runCur = 0
	case "home.bottom":
		m.runCur = len(m.runs) - 1
	case "home.open":
		if len(m.runs) == 0 {
			return m, nil
		}
		// A flow of several runs opens on the flow; a finished run on its
		// result, which is what it is opened for; a running one on its
		// graph.
		r := m.runs[m.runCur]
		if id := m.flowOf(r); len(m.flowRuns(id)) > 1 {
			m.openFlow(id, r.RunID)
			return m, nil
		}
		cmd := m.selectRun(m.runCur)
		m.picker = false
		if !runIsWorking(r) && r.Tasks > 0 {
			m.detail = detailResult
			return m, tea.Batch(cmd, m.prStatusCmd())
		}
		return m, cmd
	case "home.flow":
		// The selected run's flow, whatever its size: where notes are kept.
		if len(m.runs) > 0 {
			r := m.runs[m.runCur]
			m.openFlow(m.flowOf(r), r.RunID)
		}
	case "home.new":
		// i and / start one as well, since they are what opens a line to
		// type in everywhere else in the monitor.
		m.startCompose("")
	case "home.editor":
		m.homeErr = nil
		return m, m.openEditor()
	}
	return m, nil
}

// doRun drives the run view: the focused pane's keys and the run's own.
func (m tuiModel) doRun(action string) (tea.Model, tea.Cmd) {
	switch action {
	case "run.steer":
		// From a line of the timeline, the steer is to its task, quoting
		// the line; anywhere else, to the run. See timeline.go.
		if m.focus != focusTimeline || !m.steerFromTimeline() {
			m.startSteer("")
		}
	case "pane.next":
		m.focus = m.focus.next()
		m.followZoom()
	case "pane.prev":
		m.focus = m.focus.prev()
		m.followZoom()
	case "pane.1":
		// Jump straight to a pane. Tab cycles, which is fine for a step
		// sideways and tiresome once you know where you are going — and
		// the numbers are visible in the pane titles.
		m.focus = focusTasks
		m.followZoom()
	case "pane.2":
		m.focus = focusNode
		m.followZoom()
	case "pane.3":
		m.focus = focusTimeline
		m.followZoom()
	case "pane.4":
		// A number only ever names a place. Pane 4 has two tabs, so the
		// key that names it also turns it once you are there.
		if m.focus == focusRadio {
			m.briefTab = !m.briefTab
		}
		m.focus = focusRadio
		m.followZoom()
	case "run.zoom":
		// Zoom the focused pane to the whole body, tmux-style.
		if m.zoom == m.focus {
			m.zoom = -1
		} else {
			m.zoom = m.focus
		}
	case "run.wider":
		m.widthDelta += 4
	case "run.narrower":
		m.widthDelta -= 4
	case "graph.down":
		m.moveVertical(1)
	case "graph.up":
		m.moveVertical(-1)
	case "graph.left":
		// Inside the graph, h/l navigate the diagram: the box left or right
		// of the selected one. The graph is two-dimensional, so a direction
		// key has a direction to mean here.
		m.moveHorizontal(-1)
	case "graph.right":
		m.moveHorizontal(1)
	case "node.down":
		m.scrollNode(1)
	case "node.up":
		m.scrollNode(-1)
	case "cursor.down":
		m.moveCursor(1)
	case "cursor.up":
		m.moveCursor(-1)
	case "pane.graph":
		// From a flat list there is no horizontal axis: h steps back to
		// the graph, l to the right column's next tab.
		m.focus = focusTasks
	case "pane.nexttab":
		m.focus = map[focusPane]focusPane{focusNode: focusTimeline, focusTimeline: focusRadio, focusRadio: focusNode}[m.focus]
	case "run.top":
		if m.focus == focusNode {
			m.nodeScroll = 0
			break
		}
		m.jumpTo(0)
	case "run.bottom":
		if m.focus == focusNode {
			m.nodeScroll = nodeScrollTail
			break
		}
		m.jumpToEnd()
	case "run.pagedown":
		if m.focus == focusNode {
			m.scrollNode(10)
			break
		}
		m.moveCursor(10)
	case "run.pageup":
		if m.focus == focusNode {
			m.scrollNode(-10)
			break
		}
		m.moveCursor(-10)
	case "timeline.scene":
		// A scene opens into the events it was made of.
		m.toggleScene()
	case "run.open":
		// Enter opens what the cursor is on, wherever it is. See
		// navigation.go for where each pane leads.
		cmd := m.openFromFocus()
		return m, cmd
	case "run.task":
		// The selected task's view, from any pane: watching a worker is the
		// common reason to reach for the keyboard at all.
		if t, ok := m.selectedTask(); ok {
			m.openTask(t.ID, time.Time{})
		}
	case "run.attach":
		return m.attachSelected()
	case "run.follow":
		// Re-arm tail following after manual navigation.
		m.followTail = true
		m.jumpToEnd()
	case "run.lanes":
		// Boxes read as a diagram; lanes fit more tasks on screen.
		m.boxMode = !m.boxMode
	case "run.back":
		// Back out one level: to the flow a run is a step of, or home.
		if len(m.flowRuns(m.flowOf(m.live))) > 1 {
			m.openFlow(m.flowOf(m.live), m.live.RunID)
		} else {
			m.picker = true
		}
	case "run.outputs":
		cmd := m.openOutputs("")
		return m, cmd
	case "run.questions":
		m.openQuestions()
	case "run.tmux":
		// Not "return m, m.openTmuxPanes()": the call bumps m.tmuxGen, and
		// Go leaves unspecified whether m is read before or after it.
		cmd := m.openTmuxPanes()
		return m, cmd
	case "run.flow":
		// Up to the run's flow, however many steps it has: where its notes
		// are written and its steps' results read side by side.
		m.openFlow(m.flowOf(m.live), m.live.RunID)
	case "run.result":
		m.detail, m.detailScroll = detailResult, 0
		return m, m.prStatusCmd()
	case "run.next":
		m.startCompose(m.flowOf(m.live))
	case "run.stepnext", "run.stepprev":
		// Within a flow, the next step; otherwise the next run.
		d := 1
		if action == "run.stepprev" {
			d = -1
		}
		if cmd, ok := m.stepRun(d); ok {
			return m, cmd
		}
		if i := m.runCur + d; i >= 0 && i < len(m.runs) {
			// selectRun mutates m, so run it before m is copied into the
			// return value — otherwise the switch is silently discarded.
			cmd := m.selectRun(i)
			return m, cmd
		}
	}
	return m, nil
}

// attachSelected attaches to the selected worker's live Claude session. s
// shows what the worker has written; a puts you inside the session itself,
// which for a running worker is the only place anything is visible.
func (m tuiModel) attachSelected() (tea.Model, tea.Cmd) {
	t, ok := m.selectedTask()
	if !ok {
		return m, nil
	}
	cmd, err := m.attachToSession(t.ID)
	if err != nil {
		m.err = err
		return m, nil
	}
	return m, cmd
}
