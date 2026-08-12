// graph_box.go renders a laid-out DAG as an ASCII/Unicode diagram.
//
// An earlier version drew full-width boxes stacked vertically, which looked
// like a list with borders rather than a graph. What makes terminal graph
// tools — Graph::Easy, dot-to-ascii, dgraph — read as diagrams is that
// nodes are only as wide as their content and there is whitespace around
// them, so the edges have somewhere to run.
//
// So: draw onto a character canvas. Boxes are sized to their label and
// placed by (layer, lane); edges are routed between them as vertical drops
// joined by a horizontal run. Siblings sit side by side when they fit and
// fall back to stacking when they do not, because the pane is a third of
// the screen and a wide graph must degrade rather than overflow.
//
//	         ┌────────────────┐
//	         │ ✓ auth-audit   │
//	         └───────┬────────┘
//	                 │
//	    ┌────────────┴───────────┐
//	    │                        │
//	┌───┴────┐              ┌────┴─────┐
//	│ ▶ api  │              │ ○ utils  │
//	└───┬────┘              └────┬─────┘
//	    │                        │
//	    └───────────┬────────────┘
//	                │
//	      ┌─────────┴─────────┐
//	      │ · synthesis       │
//	      └───────────────────┘
package main

import (
	"sort"
	"strconv"
	"strings"

	"github.com/keyolk/narwhal/internal/broker"
)

// Box-drawing set. Sharp corners for boxes read as crisper at small sizes
// than rounded ones; the routing uses the matching tee/elbow forms.
const (
	boxTopLeft     = "┌"
	boxTopRight    = "┐"
	boxBottomLeft  = "└"
	boxBottomRight = "┘"
	boxHoriz       = "─"
	boxVert        = "│"

	// Junctions where an edge meets a border or another edge.
	jTeeDown  = "┬" // edge leaving the bottom of a box
	jTeeUp    = "┴" // edge arriving at the top of a box
	jElbowNW  = "┌"
	jElbowNE  = "┐"
	jElbowSW  = "└"
	jElbowSE  = "┘"
	jCross    = "┼"
	jTeeRight = "├"
	jTeeLeft  = "┤"
)

// boxPart identifies which line of a box a row is, so selection can
// highlight the whole box rather than one line of it.
type boxPart int

const (
	partRoute boxPart = iota
	partTop
	partBody
	partBottom
)

// boxRow is one rendered line. Rows overlapping a task carry its index in
// layout order; pure routing rows carry -1.
type boxRow struct {
	text string
	node int
	part boxPart
}

// canvas is a fixed-size grid of runes drawn onto by absolute coordinates.
// Terminal diagrams are easier to reason about as a grid than as string
// concatenation, especially where edges cross.
type canvas struct {
	cells [][]rune
	w, h  int
}

func newCanvas(w, h int) *canvas {
	c := &canvas{w: w, h: h, cells: make([][]rune, h)}
	for y := range c.cells {
		row := make([]rune, w)
		for x := range row {
			row[x] = ' '
		}
		c.cells[y] = row
	}
	return c
}

func (c *canvas) set(x, y int, r rune) {
	if x < 0 || y < 0 || x >= c.w || y >= c.h {
		return
	}
	c.cells[y][x] = r
}

// join places r at (x,y), merging it with what is already there so crossing
// edges produce the right junction instead of overwriting each other.
func (c *canvas) join(x, y int, r rune) {
	if x < 0 || y < 0 || x >= c.w || y >= c.h {
		return
	}
	cur := c.cells[y][x]
	c.cells[y][x] = mergeGlyph(cur, r)
}

// mergeGlyph combines two box-drawing runes into the junction that carries
// both. Only the cases the router actually produces are handled; anything
// else keeps the newer glyph.
func mergeGlyph(a, b rune) rune {
	if a == ' ' || a == b {
		return b
	}
	const (
		h = '─'
		v = '│'
	)
	switch {
	case a == h && b == v, a == v && b == h:
		return '┼'
	case a == '┬' && b == v, a == v && b == '┬':
		return '┼'
	case a == '┴' && b == v, a == v && b == '┴':
		return '┼'
	}
	return b
}

