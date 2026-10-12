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

// timelineEvents is what pane 3 lists: everything, the radio's own lines —
// instructions, relays, urgent posts — included.
//
// It used to leave those out while it sat beside the radio, which showed
// them one glance away. The two are tabs of one column now and never on
// screen together, so a timeline without them would be the run's history
// with the operator's part missing.
func (m tuiModel) timelineEvents() []broker.TimelineEvent {
	return broker.Timeline(m.snap)
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
	case l.zoom >= 0 || m.readerPane() != focusTimeline:
		return 1
	}
	return l.top
}

// timelineFollow selects the newest event and keeps it selected as more
// arrive; any other value of timelineSel is a fixed event.
const timelineFollow = -1

// selectedEvent resolves the selection against the events on screen.
func (m tuiModel) selectedEvent(events []broker.TimelineEvent) (int, bool) {
	if len(events) == 0 {
		return 0, false
	}
	if m.timelineSel == timelineFollow || m.timelineSel >= len(events) {
		return len(events) - 1, true
	}
	return max(m.timelineSel, 0), true
}

// moveTimeline steps the selection; stepping past the newest follows again.
func (m *tuiModel) moveTimeline(delta int) {
	events := m.timelineEvents()
	cur, ok := m.selectedEvent(events)
	if !ok {
		return
	}
	cur += delta
	if cur >= len(events)-1 {
		m.timelineSel = timelineFollow
		return
	}
	m.timelineSel = max(cur, 0)
}

// viewTimeline is pane 3 zoomed across the body.
func (m tuiModel) viewTimeline(width, height int) string {
	return m.renderTimeline(m.timelineEvents(), width, height)
}

func (m tuiModel) renderTimeline(events []broker.TimelineEvent, width, height int) string {
	total := len(events)
	label := fmt.Sprintf("Timeline (%d)", total)
	if !m.briefTab && !m.timelineZoomed() {
		// Says why the operator's instructions are not listed here.
		label = fmt.Sprintf("Timeline · tasks & judge (%d)", total)
	}
	avail := max(1, height-1)
	sel, _ := m.selectedEvent(events)
	// Following, the newest sit at the bottom; a fixed selection is kept
	// in the middle of the window, so there is context either side of it.
	start := max(0, total-avail)
	if m.timelineSel != timelineFollow {
		start = min(max(0, sel-avail/2), max(0, total-avail))
	}
	end := min(total, start+avail)
	if total > avail {
		label += fmt.Sprintf("  %d-%d", start+1, end)
	}
	if m.timelineSel == timelineFollow {
		label += " ⌄"
	}
	rows := []string{numberedPaneTitle(3, label, m.focus == focusTimeline, width)}
	if total == 0 {
		rows = append(rows, styDim.Render("   nothing yet"))
		return padRows(rows, width, height)
	}
	focused := m.focus == focusTimeline
	window := events[start:end]
	// The glyph says what kind of event it is, so the kind is not spelled
	// out: under the graph the pane is a third of the screen wide, and the
	// word "instruction" cost the line most of what the operator wrote.
	whoW := 0
	for _, e := range window {
		whoW = max(whoW, displayWidth(e.Who))
	}
	whoW = min(whoW, 10)
	for i, e := range window {
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
		if focused && start+i == sel {
			// The selection is what enter opens, so it reads as one bar.
			line = stySel.Render(padRight(ansi.Truncate(ansi.Strip(line), width, "…"), width))
		}
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
	case broker.TimelineFollows:
		return "↳", styCyan
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
