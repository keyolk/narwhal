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
	title := paneTitle("Node", m.focus == focusTasks, width)

	t, ok := m.selectedTask()
	if !ok {
		return lipgloss.NewStyle().Width(width).Render(
			title + "\n" + styDim.Render("(no tasks yet)"))
	}

	rows := []string{title}
	rows = append(rows, m.inspectorHeadline(t, width))
	rows = append(rows, m.inspectorFields(t, width)...)

	// Whatever is left goes to recent activity, which is the part that
	// changes while you watch.
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
	return fmt.Sprintf("%s %s  %s", style.Render(icon), name,
		style.Render(string(t.State)))
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
		prefix := fmt.Sprintf(" %s %s ", icon, label)
		out = append(out, styDim.Render(prefix)+
			style.Render(truncate(value, width-displayWidth(prefix))))
	}

	model := t.Model
	if model == "" {
		model = "default"
	}
	add(icons.fieldModel, "model", model, styDim)

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
		add(icons.fieldBlocks, "blocks", strings.Join(blocks, ", "), styDim)
	}

	if t.Dispatches > 1 {
		// One dispatch is the normal case and not worth a line; more than
		// one means it was retried, which is worth seeing.
		add(icons.fieldDispatch, "tries",
			fmt.Sprintf("%d of %d", t.Dispatches, broker.MaxDispatchFailures), styYellow)
	}

	if files := m.filesHeldBy(t.ID); len(files) > 0 {
		add(icons.fieldFiles, "holds", strings.Join(files, ", "), styDim)
	}

	return out
}

// inspectorActivity is the tail of what the worker has been doing, which is
// the only part of the pane that moves on its own.
func (m tuiModel) inspectorActivity(t broker.TaskSnapshot, width, budget int) []string {
	if budget < 2 {
		return nil
	}
	label := styDim.Render(fmt.Sprintf(" %s recent", icons.fieldActivity))

	lines := m.workerActivityTail(t.ID, width-1)
	if len(lines) == 0 {
		if t.Dispatches == 0 {
			return nil
		}
		return []string{label, styDim.Render("   (no output yet)")}
	}
	// Keep the newest, since the tail is what "recent" means.
	if len(lines) > budget-1 {
		lines = lines[len(lines)-(budget-1):]
	}
	out := []string{label}
	for _, l := range lines {
		out = append(out, " "+l)
	}
	return out
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