func (c *canvas) lines() []string {
	out := make([]string, c.h)
	for y, row := range c.cells {
		out[y] = strings.TrimRight(string(row), " ")
	}
	return out
}

// hline draws a horizontal run from x1 to x2 inclusive.
func (c *canvas) hline(x1, x2, y int) {
	if x1 > x2 {
		x1, x2 = x2, x1
	}
	for x := x1; x <= x2; x++ {
		c.join(x, y, '─')
	}
}

// vline draws a vertical run from y1 to y2 inclusive.
func (c *canvas) vline(x, y1, y2 int) {
	if y1 > y2 {
		y1, y2 = y2, y1
	}
	for y := y1; y <= y2; y++ {
		c.join(x, y, '│')
	}
}

// placedBox is a node with a resolved position and size on the canvas.
type placedBox struct {
	node  graphNode
	index int // position in layout order
	x, y  int // top-left corner
	w, h  int // outer dimensions, borders included
	label string
	badge string
	icon  string
}

// tap returns the column an edge attaches to: the box's horizontal center.
func (b placedBox) tap() int { return b.x + b.w/2 }

// renderBoxes draws the layout as a diagram sized to fit width.
func (g graphLayout) renderBoxes(width int, iconFor func(broker.TaskState) (string, string)) []boxRow {
	if len(g.nodes) == 0 {
		return nil
	}

	// Group by layer; siblings in a layer are candidates for sharing a row.
	byLayer := map[int][]graphNode{}
	maxLayer := 0
	for _, n := range g.nodes {
		byLayer[n.layer] = append(byLayer[n.layer], n)
		if n.layer > maxLayer {
			maxLayer = n.layer
		}
	}
	index := map[string]int{}
	for i, n := range g.nodes {
		index[n.task.ID] = i
	}

	const (
		boxHeight = 3 // top border, body, bottom border
		gapX      = 2 // whitespace between sibling boxes
		gapY      = 2 // routing band between layers
	)

	var placed []placedBox
	y := 0
	for layer := 0; layer <= maxLayer; layer++ {
		nodes := byLayer[layer]
		if len(nodes) == 0 {
			continue
		}
		rows := packRow(nodes, width, gapX, iconFor)
		for _, row := range rows {
			// Center the row's boxes in the pane so the diagram has margins
			// rather than hugging the left edge.
			total := 0
			for i, b := range row {
				total += b.w
				if i > 0 {
					total += gapX
				}
			}
			x := (width - total) / 2
			if x < 0 {
				x = 0
			}
			for i := range row {
				row[i].x = x
				row[i].y = y
				row[i].h = boxHeight
				row[i].index = index[row[i].node.task.ID]
				x += row[i].w + gapX
				placed = append(placed, row[i])
			}
			y += boxHeight + gapY
		}
	}

	height := y
	if height < boxHeight {
		height = boxHeight
	}
	c := newCanvas(width, height)

	pos := map[string]placedBox{}
	for _, b := range placed {
		pos[b.node.task.ID] = b
	}

	// Boxes first, then edges. Drawing edges last lets a tee replace the
	// plain border run where an edge meets a frame; the reverse order had
	// drawBox paint over every tee it had just placed.
	for _, b := range placed {
		drawBox(c, b)
	}
	// Edges are grouped before drawing, because an edge drawn on its own
	// lays a horizontal bar that the next edge overwrites. A fan-out (one
	// parent, many children) and a fan-in (many parents, one child) are
	// each a single shape and are drawn as one.
	//
	// Fan-ins win when a node has several parents: routing it from each
	// parent separately produced overlapping bars on the same line.
	drawn := map[string]bool{} // "parent\x00child" edges already covered

	for _, ch := range placed {
		var ps []placedBox
		for _, d := range ch.node.task.Deps {
			if p, ok := pos[d]; ok {
				ps = append(ps, p)
			}
		}
		if len(ps) < 2 {
			continue
		}
		routeFanIn(c, ps, ch)
		for _, p := range ps {
			drawn[p.node.task.ID+"\x00"+ch.node.task.ID] = true
		}
	}

	children := map[string][]placedBox{}
	var parents []placedBox
	seenParent := map[string]bool{}
	for _, b := range placed {
		for _, d := range b.node.task.Deps {
			p, ok := pos[d]
			if !ok || drawn[d+"\x00"+b.node.task.ID] {
				continue
			}
			children[d] = append(children[d], b)
			if !seenParent[d] {
				seenParent[d] = true
				parents = append(parents, p)
			}
		}
	}
	for _, p := range parents {
		routeFanOut(c, p, children[p.node.task.ID])
	}

	// Trim trailing blank lines so the pane does not carry dead space.
	lines := c.lines()
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	rows := make([]boxRow, len(lines))
	for i, ln := range lines {
		rows[i] = boxRow{text: ln, node: -1, part: partRoute}
	}
	for _, b := range placed {
		for dy := 0; dy < b.h; dy++ {
			y := b.y + dy
			if y >= len(rows) {
				break
			}
			part := partBody
			switch dy {
			case 0:
				part = partTop
			case b.h - 1:
				part = partBottom
			}
			// A line can only belong to one task; layers never overlap
			// vertically, so the first claim is the only claim.
			if rows[y].node == -1 {
				rows[y].node = b.index
				rows[y].part = part
			}
		}
	}
	return rows
}

