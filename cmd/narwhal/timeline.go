// timeline.go draws pane 3, the run's history under the graph: what
// started and finished, what the operator asked and where it went, and the
// judge's calls that changed a task's course.
//
// Brief is the run as it stands; a decision drops off it once dealt with.
// This is the same run told in order, so "what happened while I was away"
// has an answer on screen. It is a pane like the others: `3` focuses it,
// j/k scroll it, `z` zooms it, and `narwhal export` writes the whole of it.
package main

import (
	"fmt"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/keyolk/narwhal/internal/broker"
)

// timelineEvents is what pane 3 lists where it is drawn now.
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

// timelineZoomed reports whether pane 3 is drawn across the body.
func (m tuiModel) timelineZoomed() bool {
	return m.effectiveZoom() == focusTimeline
}

// timelineRows is how many events pane 3 shows at once where it is drawn.
func (m tuiModel) timelineRows() int {
	l := m.layout()
	switch {
	case l.zoom == focusTimeline:
		return l.top - 1
	case l.zoom >= 0 || l.single:
		return 1
	}
	return l.bottom
}

// timelineMaxBack is the furthest pane 3 can scroll back: to where its
// oldest event is the top row.
func (m tuiModel) timelineMaxBack() int {
	return max(0, len(m.timelineEvents())-max(1, m.timelineRows()))
}

// viewTimeline is pane 3 zoomed across the body.
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
	rows := []string{numberedPaneTitle(3, label, m.focus == focusTimeline, width)}
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
