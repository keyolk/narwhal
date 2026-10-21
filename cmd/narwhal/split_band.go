// split_band.go holds the boundary between the two columns of the run,
// flow and Outputs views: a tuikit split.Model per view.
//
// The run view used to keep an unclamped column delta (width_delta), moved
// four columns by < and >. The width was clamped only when drawn, so presses
// past either limit kept changing the delta with nothing moving on screen,
// and the next press the other way did nothing either: a dead zone, saved to
// ui.json. The flow and Outputs views had fixed widths. Now each view owns a
// split.Model whose ratio is clamped on every write, so a press always moves
// the divider or changes nothing; the divider can be dragged with the mouse,
// and = (or a double click on it) resets it.
//
// Until a view's split is moved, its left column follows the width as it
// always has, caps included (the graph stops at 44 columns, 52 for boxes, so
// it does not eat a wide screen by default). Moving the divider fixes a
// ratio, which then survives resizes and restarts; reset goes back to
// following the width.
package main

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/keyolk/tuikit/keys"
	"github.com/keyolk/tuikit/mouse"
	"github.com/keyolk/tuikit/split"
)

// bandKind names a view with two columns.
type bandKind int

const (
	bandRun bandKind = iota
	bandFlow
	bandOutputs
	bandCount
)

// Frame cells outside the split: the left border and the cell of air after
// it, and the right border. The split's divider is the middle border and the
// air after it (bandDivider), so the look is the one framedBand always drew.
const (
	bandFrameLeft = 2
	bandFrameCols = 3
	bandDivider   = bandBorderCols - bandFrameCols
)

// band is view k's split with this frame's settings: the minimums (the
// graph's depends on boxes or lanes) and the default the width gives. The
// stored model keeps only what the person did: the ratio and a drag.
func (m tuiModel) band(k bandKind) split.Model {
	sp := m.splits[k]
	sp.Divider = bandDivider
	// The column minimums bind long before these do; they only keep a
	// ratio restored from a much wider terminal sane.
	sp.Lo, sp.Hi = 0.05, 0.95
	// A collapsed band shows the column being read.
	sp.Primary = split.B
	sp.Collapse = 0
	sp.Grab = 0
	var def int
	switch k {
	case bandRun:
		minLeft, maxLeft := 24, 44
		if m.boxMode {
			// Boxes need two borders and a readable label.
			minLeft, maxLeft = 30, 52
		}
		sp.MinA, sp.MinB = minLeft, 20
		// Under narrowWidth the run view zooms the focused pane instead
		// (effectiveZoom); the split says the same.
		sp.Collapse = narrowWidth - bandFrameCols
		def = min(max(m.width/3, minLeft), maxLeft)
	case bandFlow:
		sp.MinA, sp.MinB = 30, 20
		def = max(m.width*2/5, 30)
	case bandOutputs:
		sp.MinA, sp.MinB = 28, 10
		def = min(max(m.width/3, 28), 48)
	}
	if avail := m.width - bandBorderCols; avail > 0 {
		sp.Default = float64(def) / float64(avail)
	}
	return sp
}

// bandInner is the rect the split is laid out in, frame excluded, h rows.
func (m tuiModel) bandInner(h int) mouse.Rect {
	return mouse.Rect{X: bandFrameLeft, W: max(m.width-bandFrameCols, 0), H: max(h, 1)}
}

// bandWidths are the two columns' widths in view k, and whether the band is
// collapsed to its right column (too narrow for both minimums).
func (m tuiModel) bandWidths(k bandKind) (lw, rw int, collapsed bool) {
	inner := m.bandInner(1)
	a, _, b := m.band(k).Layout(inner)
	if a.W == 0 {
		return 0, b.W, true
	}
	return a.W, b.W, false
}

// bandOf is the view a split key was pressed in.
func bandOf(ctx keys.Ctx) (bandKind, bool) {
	switch ctx {
	case ctxRun:
		return bandRun, true
	case ctxFlow:
		return bandFlow, true
	case ctxOutputs:
		return bandOutputs, true
	}
	return 0, false
}

// activeBand is the two-column view on screen, if one is and nothing is
// drawn over it.
func (m tuiModel) activeBand() (bandKind, bool) {
	switch {
	case m.palette, m.help, m.steering, m.composing, m.picker, m.questionsView:
		return 0, false
	case m.outputsView:
		return bandOutputs, true
	case m.flowView:
		return bandFlow, true
	case m.detail != detailClosed:
		return 0, false
	}
	return bandRun, true
}

// splitArea is where view k's split is on screen: the rows between the
// band's top and bottom edges, frame columns excluded. It is the zero Rect
// while the view shows one column (zoomed), so the split takes no press and
// a key changes only the ratio.
func (m tuiModel) splitArea(k bandKind) mouse.Rect {
	switch k {
	case bandRun:
		if m.effectiveZoom() >= 0 {
			return mouse.Rect{}
		}
		r := m.bandInner(m.layout().top)
		r.Y = lipgloss.Height(m.viewHeader()) + 1
		return r
	case bandFlow:
		if m.flowZoom {
			return mouse.Rect{}
		}
		g := m.flowGeometry()
		r := m.bandInner(g.bodyH - 2)
		r.Y = lipgloss.Height(g.head+"\n"+g.title) + 1
		return r
	case bandOutputs:
		g := m.outputsGeom()
		r := m.bandInner(g.rows)
		r.Y = g.top + 1
		return r
	}
	return mouse.Rect{}
}