// packRow splits a layer's nodes into rows that fit the pane. Siblings sit
// side by side when they fit; otherwise the layer wraps onto more rows.
func packRow(
	nodes []graphNode,
	width, gapX int,
	iconFor func(broker.TaskState) (string, string),
) [][]placedBox {
	var rows [][]placedBox
	var cur []placedBox
	used := 0

	for _, n := range nodes {
		b := measureBox(n, width, iconFor)
		need := b.w
		if len(cur) > 0 {
			need += gapX
		}
		if len(cur) > 0 && used+need > width {
			rows = append(rows, cur)
			cur, used = nil, 0
			need = b.w
		}
		cur = append(cur, b)
		used += need
	}
	if len(cur) > 0 {
		rows = append(rows, cur)
	}
	return rows
}

// measureBox sizes a box to its content, capped so one long id cannot push
// the diagram past the pane.
func measureBox(n graphNode, width int, iconFor func(broker.TaskState) (string, string)) placedBox {
	icon, _ := iconFor(n.task.State)
	label := n.task.ID
	badge := ""
	if n.task.Dispatches > 1 {
		badge = "×" + strconv.Itoa(n.task.Dispatches)
	}

	// " icon label badge " plus two borders.
	inner := 1 + displayWidth(icon) + 1 + displayWidth(label) + 1
	if badge != "" {
		inner += displayWidth(badge) + 1
	}
	max := width - 2
	if max < 10 {
		max = 10
	}
	if inner > max {
		inner = max
	}
	return placedBox{node: n, w: inner + 2, label: label, badge: badge, icon: icon}
}

// drawBox paints a box's frame and label onto the canvas.
func drawBox(c *canvas, b placedBox) {
	right := b.x + b.w - 1
	bottom := b.y + b.h - 1

	c.set(b.x, b.y, []rune(boxTopLeft)[0])
	c.set(right, b.y, []rune(boxTopRight)[0])
	c.set(b.x, bottom, []rune(boxBottomLeft)[0])
	c.set(right, bottom, []rune(boxBottomRight)[0])

	for x := b.x + 1; x < right; x++ {
		c.set(x, b.y, '─')
		c.set(x, bottom, '─')
	}
	for y := b.y + 1; y < bottom; y++ {
		c.set(b.x, y, []rune(boxVert)[0])
		c.set(right, y, []rune(boxVert)[0])
	}

	body := " " + b.icon + " " + b.label
	inner := b.w - 2
	if b.badge != "" && displayWidth(body)+displayWidth(b.badge)+2 <= inner {
		pad := inner - displayWidth(body) - displayWidth(b.badge) - 1
		body += strings.Repeat(" ", pad) + b.badge + " "
	}
	body = padRight(truncate(body, inner), inner)

	x := b.x + 1
	for _, r := range body {
		c.set(x, b.y+1, r)
		x += runeCells(r)
	}
}

