// navigation.go is how the monitor's views lead into each other.
//
// Every pane answered its own question and stopped there. The timeline said
// a task finished and could not show how; a radio message said a worker
// had found something and could not show what it was doing when it did; a
// task's summary and its session were two screens with a key between
// them. Following one thread of a run — I asked for this, who took it,
// what did they do — meant remembering times and finding them again by
// hand in another view.
//
// Now enter opens what the cursor is on, and every opened view leads on:
//
//	graph, node          → the task view: its whole story, newest last
//	timeline event       → the task view at that moment, or the message
//	radio message        → the message, and from it (enter or s) the
//	                       task that sent or received it, at that moment
//
// esc steps back to where you were.
package main

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/keyolk/narwhal/internal/broker"
)

// openFromFocus opens whatever the focused pane's cursor is on, and
// returns the poll an opened run needs.
func (m *tuiModel) openFromFocus() tea.Cmd {
	switch m.focus {
	case focusTasks, focusNode:
		if t, ok := m.selectedTask(); ok {
			m.openTask(t.ID, time.Time{})
		}
	case focusRadio:
		if !m.briefTab && len(m.snap.Messages) > 0 {
			m.detail = detailMessage
			m.detailScroll = 0
		}
	case focusTimeline:
		// A scene leads to its subject: a task's course to the task, an
		// exchange to its first message, the run's start to what it
		// continues.
		items := m.timelineItems()
		i, ok := m.selectedItem(len(items))
		if !ok {
			return nil
		}
		if items[i].reason != nil {
			m.openTask(items[i].task, time.Time{})
			return nil
		}
		sc := *items[i].scene
		switch sc.Kind {
		case broker.SceneTask:
			m.openTask(sc.Who[0], time.Time{})
			return nil
		case broker.SceneNow:
			return nil
		}
		for _, e := range sc.Events {
			switch {
			case e.Run != "":
				return m.openRun(e.Run)
			case e.Seq > 0:
				m.openMessage(e.Seq)
				return nil
			}
		}
	}
	return nil
}

// openTask shows a task's view. With at set, the view opens on that moment
// and stays there; without, it follows the newest.
func (m *tuiModel) openTask(id string, at time.Time) {
	for i, r := range m.graphRows() {
		if r.id == id {
			m.selectNode(i)
			break
		}
	}
	m.detail = detailSession
	m.detailScroll = 0
	m.sessionTail = at.IsZero()
	m.resetTaskView()
	if !at.IsZero() {
		// The cursor on the moment, with what came before above it.
		m.taskTab = taskTabActivity
		if t, ok := m.selectedTask(); ok {
			l := m.layoutTask(t, m.width-2)
			if i := m.entryAt(t, at); i >= 0 {
				m.taskSel = i
				m.detailScroll = max(0, l.starts[i]-2)
			}
		}
	}
}

// openMessage shows one radio message by its sequence number.
func (m *tuiModel) openMessage(seq int64) {
	for i, msg := range m.snap.Messages {
		if msg != nil && msg.Seq == seq {
			m.radioCur = i
			m.followTail = false
			m.detail = detailMessage
			m.detailScroll = 0
			return
		}
	}
}

// openMessageTask goes from the open message to its task.
func (m *tuiModel) openMessageTask() {
	if m.radioCur >= len(m.snap.Messages) {
		return
	}
	msg := m.snap.Messages[m.radioCur]
	if id := m.messageTask(msg); id != "" {
		m.openTask(id, msg.CreatedAt)
	}
}

// messageTask is the task a message belongs to: the worker that posted
// it, or for the operator's instruction and its relay, the first task it
// went to.
func (m tuiModel) messageTask(msg *broker.Message) string {
	if msg == nil {
		return ""
	}
	if id := strings.TrimPrefix(msg.Sender, "worker-"); id != msg.Sender && m.taskByID(id).ID != "" {
		return id
	}
	if to := m.instructionRecipients(msg); len(to) > 0 {
		return to[0]
	}
	return ""
}

// instructionRecipients is where an operator instruction went: the tasks
// it named, or failing that the ones the router relayed it to. A relay's
// own mentions answer the same question for the relay.
func (m tuiModel) instructionRecipients(msg *broker.Message) []string {
	tasks := make(map[string]bool, len(m.snap.Tasks))
	for _, t := range m.snap.Tasks {
		tasks[t.ID] = true
	}
	switch msg.Sender {
	case "main":
		if to := recipients(msg, "", tasks); len(to) > 0 {
			return to
		}
		return routedTo(m.snap, msg.Content)
	case "coordinator":
		return recipients(msg, "", tasks)
	}
	return nil
}

