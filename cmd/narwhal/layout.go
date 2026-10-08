// layout.go arranges the four panes as two framed bands.
//
//	╭─ 1 Graph ──────┬─ 2 Node ──────────────────╮
//	│                │                           │   what the run is: its
//	╰────────────────┴───────────────────────────╯   shape and one task
//	╭─ 3 Timeline ───┬─ 4 Radio │ Brief ─────────╮
//	│                │                           │   what the run did: its
//	╰────────────────┴───────────────────────────╯   history and channel
//
// The panes used to be four titled regions floating on one background, the
// left column split at one height and the right at another. Graph and Node
// are one question — pick a task, read it — and Timeline and Radio are
// another — what has happened. Framing each pair as one box, split at one
// shared height, says that, and gives the eye two things to look at rather
// than four. The numbers follow reading order, band by band.
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
	// zoom is the pane drawn across the body, or -1 for the bands.
	zoom focusPane
	// single means the body is too short for two bands: one band holds the
	// graph and pane 4, and the other two panes show zoomed when focused.
	single bool
	// left and right are the widths the panes in each column are drawn
	// at; top and bottom the inner heights of the two bands.
	left, right int
	top, bottom int
}

// Band geometry. A band costs a border row above and below; the columns
// cost three border cells and a cell of air inside each left border, so
// text does not sit against the line.
const (
	bandBorderRows = 2
	bandBorderCols = 5
	minTopBand     = 4
	minBottomBand  = 5
)

// singleBand reports whether the body is too short for two bands.
func (m tuiModel) singleBand() bool {
	return m.bodyHeight() < 2*bandBorderRows+minTopBand+minBottomBand
}

// effectiveZoom is the pane drawn across the body, or -1.
//
// A pane that has no place in a single band still shows when focused —
// across the body, as a zoom would — so its number means the same on every
// terminal, and leaving it restores the band. Derived from the body height
// alone, so the width functions can use it without a cycle.
func (m tuiModel) effectiveZoom() focusPane {
	if m.zoom >= 0 {
		return m.zoom
	}
	if (m.focus == focusNode || m.focus == focusTimeline) && m.singleBand() {
		return m.focus
	}
	return -1
}

func (m tuiModel) layout() paneLayout {
	body := m.bodyHeight()
	l := paneLayout{zoom: m.effectiveZoom(), single: m.singleBand()}
	if l.zoom >= 0 {
		l.left, l.right, l.top = m.width, m.width, body
		return l
	}
	l.left = m.columnWidth()
	l.right = max(20, m.width-l.left-bandBorderCols)
	if l.single {
		l.top = max(1, body-bandBorderRows)
		return l
	}
	inner := body - 2*bandBorderRows
	// The top band is sized to what it holds — the graph's drawing, the
	// node's fields and feed — and the bottom band, whose radio grows
	// without bound, takes the rest. + and - move the split from there.
	//
	// The node's feed alone could claim the screen: a busy worker has
	// hundreds of lines, and the pane scrolls. It gets up to two fifths of
	// the body, as it had before the bands. The graph gets what its
	// drawing needs, since a box cut off at the band's edge cannot be
	// scrolled to the way a feed can.
	node := min(m.inspectorContentHeight()-1, inner*2/5)
	want := max(m.graphContentHeight(l.left)-1, node) + m.heightDelta
	l.top = min(max(want, minTopBand), inner-minBottomBand)
	l.bottom = inner - l.top
	return l
}

// viewBands draws the main view's body.
func (m tuiModel) viewBands(l paneLayout) string {
	if l.single {
		return framedBand(l.left, l.right, l.top,
			m.viewTasks(l.left, l.top+1), m.viewChannel(l.right, l.top+1))
	}
	return framedBand(l.left, l.right, l.top,
		m.viewTasks(l.left, l.top+1), m.viewInspector(l.right, l.top+1)) + "\n" +
		framedBand(l.left, l.right, l.bottom,
			m.renderTimeline(m.timelineEvents(), l.left, l.bottom+1), m.viewChannel(l.right, l.bottom+1))
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
	if l.zoom >= 0 || l.single {
		return 0
	}
	return l.top
}