// routeFanIn draws every edge arriving at one child: a drop out of each
// parent, one shared horizontal bar, and one drop into the child. It is
// the mirror of routeFanOut, and exists for the same reason — several
// parents feeding one node is a single shape, not N independent edges.
//
// Parents are grouped by the row they sit on. A layer too wide for the
// pane wraps onto extra rows, and a single shared bar would then have to
// cross the boxes on the rows between; one bar per parent row avoids that
// entirely, at the cost of a few more lines.
func routeFanIn(c *canvas, parents []placedBox, child placedBox) {
	if len(parents) == 0 {
		return
	}
	byRow := map[int][]placedBox{}
	var rowOrder []int
	for _, p := range parents {
		if _, seen := byRow[p.y]; !seen {
			rowOrder = append(rowOrder, p.y)
		}
		byRow[p.y] = append(byRow[p.y], p)
	}
	sort.Ints(rowOrder)

	cx, cy := child.tap(), child.y
	c.set(cx, cy, []rune(jTeeUp)[0])

	// Each parent row gets its own bar. The child's column then carries the
	// connection from one bar down to the next, and from the last bar into
	// the child — running it to the child from every bar would draw through
	// the boxes on the rows in between.
	bars := make([]int, 0, len(rowOrder))
	for _, ry := range rowOrder {
		group := byRow[ry]
		bar := ry + group[0].h // one line below the row's bottom border
		if bar >= cy {
			bar = cy - 1
		}
		bars = append(bars, bar)

		lo, hi := cx, cx
		for _, p := range group {
			x := p.tap()
			if x < lo {
				lo = x
			}
			if x > hi {
				hi = x
			}
		}

		for _, p := range group {
			c.set(p.tap(), ry+p.h-1, []rune(jTeeDown)[0])
		}
		c.hline(lo, hi, bar)
		for _, p := range group {
			c.set(p.tap(), bar, sourceJunction(p.tap(), lo, hi))
		}
		c.set(cx, bar, barJunction(cx, lo, hi))
	}

	// Stitch the bars together. The connection runs down the child's column,
	// but only where that column is clear — a layer too wide for the pane
	// wraps onto extra rows, and those boxes sit directly below the first
	// bar. Where the column is occupied the run is routed down the margin
	// instead, outside every box.
	for i, bar := range bars {
		end := cy - 1
		if i+1 < len(bars) {
			end = bars[i+1] - 1
		}
		if bar+1 > end {
			continue
		}
		if columnClear(c, cx, bar+1, end) {
			c.vline(cx, bar+1, end)
			continue
		}
		detour := freeColumn(c, bar+1, end, cx)
		if detour < 0 {
			// Nowhere to route: fall back to the direct line rather than
			// dropping the edge entirely.
			c.vline(cx, bar+1, end)
			continue
		}
		c.hline(cx, detour, bar)
		c.vline(detour, bar+1, end)
		c.hline(detour, cx, end)
		c.set(detour, bar, cornerAt(detour, cx, true))
		c.set(detour, end, cornerAt(detour, cx, false))
	}
}

// columnClear reports whether a vertical run can pass through unobstructed.
func columnClear(c *canvas, x, y1, y2 int) bool {
	for y := y1; y <= y2; y++ {
		if y < 0 || y >= c.h || x < 0 || x >= c.w {
			continue
		}
		if c.cells[y][x] != ' ' {
			return false
		}
	}
	return true
}

