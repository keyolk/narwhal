// layout.go arranges the run view as two columns in one frame.
//
//	╭─ 1 Graph ──────┬─ 2 Task · 3 Timeline · 4 Radio ─╮
//	│                │                                 │
//	│  pick a task   │  read one thing, at full height │
//	│                │                                 │
//	╰────────────────┴─────────────────────────────────╯
//
// It was four panes in two bands — graph and node over timeline and radio
// — all on screen at once. Each got a quarter of the screen, so the node's
// feed and the radio's posts were cut to a few lines, and the eye had four
// places to look with nothing to say which mattered. The flow and Outputs
// views had already settled on the shape that reads: a list on the left,
// one thing read on the right. The run view now has it too. The graph is
// the list; the right column is whichever of the task, the timeline or the
// radio was last chosen, with the others a key away in its title.
package main

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// paneLayout is where everything goes in the main view. It is computed in
// one place because rendering, scrolling and navigation all need the same
// numbers: a pane drawn at one height and scrolled at another moves the
// offset without moving the screen.
type paneLayout struct {
	// zoom is the pane drawn across the body, or -1 for the two columns.
	zoom focusPane
	// left and right are the widths the two columns are drawn at; top is
	// their inner height.
	left, right int
	top         int
}

// Frame geometry. The frame costs a border row above and below; the
// columns cost three border cells and a cell of air inside each left
// border, so text does not sit against the line.
const (
	bandBorderRows = 2
	bandBorderCols = 5
)

// effectiveZoom is the pane drawn across the body, or -1.
func (m tuiModel) effectiveZoom() focusPane {
	if m.zoom >= 0 {
		return m.zoom
	}
	return -1
}

// readerPane is the pane the right column shows: the focused one, or with
// the graph focused, the one last read — choosing a task should not change
// what is being read about it.
func (m tuiModel) readerPane() focusPane {
	if m.focus != focusTasks {
		return m.focus
	}
	if m.reader == focusTasks {
		return focusNode
	}
	return m.reader
}

func (m tuiModel) layout() paneLayout {
	body := m.bodyHeight()
	l := paneLayout{zoom: m.effectiveZoom()}
	if l.zoom >= 0 {
		l.left, l.right, l.top = m.width, m.width, body
		return l
	}
	l.left = m.columnWidth()
	l.right = max(20, m.width-l.left-bandBorderCols)
	l.top = max(1, body-bandBorderRows)
	return l
}

// viewBands draws the main view's body: the graph, and the pane being
// read under a title that names the others.
func (m tuiModel) viewBands(l paneLayout) string {
	return framedBand(l.left, l.right, l.top, m.viewTasks(l.left, l.top+1), m.viewReader(l.right, l.top+1))
}

// viewReader is the right column: the chosen pane, its own title replaced
// by the tabs.
func (m tuiModel) viewReader(w, h int) string {
	var pane string
	switch m.readerPane() {
	case focusTimeline:
		pane = m.renderTimeline(m.timelineEvents(), w, h)
	case focusRadio:
		pane = m.viewChannel(w, h)
	default:
		pane = m.viewInspector(w, h)
	}
	lines := strings.Split(pane, "\n")
	lines[0] = m.readerTabs(w)
	return strings.Join(lines, "\n")
}

// readerTabs is the right column's title: each pane it can show, by the
// number that shows it, the shown one marked.
func (m tuiModel) readerTabs(w int) string {
	focused := m.focus != focusTasks
	cur := m.readerPane()
	tab := func(n string, label string, p focusPane) string {
		switch {
		case p == cur && focused:
			return styCyan.Render(n+" ") + styCyanBold.Render(label)
		case p == cur:
			return styDim.Render(n+" ") + styTitle.Render(label)
		}
		return styDim.Render(n + " " + label)
	}
	radio := "Radio"
	if m.briefTab {
		radio = "Brief"
	}
	sep := styDim.Render(" · ")
	title := tab("2", "Task", focusNode) + sep + tab("3", "Timeline", focusTimeline) + sep + tab("4", radio, focusRadio)
	if cur == focusRadio {
		other := "Brief"
		if m.briefTab {
			other = "Radio"
		}
		title += styDim.Render("  (4 again → " + other + ")")
	}
	rule := w - ansi.StringWidth(title) - 1
	if rule < 1 {
		return ansi.Truncate(title, w, "…")
	}
	return title + " " + styDim.Render(strings.Repeat("─", rule))
}