// setBand stores view k's split after a change. A reset goes back to
// following the width rather than fixing the default at this width.
func (m *tuiModel) setBand(k bandKind, sp split.Model, reset bool) {
	if reset {
		m.splits[k], m.splitSet[k] = split.Model{}, false
		return
	}
	m.splits[k], m.splitSet[k] = sp, true
}

// doSplit runs a split key in the view it was pressed in.
func (m tuiModel) doSplit(b keys.Binding) (tea.Model, tea.Cmd) {
	k, ok := bandOf(b.Ctx)
	if !ok {
		return m, nil
	}
	if b.Action == split.ActionReset {
		m.setBand(k, split.Model{}, true)
		return m, nil
	}
	sp, r := m.band(k).Handle(b.Action, m.splitArea(k))
	if r.Changed {
		m.setBand(k, sp, false)
	}
	return m, nil
}

// splitMouse gives a mouse event to the split on screen before the text
// selection and the click handling see it. consumed: it was the split's (a
// press on the divider, a drag, its release) and nothing else may see it.
func (m tuiModel) splitMouse(msg tea.MouseMsg) (tuiModel, bool) {
	k, ok := m.activeBand()
	if !ok {
		return m, false
	}
	// Only a left press or a drag in progress can be the split's; the
	// check keeps every other event from measuring the view.
	if !m.splits[k].Dragging() && !mouse.IsLeftPress(msg) {
		return m, false
	}
	sp, r := m.band(k).UpdateAt(msg, m.splitArea(k), m.clock())
	if !r.Consumed {
		return m, false
	}
	// A press the split consumed that did not start a drag is the second
	// half of a double click: the reset.
	reset := mouse.IsLeftPress(msg) && !sp.Dragging()
	switch {
	case reset:
		m.setBand(k, sp, true)
	case r.Changed || m.splitSet[k]:
		m.setBand(k, sp, false)
	default:
		// A press or release that moved nothing keeps the band following
		// the width; only the drag state is kept.
		m.splits[k] = sp
	}
	if mouse.IsLeftPress(msg) {
		// A divider press is a click somewhere else than the last row.
		m.clicks = mouse.Clicks{}
	}
	return m, true
}

// splitStates are the moved splits, for ui.json: a view never moved saves
// nothing and keeps following the width.
func (m tuiModel) splitState(k bandKind) split.State {
	if !m.splitSet[k] {
		return split.State{}
	}
	return m.splits[k].State()
}

// restoreSplit applies a saved split; anything split.Restore rejects (a
// percentage, zero, a negative) leaves the view following the width.
func (m *tuiModel) restoreSplit(k bandKind, s split.State) {
	if sp, ok := (split.Model{}).Restore(s); ok {
		m.splits[k], m.splitSet[k] = sp, true
	}
}

// migrateWidthDelta turns the width_delta an older narwhal saved into the
// run split's ratio, once, at the first size: the delta was in columns over
// a third of the width, so it only means a ratio at a known width. The width
// the old code would have drawn at this size becomes the ratio. A run split
// saved since wins, and the delta is dropped either way.
func (m *tuiModel) migrateWidthDelta() {
	if m.legacyWidthDelta == 0 || m.width <= 0 {
		return
	}
	delta := m.legacyWidthDelta
	m.legacyWidthDelta = 0
	if m.splitSet[bandRun] {
		return
	}
	avail := m.width - bandBorderCols
	if avail <= 0 {
		return
	}
	left := legacyColumnWidth(m.width, delta, m.boxMode)
	sp := m.band(bandRun).Set(float64(left)/float64(avail), m.bandInner(1))
	m.setBand(bandRun, sp, false)
}

// legacyColumnWidth is the graph column the width_delta layout drew at
// width, kept only to migrate a saved delta.
func legacyColumnWidth(width, delta int, boxMode bool) int {
	w := width/3 + delta
	minLeft, maxLeft := 24, 44
	if boxMode {
		minLeft, maxLeft = 30, 52
	}
	maxLeft = max(maxLeft, width-24)
	w = min(max(w, minLeft), maxLeft, width-2)
	return max(w, 1)
}

// splitBindings are view ctx's split keys: < > = from the kit, in the
// footer as one "width" hint at priority p, under the view's help group.
func splitBindings(ctx keys.Ctx, group string, p int) []keys.Binding {
	bs := split.Bindings(ctx)
	for i := range bs {
		bs[i].Group = group
		switch bs[i].Action {
		case split.ActionGrow:
			bs[i].Help = "wider left column"
			bs[i].Footer, bs[i].FooterGroup = p, "width"
		case split.ActionShrink:
			bs[i].Help = "narrower left column"
			bs[i].Footer, bs[i].FooterGroup = p, "width"
		case split.ActionReset:
			bs[i].Help = "columns back to the default widths"
		}
	}
	return bs
}
