// timeline.go draws pane 4, the run's history under the graph: what
// started and finished, what the operator asked and where it went, and the
// judge's calls that changed a task's course.
//
// Brief is the run as it stands; a decision drops off it once dealt with.
// This is the same run told in order, so "what happened while I was away"
// has an answer on screen. It is a pane like the others: `4` focuses it,
// j/k scroll it, `z` zooms it, and `narwhal export` writes the whole of it.
package main

import (
	"fmt"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/keyolk/narwhal/internal/broker"
)

// minTimeline is the fewest rows worth giving pane 4 under the graph: a
// title and a handful of events. Below it the graph keeps its column.
const minTimeline = 6

// viewLeft is the graph, with pane 4 beneath it when the graph leaves room.
//
// A graph is a handful of boxes; on a normal terminal it filled a tenth of
// its column and padded the rest, while the run's history sat behind a key
// nobody thinks to press. Giving the history the space the graph does not
// use puts "what has happened" next to "what the run looks like", always.
func (m tuiModel) viewLeft(width, height int) string {
	if !m.timelineFits(height) {
		return m.viewTasks(width, height)
	}
	graph := m.graphContentHeight(width)
	return lipgloss.JoinVertical(lipgloss.Left,
		m.viewTasks(width, graph),
		"",
		m.renderTimeline(m.timelineEvents(), width, height-graph-1))
}

// timelineFits reports whether pane 4 has room under the graph in a body
// of this height. Where it does not, focusing it shows it zoomed.
func (m tuiModel) timelineFits(bodyHeight int) bool {
	return m.boxMode && m.zoom < 0 &&
		m.graphContentHeight(m.graphPaneWidth())+1+minTimeline <= bodyHeight
}

// graphContentHeight is the rows the box graph needs: its title, its
// drawing, and one row of air.
func (m tuiModel) graphContentHeight(width int) int {
	return 1 + len(m.boxRows(width)) + 1
}

// timelineEvents is what pane 4 lists where it is drawn now.
//
// Under the graph, beside the radio, it leaves out the radio's own lines —
// instructions, relays, urgent posts — which are one glance to the right,
// and keeps what only the task states and verdicts record. Beside the
// brief nothing else on screen shows those lines, and zoomed it is the
// only pane, so there it lists everything.
func (m tuiModel) timelineEvents() []broker.TimelineEvent {
	all := broker.Timeline(m.snap)
	if m.briefTab || m.timelineZoomed() {
		return all
	}
	var own []broker.TimelineEvent
	for _, e := range all {
		if !e.Kind.FromRadio() {
			own = append(own, e)
		}
	}
	return own
}

// timelineZoomed reports whether pane 4 is drawn across the body.
func (m tuiModel) timelineZoomed() bool {
	if m.zoom == focusTimeline {
		return true
	}
	return m.zoom < 0 && m.focus == focusTimeline && !m.timelineFits(m.bodyHeight())
}

// timelineRows is how many events pane 4 shows at once where it is drawn.
func (m tuiModel) timelineRows() int {
	body := m.bodyHeight()
	if m.timelineZoomed() {
		return body - 1
	}
	return body - m.graphContentHeight(m.graphPaneWidth()) - 1 - 1
}

// timelineMaxBack is the furthest pane 4 can scroll back: to where its
// oldest event is the top row.
func (m tuiModel) timelineMaxBack() int {
	return max(0, len(m.timelineEvents())-max(1, m.timelineRows()))
}

// viewTimeline is pane 4 zoomed across the body.
func (m tuiModel) viewTimeline(width, height int) string {
	return m.renderTimeline(broker.Timeline(m.snap), width, height)
}

func (m tuiModel) renderTimeline(events []broker.TimelineEvent, width, height int) string {
	total := len(events)
	label := fmt.Sprintf("Timeline (%d)", total)
	if !m.briefTab && !m.timelineZoomed() {
		// Says why the operator's instructions are not listed here.
		label = fmt.Sprintf("Timeline · tasks & judge (%d)", total)
	}
	avail := max(1, height-1)
	back := min(max(m.timelineBack, 0), max(0, total-avail))
	end := total - back
	start := max(0, end-avail)
	if total > avail {
		label += fmt.Sprintf("  %d-%d", start+1, end)
		if back == 0 {
			label += " ⌄"
		}
	}
	rows := []string{numberedPaneTitle(4, label, m.focus == focusTimeline, width)}
	if total == 0 {
		rows = append(rows, styDim.Render("   nothing yet"))
		return padRows(rows, width, height)
	}
	events = events[start:end]
	// The glyph says what kind of event it is, so the kind is not spelled
	// out: under the graph the pane is a third of the screen wide, and the
	// word "instruction" cost the line most of what the operator wrote.
	whoW := 0
	for _, e := range events {
		whoW = max(whoW, displayWidth(e.Who))
	}
	whoW = min(whoW, 10)
	for _, e := range events {
		glyph, style := timelineGlyph(e.Kind)
		text := e.Text
		if text == "" {
			text = string(e.Kind)
		} else if e.Kind == broker.TimelineStarted || e.Kind == broker.TimelineVerdict {
			text = string(e.Kind) + " " + text
		}
		line := styDim.Render(clock(e.At)) + " " +
			style.Render(glyph) + " " + styCyan.Render(padRight(truncate(e.Who, whoW), whoW)) + " " +
			style.Render(text)
		// Styled before it is fitted, so the cut has to skip escapes.
		rows = append(rows, ansi.Truncate(line, width, "…"))
	}
	return padRows(rows, width, height)
}

// timelineGlyph gives each kind the weight it carries on the graph: done
// green, failed and urgent red, the operator yellow, the judge magenta.
func timelineGlyph(k broker.TimelineKind) (string, lipgloss.Style) {
	switch k {
	case broker.TimelineRun:
		return "◆", styTitle
	case broker.TimelineStarted:
		return "▸", styBlue
	case broker.TimelineCompleted:
		return "✓", styGreen
	case broker.TimelineFailed:
		return "✗", styRedBold
	case broker.TimelineUrgent:
		return "!", styRedBold
	case broker.TimelineInstruction:
		return "»", styYellow
	case broker.TimelineRelay:
		return "→", styYellow
	default: // verdict, asked
		return icons.fieldJudge, styMagenta
	}
}
