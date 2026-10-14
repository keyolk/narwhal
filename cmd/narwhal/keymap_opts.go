// keymap_opts.go is the model's side of the binding table: which contexts
// have the keys now, which actions can run, which are worth footer room,
// which label a key wears, and how footers and help are drawn.
package main

import (
	"github.com/charmbracelet/lipgloss"
	"github.com/keyolk/tuikit/keys"
)

// keymap is the model's table, or the default for a model built without
// one (tests build literals).
func (m tuiModel) keymap() keys.Keymap { return m.km.Or(defaultKeymap) }

// activeStack is the contexts that have the keys now, innermost first. It
// mirrors the order the screens are drawn in View.
func (m tuiModel) activeStack() []keys.Ctx {
	switch {
	case m.palette:
		return []keys.Ctx{ctxPalette}
	case m.help:
		return []keys.Ctx{ctxHelp}
	case m.steering:
		return []keys.Ctx{ctxSteer}
	case m.composing:
		return []keys.Ctx{ctxCompose}
	case m.questionsView && m.answering:
		return []keys.Ctx{ctxAnswer}
	}
	return m.screenStack()
}

// screenStack is the stack of the screen under any overlay or text line.
func (m tuiModel) screenStack() []keys.Ctx {
	switch {
	case m.questionsView:
		return []keys.Ctx{ctxQuestions, ctxGlobal}
	case m.outputsView:
		return []keys.Ctx{ctxOutputs, ctxGlobal}
	case m.flowView && !m.picker:
		if m.flowDetail {
			return []keys.Ctx{ctxStepOut, ctxFlow, ctxGlobal}
		}
		return []keys.Ctx{ctxFlow, ctxGlobal}
	case m.picker:
		return []keys.Ctx{ctxHome, ctxGlobal}
	}
	switch m.detail {
	case detailResult:
		return []keys.Ctx{ctxResult, ctxDetail, ctxGlobal}
	case detailMessage:
		return []keys.Ctx{ctxMessage, ctxDetail, ctxGlobal}
	case detailSession:
		t, ok := m.selectedTask()
		if !ok {
			return []keys.Ctx{ctxTask, ctxDetail, ctxGlobal}
		}
		if m.currentTaskTab(t) == taskTabActivity {
			return []keys.Ctx{ctxTaskAct, ctxTask, ctxDetail, ctxGlobal}
		}
		return []keys.Ctx{ctxTaskRead, ctxTask, ctxDetail, ctxGlobal}
	}
	pane := map[focusPane]keys.Ctx{focusTasks: ctxGraph, focusNode: ctxNode, focusTimeline: ctxTimeline, focusRadio: ctxRadio}[m.focus]
	return []keys.Ctx{pane, ctxRun, ctxGlobal}
}

// keyOpts is the model's view of its actions for every keys call: what can
// run, what is worth footer room, and which label a key wears now.
func (m tuiModel) keyOpts() keys.Opts {
	return keys.Opts{Enabled: m.keyEnabled, Visible: m.keyVisible, State: m.keyState, Label: m.keyLabel}
}

// keyName is the key that runs action on the screen now, named the way the
// footer names it, for prose that says what to press ("press i to send
// one"): a keys.yaml rebinding moves the prose with the key. Prose about
// another screen's key (the steering line under a run's result) names the
// action's first key in the table.
func (m tuiModel) keyName(action string) string {
	km := m.keymap()
	k, ok := km.KeyFor(m.screenStack(), action, m.keyOpts())
	if !ok {
		for _, b := range km.Bindings() {
			if b.Action == action {
				k = b.Key()
				break
			}
		}
	}
	return keys.Names{}.Name(k)
}

// keyLabel is a label that carries a live value, which a Labels map
// cannot: the second press of a remove names the key as it is bound now.
func (m tuiModel) keyLabel(action string) (string, bool) {
	switch action {
	case "home.remove", "flow.remove":
		if m.removeStep != "" {
			return "remove — " + m.keyName(action) + " again", true
		}
	}
	return "", false
}

