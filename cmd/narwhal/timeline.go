// timeline.go shows the run's history in the radio pane's place: what
// started and finished, what the operator asked and where it went, and the
// judge's calls that changed a task's course.
//
// Brief is the run as it stands; a decision drops off it once dealt with.
// This is the same run told in order, so "what happened while I was away"
// has an answer on screen. `5` shows it. It keeps to the newest events that
// fit; `z` zooms for more, and `narwhal export` writes the whole of it.
package main

import (
	"fmt"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/keyolk/narwhal/internal/broker"
)

func (m tuiModel) viewTimeline(width, height int) string {
	events := broker.Timeline(m.snap)
	rows := []string{numberedPaneTitle(5, fmt.Sprintf("Timeline (%d)", len(events)),
		m.focus == focusRadio, width)}
	if len(events) == 0 {
		rows = append(rows, styDim.Render("   nothing yet"))
		return padRows(rows, width, height)
	}
	// Newest at the bottom, like the radio: the pane follows the run.
	if avail := height - 1; avail > 0 && len(events) > avail {
		events = events[len(events)-avail:]
	}
	for _, e := range events {
		glyph, style := timelineGlyph(e.Kind)
		who := ""
		if e.Who != "" {
			who = styCyan.Render(padRight(e.Who, 12)) + " "
		}
		line := styDim.Render(e.At.Local().Format("15:04:05")) + " " +
			style.Render(glyph) + " " + who + style.Render(string(e.Kind)) + " " + e.Text
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
