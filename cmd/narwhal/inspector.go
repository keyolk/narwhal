// inspector.go renders the node inspector: a compact summary of whichever
// task the graph cursor is on.
//
// It exists because moving the cursor in the graph used to change nothing
// else on screen. Reading a node meant opening a detail view and backing
// out of it, which is heavy for what is usually a one-glance question —
// what is this task waiting on, what model is it running, what has it done
// lately. The inspector answers those while you navigate.
//
// It is deliberately a summary. Assignments run to a couple of kilobytes in
// real runs, so the full text stays behind `enter`, and the whole session
// transcript stays behind `s`.
package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/keyolk/narwhal/internal/broker"
)

func (m tuiModel) viewInspector(width, height int) string {
	title := numberedPaneTitle(2, "Node", m.focus == focusNode, width)

	t, ok := m.selectedTask()
	if !ok {
		return lipgloss.NewStyle().Width(width).Render(
			title + "\n" + styDim.Render("(no tasks yet)"))
	}

	rows := []string{title}
	rows = append(rows, m.inspectorHeadline(t, width))
	rows = append(rows, m.inspectorFields(t, width)...)

	// Whatever is left goes to the activity feed, which is the part that
	// changes while you watch and the reason to give this pane room.
	if used := len(rows); used < height {
		rows = append(rows, m.inspectorActivity(t, width, height-used)...)
	}
	if len(rows) > height {
		rows = rows[:height]
	}
	// Pad to the full height. The field rows only appear when they have
	// something to say, so without this the pane's height tracks the
	// selected node and the radio title below it slides up and down as the
	// cursor moves — the one thing a fixed reference point must not do.
	for len(rows) < height {
		rows = append(rows, "")
	}
	return lipgloss.NewStyle().Width(width).Render(strings.Join(rows, "\n"))
}

// inspectorHeadline is the task id and state, plus the first line of the
// assignment — enough to recognize which task this is without opening it.
func (m tuiModel) inspectorHeadline(t broker.TaskSnapshot, width int) string {
	icon, style := taskIconStyle(t.State)
	name := t.ID
	if t.Name != "" && t.Name != t.ID {
		name += styDim.Render(" (" + t.Name + ")")
	}
	return fmt.Sprintf("%s %s  %s", style.Render(icon), styTitle.Render(name),
		style.Bold(true).Render(string(t.State)))
}

// inspectorFields renders the one-line facts: model, what it waits on, what
// it blocks, retries, and any files it holds.
//
// Rows are only emitted when they say something. A fixed grid of mostly
// empty labels is harder to scan than three populated lines, and the pane
// is short.
func (m tuiModel) inspectorFields(t broker.TaskSnapshot, width int) []string {
	var out []string
	add := func(icon, label, value string, style lipgloss.Style) {
		if value == "" {
			return
		}
		// The icon takes the value's colour and the word stays dim, so a
		// row reads as one coloured unit with a quiet label rather than
		// three shades competing across it.
		prefix := fmt.Sprintf(" %s %s ", icon, label)
		out = append(out, style.Render(" "+icon)+
			styDim.Render(" "+label+" ")+
			style.Render(truncate(value, width-displayWidth(prefix))))
	}

	model := t.Model
	if model == "" {
		model = "default"
	}
	// Model is structural: which tier this task was handed. It matters
	// most when it is not the default, which is exactly when a dim value
	// hid it.
	modelStyle := styMagenta
	if t.Model == "" {
		modelStyle = styDim
	}
	add(icons.fieldModel, "model", model, modelStyle)

	if len(t.Deps) > 0 {
		// Say which deps are still outstanding, not just what the edges
		// are: "waits on api" is the answer when a task is sitting idle,
		// and the graph already shows the edges themselves.
		pending := m.pendingDeps(t)
		value := strings.Join(t.Deps, ", ")
		style := styGreen
		if len(pending) > 0 {
			value = strings.Join(pending, ", ") + styDim.Render(
				fmt.Sprintf("  (%d/%d done)", len(t.Deps)-len(pending), len(t.Deps)))
			style = styYellow
		}
		add(icons.fieldDeps, "waits", value, style)
	}

	if blocks := m.blockedBy(t.ID); len(blocks) > 0 {
		// What a task holds up is the reason to care that it is stuck.
		add(icons.fieldBlocks, "blocks", strings.Join(blocks, ", "), styBlue)
	}

	if t.Dispatches > 1 {
		// One dispatch is the normal case and not worth a line; more than
		// one means it was retried, which is worth seeing.
		// The last attempt before the circuit breaker fails the task is a
		// different situation from the second of three.
		tryStyle := styYellow
		if t.Dispatches >= broker.MaxDispatchFailures {
			tryStyle = styRedBold
		}
		add(icons.fieldDispatch, "tries",
			fmt.Sprintf("%d of %d", t.Dispatches, broker.MaxDispatchFailures), tryStyle)
	}

	if files := m.filesHeldBy(t.ID); len(files) > 0 {
		// A held path is a claim against every peer, so it reads as
		// structure rather than as a note.
		add(icons.fieldFiles, "holds", strings.Join(files, ", "), styMagenta)
	}

	return out
}