func (m tuiModel) keyEnabled(action string) bool {
	switch action {
	case "message.task":
		return m.detail == detailMessage
	case "task.follow":
		return m.detail == detailSession
	case "stepout.open", "stepout.copy", "stepout.enter", "stepout.edit":
		outs := m.stepOutputs()
		sel := m.stepOutputSel(outs)
		if sel >= len(outs) {
			return false
		}
		return action != "stepout.edit" || outs[sel].kind == outFile
	}
	return true
}

func (m tuiModel) keyVisible(action string) bool {
	switch action {
	case "run.open":
		return !(m.focus == focusRadio && m.briefTab)
	case "run.follow", "pane.4":
		return m.focus == focusRadio
	case "run.lanes", "run.narrower", "run.wider":
		return m.focus == focusTasks
	case "message.task":
		return m.detail == detailMessage && m.selectedMessageTask() != ""
	case "flow.steer", "flow.compose", "flow.read", "flow.open", "flow.outputs", "flow.result",
		"flow.notes", "flow.export", "flow.remove", "flow.close":
		return !m.flowDetail
	case "flow.steps", "flow.pagedown":
		return m.flowDetail
	case "outputs.open", "outputs.copy", "outputs.edit", "outputs.enter":
		o := m.selectedOutput()
		if o == nil {
			return false
		}
		switch action {
		case "outputs.edit":
			return o.kind == outFile
		case "outputs.enter":
			return o.kind == outAnswer || o.kind == outReport
		}
	}
	return true
}

func (m tuiModel) keyState(action string) string {
	switch action {
	case "run.open":
		return map[focusPane]string{focusTasks: "graph", focusNode: "node", focusTimeline: "timeline", focusRadio: "radio"}[m.focus]
	case "run.steer":
		if m.focus == focusTimeline {
			return "timeline"
		}
	case "run.zoom":
		if m.zoom >= 0 {
			return "zoomed"
		}
	case "run.lanes":
		if m.boxMode {
			return "boxes"
		}
		return "lanes"
	case "pane.4":
		if m.briefTab {
			return "brief"
		}
		return "radio"
	case "flow.zoom":
		if m.flowZoom {
			return "zoomed"
		}
	case "flow.back":
		switch {
		case m.flowZoom:
			return "zoomed"
		case m.flowDetail:
			return "detail"
		}
	case "flow.close":
		if f, ok := m.flows[m.flowID]; ok && f.Closed {
			return "closed"
		}
	case "outputs.open", "outputs.copy", "outputs.enter":
		if o := m.selectedOutput(); o != nil {
			switch o.kind {
			case outFile:
				return "file"
			case outPR:
				return "pr"
			case outCommit:
				return "commit"
			case outBranch:
				return "branch"
			case outAnswer, outReport:
				return "text"
			}
		}
	case "steer.send":
		if !m.currentSteerTarget().live() {
			return "resume"
		}
	}
	return ""
}

// selectedOutput is the Outputs view's selection, or nil.
func (m tuiModel) selectedOutput() *output {
	outs := m.outputs()
	if m.outputSel < len(outs) {
		return &outs[m.outputSel]
	}
	return nil
}

// selectedMessageTask is the task the open message leads to, or "".
func (m tuiModel) selectedMessageTask() string {
	if m.radioCur < 0 || m.radioCur >= len(m.snap.Messages) {
		return ""
	}
	return m.messageTask(m.snap.Messages[m.radioCur])
}

// footerStyle draws hints the way the hand-written footers did: plain text
// joined by " · ", dimmed as one line by the caller so a hint like "q quit"
// stays one run of text.
var footerStyle = keys.FooterStyle{Separator: " · "}

// keyFooter is the hint line for stack at width w, dimmed.
func (m tuiModel) keyFooter(stack []keys.Ctx, w int) string {
	return styDim.Render(m.keymap().Footer(stack, max(1, w), m.keyOpts(), footerStyle))
}

// helpStyle is the overlay's look in the monitor's palette.
var helpStyle = keys.HelpStyle{
	Title: lipgloss.NewStyle().Bold(true),
	Key:   lipgloss.NewStyle().Foreground(lipgloss.Color("6")).Bold(true),
	Muted: lipgloss.NewStyle().Faint(true),
}