// viewMessageDetail shows one radio message with what it connects to: the
// task that posted it, or for an instruction, where it went and who has
// answered since.
func (m tuiModel) viewMessageDetail() string {
	if len(m.snap.Messages) == 0 {
		return "no messages"
	}
	msg := m.snap.Messages[m.radioCur]

	from := msg.Sender
	if msg.Sender == "main" {
		from = "you"
	}
	head := fmt.Sprintf("%s  %s  %s → %s",
		styCyanBold.Render("Message"), styDim.Render(clock(msg.CreatedAt)),
		styBlue.Render(from), mentionsLabel(msg.Mentions))

	// Only what is set: an empty thread or priority printed as "thread="
	// was a label with nothing after it.
	var meta []string
	if msg.ThreadID != "" {
		meta = append(meta, styDim.Render("thread ")+styMagenta.Render(msg.ThreadID))
	}
	if msg.Priority != "" && msg.Priority != broker.PriorityNormal {
		_, prio := priorityGlyph(msg.Priority)
		meta = append(meta, prio.Render(string(msg.Priority)))
	}
	meta = append(meta, styDim.Render(fmt.Sprintf("seq %d", msg.Seq)))

	width := m.width - 2
	body := renderMarkdown(msg.Content, width, lipgloss.NewStyle())

	if msg.Sender == "main" {
		// What the operator most wants to know about their own message.
		body = append(body, "")
		to := m.instructionRecipients(msg)
		switch {
		case len(to) == 0:
			body = append(body, styDim.Render("went to everyone"))
		default:
			body = append(body, styYellow.Render("went to → "+strings.Join(to, ", ")))
		}
		for _, in := range briefInstructions(m.snap) {
			if in.text != oneLine(msg.Content) {
				continue
			}
			if len(in.answeredBy) == 0 {
				body = append(body, styYellow.Render("… no reply yet"))
			} else {
				body = append(body, styGreen.Render("✓ answered by "+strings.Join(in.answeredBy, ", ")))
			}
			break
		}
	}

	footer := m.footerBlock(m.width, m.keyFooter(m.screenStack(), m.width))

	avail := max(3, m.height-4)
	scroll, end, pos := scrollWindow(m.detailScroll, avail, len(body))
	return pinFooter(head+pos+"\n"+strings.Join(meta, styDim.Render("  "))+"\n\n"+
		strings.Join(body[scroll:end], "\n"), footer, m.height)
}

// viewTaskView is a task in three tabs: Summary — what it was asked,
// where it stands and what it concluded; Activity — how it got there,
// entry by entry; Messages — what it said and was told on the radio.
//
// It was one feed, everything at once and in order, so the answer was at
// the bottom of hundreds of lines and the check scrolled off the top. The
// things read together are together on Summary; the long tail of tool
// calls is a tab away. A running task opens on Activity, a finished one
// on Summary.
func (m tuiModel) viewTaskView() string {
	t, ok := m.selectedTask()
	if !ok {
		return "no tasks"
	}
	width := m.width - 2
	tab := m.currentTaskTab(t)

	icon, style := taskIconStyle(t.State)
	head := fmt.Sprintf("%s  %s %s  %s",
		styCyanBold.Render("Task"), style.Render(icon),
		styTitle.Render(t.ID), style.Bold(true).Render(string(t.State)))

	var meta []string
	model := t.Model
	if model == "" {
		model = "default"
	}
	meta = append(meta, "model "+model)
	if t.Dispatches > 1 {
		meta = append(meta, fmt.Sprintf("try %d of %d", t.Dispatches, broker.MaxDispatchFailures))
	}
	if pending := m.pendingDeps(t); len(pending) > 0 {
		meta = append(meta, "waits on "+strings.Join(pending, ", "))
	}
	if blocks := m.blockedBy(t.ID); len(blocks) > 0 {
		meta = append(meta, "blocks "+strings.Join(blocks, ", "))
	}
	if !t.StartedAt.IsZero() {
		span := "started " + clock(t.StartedAt)
		if !t.EndedAt.IsZero() {
			span += " · took " + t.EndedAt.Sub(t.StartedAt).Round(time.Second).String()
		}
		meta = append(meta, span)
	}

	var body []string
	var l taskLayout
	switch tab {
	case taskTabActivity:
		l = m.layoutTask(t, width)
		body = l.lines
		if len(body) == 0 {
			body = wrapText(m.noActivityReason(t), width)
		}
	case taskTabMessages:
		body = m.taskMessageLines(t, width)
	default:
		body = m.taskSummaryLines(t, width)
	}
	tabs := m.taskTabsLine(t, tab)

	avail := max(1, m.height-7)
	scrollAt := m.detailScroll
	if tab == taskTabActivity && m.sessionTail && len(body) > avail {
		// Following pins the feed to the end as the worker works.
		scrollAt = len(body) - avail
	}
	scroll, end, pos := scrollWindow(scrollAt, avail, len(body))

	sel := -1
	if tab == taskTabActivity {
		sel = m.selectedEntry(len(l.entries))
	}
	shown := make([]string, 0, end-scroll)
	for n := scroll; n < end; n++ {
		gutter := " "
		if sel >= 0 && n >= l.starts[sel] && n < l.starts[sel+1] {
			// The entry under the cursor carries a bar down its left edge.
			gutter = styCyanBold.Render("▌")
		}
		shown = append(shown, gutter+body[n])
	}
	follow := ""
	if tab == taskTabActivity && m.sessionTail {
		follow = styCyan.Render("  [following]")
	}
	footer := m.footerBlock(m.width, m.keyFooter(m.screenStack(), m.width))
	return pinFooter(head+follow+"\n"+styDim.Render(strings.Join(meta, " · "))+"\n"+tabs+pos+"\n"+
		strings.Join(shown, "\n"), footer, m.height)
}