// framedSingle is framedBand with one pane across the band, and a note at
// the right of its top edge.
func framedSingle(w, h int, pane, note string) string {
	w, h = max(w, 1), max(h, 0)
	lines := strings.Split(pane, "\n")
	c := w + 1
	edge := borderTitle(lineAt(lines, 0), c)
	if note != "" {
		// The note replaces the tail of the rule, so the edge keeps its width.
		n := " " + note + " ─"
		if ansi.StringWidth(edge)-ansi.StringWidth(n) > ansi.StringWidth(strings.TrimRight(ansi.Strip(edge), "─"))+2 {
			edge = ansi.Truncate(edge, c-ansi.StringWidth(n), "") + styDim.Render(n)
		}
	}
	var b strings.Builder
	b.WriteString(styDim.Render("╭") + edge + styDim.Render("╮"))
	bar := styDim.Render("│")
	for i := 1; i <= h; i++ {
		b.WriteString("\n" + bar + " " + fitCells(lineAt(lines, i), w) + bar)
	}
	b.WriteString("\n" + styDim.Render("╰"+strings.Repeat("─", c)+"╯"))
	return b.String()
}

// framedBand puts two panes side by side in one box.
//
// Each pane is rendered as before, title row first, at one row taller than
// the band's inner height. The title row becomes the band's top edge and
// the rest its body, so the panes do not need to know they are framed.
func framedBand(lw, rw, h int, left, right string) string {
	ll, rl := strings.Split(left, "\n"), strings.Split(right, "\n")
	edge := func(lines []string, w int) string {
		if len(lines) == 0 {
			return styDim.Render(strings.Repeat("─", w))
		}
		return borderTitle(lines[0], w)
	}
	// Each column is its pane plus the cell of air before it.
	lw, rw, h = max(lw, 0), max(rw, 0), max(h, 0)
	lc, rc := lw+1, rw+1
	var b strings.Builder
	b.WriteString(styDim.Render("╭") + edge(ll, lc) + styDim.Render("┬") + edge(rl, rc) + styDim.Render("╮"))
	bar := styDim.Render("│")
	for i := 1; i <= h; i++ {
		b.WriteString("\n" + bar + " " + fitCells(lineAt(ll, i), lw) + bar + " " + fitCells(lineAt(rl, i), rw) + bar)
	}
	b.WriteString("\n" + styDim.Render("╰"+strings.Repeat("─", lc)+"┴"+strings.Repeat("─", rc)+"╯"))
	return b.String()
}

// borderTitle turns a pane's title row ("1 Graph ─────") into the stretch
// of top border above it ("─ 1 Graph ─────"), exactly w cells wide. The
// rule the pane drew to fill its row is cut and redrawn to the band's
// width; the styled part — the focus colour — is kept as it was.
func borderTitle(title string, w int) string {
	core := ansi.StringWidth(strings.TrimRight(ansi.Strip(title), " ─"))
	room := max(0, w-3)
	tail := ""
	if core > room {
		tail = "…"
	}
	text := ansi.Truncate(title, min(core, room), tail) + " "
	return styDim.Render("─ ") + text + styDim.Render(strings.Repeat("─", max(0, w-2-ansi.StringWidth(text))))
}

func lineAt(lines []string, i int) string {
	if i < len(lines) {
		return lines[i]
	}
	return ""
}

// fitCells pads or cuts a styled line to exactly w cells, so the border
// after it lands in the same column on every row.
func fitCells(s string, w int) string {
	n := ansi.StringWidth(s)
	if n > w {
		return ansi.Truncate(s, w, "")
	}
	return s + strings.Repeat(" ", w-n)
}

// graphContentHeight is the rows the graph needs: its title, its drawing
// or lanes, and one row of air.
func (m tuiModel) graphContentHeight(width int) int {
	if !m.boxMode {
		return 1 + len(m.graphRows()) + 1
	}
	return 1 + len(m.boxRows(width)) + 1
}

// nodeBandRows is the node pane's inner height in the bands, or 0 where the
// bands do not show it — a zoomed pane, or a single band.
func (m tuiModel) nodeBandRows() int {
	l := m.layout()
	if l.zoom >= 0 || m.readerPane() != focusNode {
		return 0
	}
	return l.top
}
