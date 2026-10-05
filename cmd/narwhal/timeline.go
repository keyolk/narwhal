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

// viewLeft is the graph, with the timeline beneath it when the graph
// leaves room.
//
// A graph is a handful of boxes; on a normal terminal it filled a tenth of
// its column and padded the rest, while the run's history sat behind a key
// nobody thinks to press. Giving the history the space the graph does not
// use puts "what has happened" next to "what the run looks like", always.
func (m tuiModel) viewLeft(width, height int) string {
	graph := m.graphContentHeight(width)
	// The timeline needs a title and a few events to be worth drawing;
	// below that the graph keeps its whole column, and `5` still shows the
	// timeline in the radio's place.
	const minTimeline = 6
	if !m.boxMode || graph+1+minTimeline > height || m.lower == lowerTimeline {
		return m.viewTasks(width, height)
	}
	return lipgloss.JoinVertical(lipgloss.Left,
		m.viewTasks(width, graph),
		"",
		m.viewTimeline(width, height-graph-1))
}

// graphContentHeight is the rows the box graph needs: its title, its
// drawing, and one row of air.
func (m tuiModel) graphContentHeight(width int) int {
	return 1 + len(m.boxRows(width)) + 1
}

func (m tuiModel) viewTimeline(width, height int) string {
	events := broker.Timeline(m.snap)
	// Focused only where it stands in for the radio; under the graph it is
	// read-only and must not light up with the radio's focus.
	rows := []string{numberedPaneTitle(5, fmt.Sprintf("Timeline (%d)", len(events)),
		m.focus == focusRadio && m.lower == lowerTimeline, width)}
	if len(events) == 0 {
		rows = append(rows, styDim.Render("   nothing yet"))
		return padRows(rows, width, height)
	}
	// Newest at the bottom, like the radio: the pane follows the run.
	if avail := height - 1; avail > 0 && len(events) > avail {
		events = events[len(events)-avail:]
	}
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
		line := styDim.Render(e.At.Local().Format("15:04:05")) + " " +
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
