// timeline.go draws pane 3, the run's history told as scenes: each task's
// course, the workers' exchanges, the operator's instructions and where
// they went, and where the run stands now. See broker/scenes.go. space
// opens a scene into the events it was made of; enter goes to its task or
// message.
//
// Brief is the run as it stands; a decision drops off it once dealt with.
// This is the same run told in order, so "what happened while I was away"
// has an answer on screen. It is a pane like the others: `3` focuses it,
// j/k scroll it, `z` zooms it, and `narwhal export` writes the whole of it.
package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/keyolk/narwhal/internal/broker"
)

// timelineEvents is every event of the run's history, oldest first: what
// the scenes are made of, and what an opened scene lists.
func (m tuiModel) timelineEvents() []broker.TimelineEvent {
	return broker.Timeline(m.snap)
}

// timelineScenes is what pane 3 lists: the run told as scenes, ending with
// where it stands. See broker/scenes.go.
func (m tuiModel) timelineScenes() []broker.Scene {
	return broker.Scenes(m.snap, time.Now())
}

// timelineZoomed reports whether pane 3 is drawn across the body.
func (m tuiModel) timelineZoomed() bool {
	return m.effectiveZoom() == focusTimeline
}

// timelineRows is how many rows pane 3 shows at once where it is drawn.
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

// timelineFollow selects the newest scene and keeps it selected as more
// arrive; any other value of timelineSel is a fixed scene.
const timelineFollow = -1

// selectedScene resolves the selection against the scenes.
func (m tuiModel) selectedScene(n int) (int, bool) {
	if n == 0 {
		return 0, false
	}
	if m.timelineSel == timelineFollow || m.timelineSel >= n {
		return n - 1, true
	}
	return max(m.timelineSel, 0), true
}

// moveTimeline steps the selection; stepping onto the newest follows again.
func (m *tuiModel) moveTimeline(delta int) {
	n := len(m.timelineScenes())
	cur, ok := m.selectedScene(n)
	if !ok {
		return
	}
	cur += delta
	if cur >= n-1 {
		m.timelineSel = timelineFollow
		return
	}
	m.timelineSel = max(cur, 0)
}

// toggleScene opens the selected scene into its events, or closes it.
func (m *tuiModel) toggleScene() {
	scenes := m.timelineScenes()
	i, ok := m.selectedScene(len(scenes))
	if !ok {
		return
	}
	if m.sceneOpen == nil {
		m.sceneOpen = map[string]bool{}
	}
	k := scenes[i].Key
	m.sceneOpen[k] = !m.sceneOpen[k]
	if m.timelineSel == timelineFollow {
		m.timelineSel = i
	}
}

// viewTimeline is pane 3 zoomed across the body.
func (m tuiModel) viewTimeline(width, height int) string {
	return m.renderTimeline(width, height)
}

// sceneLines draws one scene: its time, mark and title, then the note the
// daemon wrote for it, then what the rules could say, then — opened — the
// events it was made of.
func (m tuiModel) sceneLines(sc broker.Scene, width int) []string {
	glyph, style := sceneStyle(sc)
	at := styDim.Render("   now  ")
	if sc.Kind != broker.SceneNow {
		at = styDim.Render(clock(sc.At())[:5] + "   ")
	}
	title := style.Render(glyph) + " " + styTitle.Render(sc.Title)
	if sc.Kind == broker.SceneNow {
		title = style.Render(glyph) + " " + style.Render(sc.Title)
	}
	out := []string{at + title}
	pad := strings.Repeat(" ", 10)
	inner := max(10, width-10)
	if sc.Note != "" {
		for _, l := range wrapText(sc.Note, inner) {
			out = append(out, pad+l)
		}
	}
	for i, l := range sc.Lines {
		// A finished task's first line is its outcome, which the note
		// says better when there is one.
		if sc.Note != "" && sc.Kind == broker.SceneTask && i == 0 && sc.Outcome != "" {
			continue
		}
		out = append(out, pad+styDim.Render(truncate(l, inner)))
	}
	if m.sceneOpen[sc.Key] {
		for _, e := range sc.Events {
			g, st := timelineGlyph(e.Kind)
			text := e.Text
			if text == "" || e.Kind == broker.TimelineStarted || e.Kind == broker.TimelineVerdict {
				text = strings.TrimSpace(string(e.Kind) + " " + text)
			}
			out = append(out, pad+styDim.Render(clock(e.At)+" ")+st.Render(g)+" "+
				styCyan.Render(truncate(e.Who, 12))+" "+styDim.Render(truncate(oneLine(text), max(4, inner-26))))
		}
	}
	return out
}

// sceneStyle is a scene's mark and colour.
func sceneStyle(sc broker.Scene) (string, lipgloss.Style) {
	switch sc.Mark {
	case "✓":
		return sc.Mark, styGreen
	case "✗":
		return sc.Mark, styRedBold
	case "⚠":
		return sc.Mark, styYellow
	case "▸":
		return sc.Mark, styCyan
	case "»":
		return sc.Mark, styYellow
	case "◆":
		return sc.Mark, styTitle
	}
	return "·", styDim
}

func (m tuiModel) renderTimeline(width, height int) string {
	scenes := m.timelineScenes()
	sel, _ := m.selectedScene(len(scenes))
	focused := m.focus == focusTimeline

	// Every scene's lines, with where each starts, so the window can be
	// placed on the selected one.
	var lines []string
	starts := make([]int, len(scenes)+1)
	for i, sc := range scenes {
		starts[i] = len(lines)
		block := m.sceneLines(sc, width-1)
		if focused && i == sel {
			for j := range block {
				block[j] = styCyanBold.Render("▌") + block[j]
			}
		} else {
			for j := range block {
				block[j] = " " + block[j]
			}
		}
		lines = append(lines, block...)
		if i < len(scenes)-1 {
			lines = append(lines, "")
		}
	}
	starts[len(scenes)] = len(lines)

	avail := max(1, height-1)
	total := len(lines)
	start := max(0, total-avail)
	if m.timelineSel != timelineFollow && len(scenes) > 0 {
		// The selected scene in view, with what came before it above.
		start = min(max(0, starts[sel]-avail/3), max(0, total-avail))
		if end := starts[sel+1]; end > start+avail {
			start = max(0, min(starts[sel], end-avail))
		}
	}
	end := min(total, start+avail)

	label := fmt.Sprintf("Timeline (%d)", max(0, len(scenes)-1))
	if m.timelineSel == timelineFollow {
		label += " ⌄"
	}
	rows := []string{numberedPaneTitle(3, label, focused, width)}
	if len(scenes) == 0 {
		return padRows(append(rows, styDim.Render("   nothing yet")), width, height)
	}
	for _, l := range lines[start:end] {
		rows = append(rows, ansi.Truncate(l, width, "…"))
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
