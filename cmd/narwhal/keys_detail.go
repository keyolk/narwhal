// keys_detail.go runs the detail views' actions: a message, a task in its
// three tabs, and the run's result. Scrolling and back are shared (the
// detail context); each view adds its own keys in an inner context.
package main

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/keyolk/narwhal/internal/broker"
	"github.com/keyolk/narwhal/internal/store"
)

func (m tuiModel) doDetail(action string) (tea.Model, tea.Cmd) {
	switch action {
	case "detail.back":
		if m.pinnedTask != "" {
			// A pane opened by w shows one task; leaving it closes the pane.
			m.quit = true
			return m, tea.Quit
		}
		m.detail = detailClosed
	case "detail.down":
		m.detailScroll++
		m.sessionTail = false
	case "detail.up":
		if m.detailScroll > 0 {
			m.detailScroll--
		}
		m.sessionTail = false
	case "detail.pagedown":
		m.detailScroll += 10
		m.sessionTail = false
	case "detail.pageup":
		m.detailScroll = max(0, m.detailScroll-10)
		m.sessionTail = false
	case "detail.top":
		m.detailScroll = 0
		m.sessionTail = false
	case "detail.bottom":
		// To the newest, and stay there as it grows.
		m.sessionTail = true
		m.detailScroll = 1 << 30
	case "message.task":
		// From a message, to the task it came from or went to, at the
		// moment it was posted.
		m.openMessageTask()
	case "detail.attach":
		// Attach from the detail view too: this is where you find out the
		// captured log is empty, and where wanting the live session next
		// is most likely.
		return m.attachSelected()
	case "detail.next", "detail.prev":
		// Walk to the next item without leaving the detail view.
		d := 1
		if action == "detail.prev" {
			d = -1
		}
		switch m.detail {
		case detailMessage:
			if i := m.radioCur + d; i >= 0 && i < len(m.snap.Messages) {
				m.radioCur = i
				m.detailScroll = 0
				m.followTail = false
			}
		case detailSession:
			if i := m.taskCur + d; i >= 0 && i < len(m.snap.Tasks) {
				m.taskCur = i
				m.detailScroll = 0
				m.sessionTail = true
				m.resetTaskView()
			}
		}
	}
	return m, nil
}

// doResult runs the result view's own keys: write the report, copy the
// answer, open an artifact, plan the next step.
func (m tuiModel) doResult(action string, n int) (tea.Model, tea.Cmd) {
	switch action {
	case "result.report":
		snap := m.snap
		return m, func() tea.Msg {
			path, err := store.ExportRun(store.DefaultExportDir(), snap, true)
			return noticeMsg{text: "report written to " + path, err: err}
		}
	case "result.copy":
		res := m.snap.Result()
		if res.Answer == "" {
			m.setNotice("", fmt.Errorf("nothing to copy yet"))
			return m, nil
		}
		return m, copyCmd(res.Answer, "the answer")
	case "result.artifact":
		return m, openArtifact(m.snap.Result().Artifacts, n, m.snap.CWD)
	case "result.next":
		m.startCompose(m.flowOf(m.live))
	case "result.outputs":
		cmd := m.openOutputs("")
		return m, cmd
	}
	return m, nil
}

// doTask runs the task view's keys. Summary and Messages are read, not
// navigated (task.read); Activity moves entry by entry (task.activity).
func (m tuiModel) doTask(action string, n int) (tea.Model, tea.Cmd) {
	t, ok := m.selectedTask()
	switch action {
	case "task.steer":
		// An instruction for this task: read on the radio while it runs,
		// or reopening it when it has finished. See steer.go.
		if ok {
			m.startSteer(t.ID)
		}
		return m, nil
	case "task.outputs":
		// This run's outputs, this task's first. See outputs.go.
		if ok {
			cmd := m.openOutputs(t.ID)
			return m, cmd
		}
		return m, nil
	case "task.nexttab":
		m.switchTaskTab(1)
		return m, nil
	case "task.prevtab":
		m.switchTaskTab(-1)
		return m, nil
	case "task.follow":
		m.sessionTail = true
		return m, nil
	}
	if !ok {
		return m.doDetail(action)
	}
	switch action {
	case "read.down":
		m.detailScroll++
	case "read.up":
		m.detailScroll = max(0, m.detailScroll-1)
	case "read.pagedown":
		m.detailScroll += m.height / 2
	case "read.pageup":
		m.detailScroll = max(0, m.detailScroll-m.height/2)
	case "read.top":
		m.detailScroll = 0
	case "read.bottom":
		m.detailScroll = 1 << 20
	case "read.request":
		m.summaryAskOpen = !m.summaryAskOpen
	case "read.artifact":
		res := broker.Snapshot{Tasks: []broker.TaskSnapshot{t}}.Result()
		return m, openArtifact(res.Artifacts, n, m.snap.CWD)
	case "act.down":
		m.moveEntry(1)
	case "act.up":
		m.moveEntry(-1)
	case "act.toggle":
		m.toggleEntry()
	case "act.all":
		// Open or fold everything, and start over from what is shown.
		m.taskOpenAll, m.taskToggled = !m.taskOpenAll, nil
		m.moveEntry(0)
	case "act.pager":
		return m, m.pageEntry()
	case "act.pagedown":
		m.scrollTask(m.height / 2)
	case "act.pageup":
		m.scrollTask(-m.height / 2)
	case "act.top":
		m.taskSel, m.detailScroll, m.sessionTail = 0, 0, false
	case "act.follow":
		// To the newest, and stay there as it grows.
		m.taskSel, m.sessionTail = -1, true
	default:
		return m.doDetail(action)
	}
	return m, nil
}
