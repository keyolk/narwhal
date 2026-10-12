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

	"github.com/keyolk/narwhal/internal/broker"
)

// openFromFocus opens whatever the focused pane's cursor is on.
func (m *tuiModel) openFromFocus() {
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
		events := m.timelineEvents()
		i, ok := m.selectedEvent(events)
		if !ok {
			return
		}
		e := events[i]
		switch {
		case e.Seq > 0:
			m.openMessage(e.Seq)
		case m.taskByID(e.Who).ID != "":
			m.openTask(e.Who, e.At)
		}
	}
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
	if !at.IsZero() {
		// Two lines of what came before, so the moment is read in context.
		m.detailScroll = max(0, m.feedLinesBefore(id, at, m.width-2)-2)
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
	body := wrapText(msg.Content, width)

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

	keys := "j/k scroll · n/p message · esc back"
	if id := m.messageTask(msg); id != "" {
		keys = "enter " + id + " at this moment · " + keys
	}
	footer := styDim.Render(keys)

	avail := max(3, m.height-4)
	scroll, end, pos := scrollWindow(m.detailScroll, avail, len(body))
	return pinFooter(head+pos+"\n"+strings.Join(meta, styDim.Render("  "))+"\n\n"+
		strings.Join(body[scroll:end], "\n"), footer, m.height)
}

// viewTaskView is a task's whole story in one place: what it is and how it
// stands, then the assignment, the operator's instructions, the judge's
// calls and the worker's reasoning, calls and results in the order they
// happened, and the final answer last.
//
// It replaces two screens. The task detail showed the assignment and a
// dozen lines of activity, the session view the activity and not the
// task, and s flipped between them; the question either answered was half
// of what you opened it for.
func (m tuiModel) viewTaskView() string {
	t, ok := m.selectedTask()
	if !ok {
		return "no tasks"
	}
	width := m.width - 2

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
	top := []string{head, styDim.Render(strings.Join(meta, " · "))}
	if c := strings.TrimSpace(t.Check); c != "" {
		line := styDim.Render("check ") + truncate(oneLine(c), width-6)
		if r := strings.TrimSpace(t.CheckResult); r != "" {
			line += "\n" + styDim.Render("  ↳ ") + truncate(oneLine(r), width-4)
		}
		top = append(top, line)
	}

	body := m.sessionActivityLines(t.ID, width)
	if len(body) == 0 && strings.TrimSpace(t.Assignment) != "" {
		// No transcript yet: the assignment is still worth reading.
		var st feedState
		body = renderEntry(transcriptEntry{kind: "text", user: true, text: t.Assignment}, width, &st)
	}

	// The final answer only exists once the worker has exited. Appended
	// rather than shown instead: it is the end of the same story.
	//
	// Two sources say it, and they outlive each other. claude-output.txt is
	// this machine's stdout capture — richer, but gone if the session
	// directory was cleaned or the run was adopted from elsewhere. The
	// snapshot outcome is what the worker declared at task-done and
	// survives persistence, so it is the one still there after a restart.
	final := m.workerOutputLines(t.ID)
	if len(final) == 0 && t.Outcome != "" {
		final = strings.Split(t.Outcome, "\n")
	}
	if len(final) > 0 {
		label := "── final answer ──"
		lstyle := styGreen
		if t.State == broker.TaskFailed {
			label, lstyle = "── why it failed ──", styRedBold
		}
		body = append(body, "", lstyle.Render(label))
		for _, l := range final {
			body = append(body, wrapText(l, width)...)
		}
	}
	if len(body) == 0 {
		// Say which is true rather than leaving a blank pane.
		var reason string
		switch {
		case t.Dispatches == 0:
			reason = "not dispatched yet — nothing to show"
		case t.State == broker.TaskDispatched:
			reason = "the worker has started but has not written its first event yet"
		default:
			reason = "no transcript found. This run predates session pinning, so " +
				"the worker's session cannot be located; only its final output " +
				"would be available, at " + m.sessionLogPath(t.ID)
		}
		body = wrapText(reason, width)
	}

	header := strings.Join(top, "\n")
	avail := max(1, m.height-strings.Count(header, "\n")-4)
	// Following pins the view to the end as the worker works.
	if m.sessionTail && len(body) > avail {
		m.detailScroll = len(body) - avail
	}
	scroll, end, pos := scrollWindow(m.detailScroll, avail, len(body))
	follow := ""
	if m.sessionTail {
		follow = styCyan.Render("  [following]")
	}
	footer := styDim.Render("j/k scroll · g/G top/end · f follow · n/p task · a attach · esc back")
	return pinFooter(top[0]+pos+follow+"\n"+strings.Join(top[1:], "\n")+"\n\n"+
		strings.Join(body[scroll:end], "\n"), footer, m.height)
}