// freeColumn finds a column outside the boxes that a detour can use,
// searching outward from the margins. Returns -1 when the canvas is full.
func freeColumn(c *canvas, y1, y2, avoid int) int {
	for x := 0; x < c.w; x++ {
		if x == avoid {
			continue
		}
		if columnClear(c, x, y1, y2) {
			return x
		}
	}
	return -1
}

// cornerAt returns the elbow for a detour turn. entering says whether the
// edge is leaving the main column for the detour.
func cornerAt(detour, main int, entering bool) rune {
	toRight := detour > main
	switch {
	case entering && toRight:
		return []rune(jElbowNE)[0] // ┐ leave right, turn down
	case entering && !toRight:
		return []rune(jElbowNW)[0] // ┌ leave left, turn down
	case !entering && toRight:
		return []rune(jElbowSE)[0] // ┘ come back from the right
	default:
		return []rune(jElbowSW)[0] // └ come back from the left
	}
}

// routeFanOut draws every edge leaving one parent as a single structure:
// one drop out of the parent, one shared horizontal bar, and one drop into
// each child.
//
// Routing edges one at a time made each arm lay its own bar on the same
// line, and the later ones overwrote the corners of the earlier ones. A
// fan-out is one shape, so it is drawn as one shape.
func routeFanOut(c *canvas, parent placedBox, children []placedBox) {
	if len(children) == 0 {
		return
	}
	px := parent.tap()
	py := parent.y + parent.h - 1
	c.set(px, py, []rune(jTeeDown)[0])

	// The bar sits midway between the parent's bottom and the nearest
	// child's top, so the drops are visible on both sides.
	top := children[0].y
	for _, ch := range children {
		if ch.y < top {
			top = ch.y
		}
	}
	bar := py + (top-py)/2
	if bar <= py {
		bar = py + 1
	}
	if bar >= top {
		bar = top - 1
	}

	// Straight drop: nothing to route.
	if len(children) == 1 && children[0].tap() == px {
		ch := children[0]
		c.vline(px, py+1, ch.y-1)
		c.set(ch.tap(), ch.y, []rune(jTeeUp)[0])
		return
	}

	c.vline(px, py+1, bar)

	lo, hi := px, px
	for _, ch := range children {
		x := ch.tap()
		if x < lo {
			lo = x
		}
		if x > hi {
			hi = x
		}
	}
	c.hline(lo, hi, bar)

	// Drops into each child, then the junction glyphs. Junctions are
	// written last so a corner is not downgraded to a plain line by a
	// later drop crossing the same cell.
	for _, ch := range children {
		x := ch.tap()
		c.vline(x, bar+1, ch.y-1)
		c.set(ch.tap(), ch.y, []rune(jTeeUp)[0])
	}
	for _, ch := range children {
		c.set(ch.tap(), bar, barJunction(ch.tap(), lo, hi))
	}
	c.set(px, bar, sourceJunction(px, lo, hi))
}

// barJunction is the glyph where a child's drop meets the shared bar.
func barJunction(x, lo, hi int) rune {
	switch x {
	case lo:
		return []rune(jElbowNW)[0] // ┌ bar turns down at its left end
	case hi:
		return []rune(jElbowNE)[0] // ┐ and at its right end
	default:
		return []rune(jTeeDown)[0] // ┬ a drop leaving the middle
	}
}

// sourceJunction is the glyph where the parent's drop meets the bar.
func sourceJunction(x, lo, hi int) rune {
	switch x {
	case lo:
		return []rune(jElbowSW)[0] // └ arriving from above at the left end
	case hi:
		return []rune(jElbowSE)[0] // ┘ at the right end
	default:
		return []rune(jTeeUp)[0] // ┴ arriving in the middle
	}
}

// runeCells is the column width of a single rune.
func runeCells(r rune) int {
	w := displayWidth(string(r))
	if w < 1 {
		return 1
	}
	return w
}
