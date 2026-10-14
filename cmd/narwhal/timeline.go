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
	"github.com/keyolk/tuikit/render"

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

// timelineFollow selects the newest entry and keeps it selected as more
// arrive; any other value of timelineSel is a fixed entry.
const timelineFollow = -1

// timelineItem is one selectable entry of pane 3: a line of what a task's
// outcome rests on, or a scene of how the run went.
type timelineItem struct {
	// task and reason are set for a provenance line; scene for a scene.
	task   string
	reason *broker.Reason
	scene  *broker.Scene
	// head is the task's heading, drawn above its first line.
	head string
}

// timelineItems is what pane 3 lists, in order: each task's provenance —
// the answer task first — then the scenes of how the run went.
func (m tuiModel) timelineItems() []timelineItem {
	var out []timelineItem
	for _, p := range broker.Provenances(m.snap) {
		ic, _ := taskIconStyle(p.Task.State)
		head := ic + " " + p.Task.ID
		if p.Task.Name != "" && p.Task.Name != p.Task.ID {
			head += " " + p.Task.Name
		}
		for i := range p.Reasons {
			it := timelineItem{task: p.Task.ID, reason: &p.Reasons[i]}
			if i == 0 {
				it.head = head
			}
			out = append(out, it)
		}
	}
	scenes := m.timelineScenes()
	for i := range scenes {
		out = append(out, timelineItem{scene: &scenes[i]})
	}
	return out
}

// selectedItem resolves the selection: following, the last entry.
func (m tuiModel) selectedItem(n int) (int, bool) {
	if n == 0 {
		return 0, false
	}
	if m.timelineSel == timelineFollow || m.timelineSel >= n {
		return n - 1, true
	}
	return max(m.timelineSel, 0), true
}

// selectedScene is the selected entry's scene index, when it is one.
func (m tuiModel) selectedScene(n int) (int, bool) {
	items := m.timelineItems()
	i, ok := m.selectedItem(len(items))
	if !ok || items[i].scene == nil {
		return 0, false
	}
	return i - (len(items) - n), true
}

// moveTimeline steps the selection; stepping onto the last follows again.
func (m *tuiModel) moveTimeline(delta int) {
	n := len(m.timelineItems())
	cur, ok := m.selectedItem(n)
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
	items := m.timelineItems()
	i, ok := m.selectedItem(len(items))
	if !ok || items[i].scene == nil {
		return
	}
	if m.sceneOpen == nil {
		m.sceneOpen = map[string]bool{}
	}
	k := items[i].scene.Key
	m.sceneOpen[k] = !m.sceneOpen[k]
	if m.timelineSel == timelineFollow {
		m.timelineSel = i
	}
}

// steerFromTimeline opens the steering line on the selected entry: a
// provenance line quoted to its task, a scene addressed to its task.
func (m *tuiModel) steerFromTimeline() bool {
	items := m.timelineItems()
	i, ok := m.selectedItem(len(items))
	if !ok {
		return false
	}
	it := items[i]
	switch {
	case it.reason != nil:
		m.startSteer(it.task)
		m.steerBuf = append(m.steerBuf, []rune(fmt.Sprintf("[%s: %s] → ", it.reason.Kind, render.Truncate(it.reason.Text, 120)))...)
		return true
	case it.scene != nil && it.scene.Kind == broker.SceneTask:
		m.startSteer(it.scene.Who[0])
		return true
	}
	return false
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
		out = append(out, pad+styDim.Render(render.Truncate(l, inner)))
	}
	if m.sceneOpen[sc.Key] {
		for _, e := range sc.Events {
			g, st := timelineGlyph(e.Kind)
			text := e.Text
			if text == "" || e.Kind == broker.TimelineStarted || e.Kind == broker.TimelineVerdict {
				text = strings.TrimSpace(string(e.Kind) + " " + text)
			}
			out = append(out, pad+styDim.Render(clock(e.At)+" ")+st.Render(g)+" "+
				styCyan.Render(render.Truncate(e.Who, 12))+" "+styDim.Render(render.Truncate(oneLine(text), max(4, inner-26))))
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
	items := m.timelineItems()
	sel, _ := m.selectedItem(len(items))
	focused := m.focus == focusTimeline

	var lines []string
	starts := make([]int, len(items)+1)
	marker := func(i int, block []string) []string {
		for j := range block {
			if focused && i == sel {
				block[j] = styCyanBold.Render("▌") + block[j]
			} else {
				block[j] = " " + block[j]
			}
		}
		return block
	}
	res := m.snap.Result()
	lines = append(lines, " "+styTitle.Render("결과"))
	switch {
	case res.Answer != "" && (res.AnswerFrom != "" || !m.runIsLive()):
		lines = append(lines, "   "+render.Truncate(flattenMarkdown(firstLine(res.Answer)), width-4))
	default:
		lines = append(lines, "   "+styDim.Render("아직 답 없음 — 아래 task들의 근거로 만들어지는 중"))
	}
	if open, _ := openQuestionCount(m.snap); open > 0 {
		lines = append(lines, "   "+styYellow.Render(fmt.Sprintf("답하지 않은 질문 %d개 (Q)", open)))
	}
	lines = append(lines, "", " "+styTitle.Render("근거")+styDim.Render("  — 줄을 고르고 i로 그 task에 정정을 보낸다"))
	firstScene := true
	for i, it := range items {
		if it.scene != nil && firstScene {
			firstScene = false
			lines = append(lines, "", " "+styTitle.Render("흐름"))
		}
		starts[i] = len(lines)
		var block []string
		switch {
		case it.reason != nil:
			if it.head != "" {
				lines = append(lines, "")
				starts[i] = len(lines)
				block = append(block, " "+styCyan.Render(it.head))
			}
			kind := styDim.Render(render.PadRight(string(it.reason.Kind), 6))
			text := it.reason.Text
			if it.reason.Flag {
				kind = styYellow.Render(render.PadRight(string(it.reason.Kind), 6))
				text = styYellow.Render("⚠ ") + text
			}
			for j, l := range wrapText(text, max(10, width-14)) {
				if j == 0 {
					block = append(block, "   "+kind+" "+l)
				} else {
					block = append(block, "          "+l)
				}
			}
		default:
			block = m.sceneLines(*it.scene, width-1)
		}
		lines = append(lines, marker(i, block)...)
		if it.scene != nil && i < len(items)-1 {
			lines = append(lines, "")
		}
	}
	starts[len(items)] = len(lines)

	avail := max(1, height-1)
	total := len(lines)
	start := 0
	if len(items) > 0 && m.timelineSel != timelineFollow {
		start = min(max(0, starts[sel]-avail/3), max(0, total-avail))
		if end := starts[sel+1]; end > start+avail {
			start = max(0, min(starts[sel], end-avail))
		}
	} else if m.timelineSel == timelineFollow {
		// Following, the result and the run's latest are what to see.
		start = 0
	}
	end := min(total, start+avail)

	label := "Timeline · 결과와 근거"
	if total > avail {
		label += fmt.Sprintf("  %d-%d/%d", start+1, end, total)
	}
	rows := []string{numberedPaneTitle(3, label, focused, width)}
	for _, l := range lines[start:end] {
		rows = append(rows, render.Truncate(l, width))
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