// inspectorActivity renders what the worker is doing, as much of it as the
// pane has room for.
//
// This is the pane's substance. It used to be a three-line tail labelled
// "recent", which is enough to see that a worker is alive and never enough
// to see what it is doing — the whole reason for opening the monitor. The
// pane now scrolls through the entire transcript, so the same view answers
// both "is it moving" and "what did it find" without leaving for a
// full-screen detail and losing your place.
func (m tuiModel) inspectorActivity(t broker.TaskSnapshot, width, budget int) []string {
	if budget < 2 {
		return nil
	}

	total := m.nodeLineCountAt(t.ID, width-1)
	if total == 0 {
		if t.Dispatches == 0 {
			return nil
		}
		return []string{m.activityLabel(0, 0, 0), styDim.Render("   (no output yet)")}
	}

	avail := budget - 1
	start, end := activityWindow(m.nodeScroll, avail, total)

	// Render only the window. The whole feed runs to hundreds of lines on
	// a long task — 929 for one worker on disk here — and styling all of
	// them to show six is work thrown away on every frame.
	out := []string{m.activityLabel(start, end, total)}
	for _, l := range m.nodeActivitySlice(t.ID, width-1, start, end) {
		out = append(out, " "+l)
	}
	return out
}

// activityWindow resolves a scroll offset to the visible range, treating
// nodeScrollTail and any offset past the end as "show the newest".
func activityWindow(scroll, avail, total int) (int, int) {
	start := scroll
	if start == nodeScrollTail || start > total-avail {
		start = total - avail
	}
	if start < 0 {
		start = 0
	}
	end := start + avail
	if end > total {
		end = total
	}
	return start, end
}

// activityLabel says how much of the activity is on screen, so a pane
// showing a window into 200 lines does not look like the whole story.
func (m tuiModel) activityLabel(start, end, total int) string {
	label := fmt.Sprintf(" %s activity", icons.fieldActivity)
	if total == 0 {
		return styDim.Render(label)
	}
	pos := fmt.Sprintf("  %d-%d/%d", start+1, end, total)
	if m.nodeScroll == nodeScrollTail {
		pos += " ⌄"
	}
	return styDim.Render(label + pos)
}

// nodeActivityLines is the worker's full activity, rendered to width.
//
// Kept for callers that genuinely need every line. The pane itself uses
// nodeActivitySlice, because rendering hundreds of lines to display six is
// the difference between a 2.4ms frame and a 0.2ms one.
func (m tuiModel) nodeActivityLines(taskID string, width int) []string {
	if sid := m.workerSessionID(taskID); sid != "" {
		if lines := globalTranscripts.render(
			transcriptPath(m.live.CWD, sid), width); len(lines) > 0 {
			return lines
		}
	}
	return m.workerOutputLines(taskID)
}

// nodeActivitySlice renders just the lines in [start, end).
//
// A transcript entry can wrap to several lines, so the entry index and the
// line index are not the same number and the slice cannot be taken before
// rendering. Rendering is done per entry from the end backwards until the
// window is covered, which is bounded by the window rather than the feed.
func (m tuiModel) nodeActivitySlice(taskID string, width, start, end int) []string {
	return clampSlice(m.nodeActivityLines(taskID, width), start, end)
}

func clampSlice(lines []string, start, end int) []string {
	if start < 0 {
		start = 0
	}
	if end > len(lines) {
		end = len(lines)
	}
	if start >= end {
		return nil
	}
	return lines[start:end]
}

// nodeLineCount is how many activity lines the selected node has, for
// clamping the scroll offset.
func (m tuiModel) nodeLineCount() int {
	t, ok := m.selectedTask()
	if !ok {
		return 0
	}
	return m.nodeLineCountAt(t.ID, m.nodePaneWidth())
}

// nodeLineCountAt is the line count at a given width.
func (m tuiModel) nodeLineCountAt(taskID string, width int) int {
	return len(m.nodeActivityLines(taskID, width))
}

// nodePaneWidth is the width the node pane renders at. Scrolling has to
// agree with rendering about how many lines there are, and the count
// depends on where the text wraps.
func (m tuiModel) nodePaneWidth() int {
	if m.zoom == focusNode {
		return m.width - 1
	}
	w := m.width - m.graphPaneWidth() - 1
	if w < 20 {
		w = 20
	}
	return w - 1
}

// pendingDeps returns the task's dependencies that have not finished, in
// the order they were declared.
func (m tuiModel) pendingDeps(t broker.TaskSnapshot) []string {
	state := make(map[string]broker.TaskState, len(m.snap.Tasks))
	for _, other := range m.snap.Tasks {
		state[other.ID] = other.State
	}
	var pending []string
	for _, d := range t.Deps {
		switch state[d] {
		case broker.TaskCompleted, broker.TaskFailed:
		default:
			pending = append(pending, d)
		}
	}
	return pending
}

// blockedBy returns the tasks that depend on this one. The snapshot only
// carries outgoing edges, so the reverse direction is derived.
func (m tuiModel) blockedBy(id string) []string {
	var out []string
	for _, other := range m.snap.Tasks {
		for _, d := range other.Deps {
			if d == id {
				out = append(out, other.ID)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}

// filesHeldBy reads the file claims a task currently holds off the radio.
//
// Claims live in the coordinator, which the monitor cannot reach — it only
// polls snapshots — but every claim and release is announced on the radio,
// so the current set can be replayed from the message log.
func (m tuiModel) filesHeldBy(taskID string) []string {
	held := map[string]bool{}
	for _, msg := range m.snap.Messages {
		action, owner, paths, ok := broker.ParseFileClaimRequest(msg.Content)
		if !ok || owner != taskID {
			continue
		}
		for _, p := range paths {
			if action == broker.FileClaimPrefix {
				held[p] = true
			} else {
				delete(held, p)
			}
		}
	}
	out := make([]string, 0, len(held))
	for p := range held {
		out = append(out, shortenPath(p))
	}
	sort.Strings(out)
	return out
}
