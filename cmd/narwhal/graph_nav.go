// graph_nav.go moves the graph cursor by what is on screen.
//
// The box view draws a diagram, so the cursor should move the way the eye
// does: down goes to the box below, not to whatever happens to be next in
// the layout list. Those two disagree constantly. In a run with three
// siblings over one synthesis task, pressing down from the middle sibling
// went to its right-hand neighbour — because the neighbour sorts next by
// (layer, id) — while the box directly underneath was the one being looked
// at.
//
// So movement is computed from the rendered geometry: each box knows the
// row it starts on and the columns it occupies, which is enough to answer
// "what is below this" and "what is to the left".
package main

// boxPos is a task's position in the rendered diagram.
type boxPos struct {
	node   int
	top    int // first row of the box
	x0, x1 int // column range, half-open
}

// center is the column the box is centred on, used to pick the nearest
// box when moving between rows of different widths.
func (b boxPos) center() int { return (b.x0 + b.x1) / 2 }

// boxPositions returns where each task's box sits in the current render.
//
// A task appears on several rows (top border, body, bottom border); the
// first is the one that matters for ordering, so later rows are ignored.
func (m tuiModel) boxPositions() []boxPos {
	rows := m.boxRows(m.graphPaneWidth())
	var out []boxPos
	seen := map[int]bool{}
	for y, r := range rows {
		for _, s := range r.spans {
			if seen[s.node] {
				continue
			}
			seen[s.node] = true
			out = append(out, boxPos{node: s.node, top: y, x0: s.x0, x1: s.x1})
		}
	}
	return out
}

// posOf finds a task's position, and ok=false when it is not drawn.
func posOf(positions []boxPos, node int) (boxPos, bool) {
	for _, p := range positions {
		if p.node == node {
			return p, true
		}
	}
	return boxPos{}, false
}

// moveVertical moves the cursor to the box above or below the current one.
//
// Down follows a dependency edge: in a diagram that line is what "below"
// means, and it is the one relationship h and l cannot express, since they
// already own the horizontal axis. So j from a fan-in sibling reaches the
// child it feeds even when the child is drawn off to one side.
//
// Failing an edge, it falls back to a box sharing the current one's
// columns, so j still works in a graph with no dependencies. Failing both,
// the cursor stays: moving to an unrelated box because it was the only
// candidate teaches you that the arrow keys are unpredictable.
//
// dir is -1 for up and +1 for down.
func (m *tuiModel) moveVertical(dir int) {
	if m.focus != focusTasks {
		return
	}
	if !m.boxMode {
		// The lane view is a plain vertical list: layout order is screen
		// order, so index arithmetic is the honest answer there.
		m.taskCur += dir
		m.clampCursors()
		return
	}

	positions := m.boxPositions()
	cur, ok := posOf(positions, m.taskCur)
	if !ok {
		m.taskCur += dir
		m.clampCursors()
		return
	}

	// The next row in the chosen direction, if any.
	targetRow := -1
	for _, p := range positions {
		if dir > 0 && p.top > cur.top {
			if targetRow == -1 || p.top < targetRow {
				targetRow = p.top
			}
		}
		if dir < 0 && p.top < cur.top {
			if targetRow == -1 || p.top > targetRow {
				targetRow = p.top
			}
		}
	}
	if targetRow == -1 {
		return // already at the top or bottom row
	}

	// Follow the graph's own edges first. In a diagram the vertical
	// relationship *is* the dependency — that is the line drawn on screen —
	// while horizontal position is what h and l already move along. An
	// earlier version preferred whatever box overlapped the current one's
	// columns, which is the same axis h/l covers and left j doing nothing
	// from two thirds of a fan-in.
	best := m.linkedNeighbour(dir, targetRow, positions)

	if best < 0 {
		// No edge to follow. Fall back to a box that shares columns, which
		// keeps j moving in graphs with no dependencies at all — a flat row
		// wrapped onto two lines is still something you want to walk.
		//
		// Overlap is required rather than nearest-by-centre: with five
		// boxes on a row and one child under the second, nearest-by-centre
		// sent every box on the row to that child, since it was the only
		// thing below and won by default.
		bestDist := 0
		for _, p := range positions {
			if p.top != targetRow {
				continue
			}
			if p.x1 <= cur.x0 || p.x0 >= cur.x1 {
				continue // no shared columns: not below, just elsewhere
			}
			d := p.center() - cur.center()
			if d < 0 {
				d = -d
			}
			if best == -1 || d < bestDist {
				best, bestDist = p.node, d
			}
		}
	}
	if best >= 0 {
		m.taskCur = best
		m.clampCursors()
	}
}

// linkedNeighbour finds a box on the target row joined to the current one
// by a dependency edge, preferring the nearest when several qualify.
//
// Only edges count. A box that merely happens to sit on the row below is
// not somewhere "down" leads — that was the original defect, where every
// box on a five-wide row jumped to the one child beneath the second.
func (m tuiModel) linkedNeighbour(dir, targetRow int, positions []boxPos) int {
	cur, ok := posOf(positions, m.taskCur)
	if !ok {
		return -1
	}
	curID := m.taskByIndex(m.taskCur).ID

	best, bestDist := -1, 0
	for _, p := range positions {
		if p.top != targetRow {
			continue
		}
		other := m.taskByIndex(p.node)
		linked := false
		if dir > 0 {
			// Moving down: the candidate depends on the current task.
			for _, d := range other.Deps {
				if d == curID {
					linked = true
					break
				}
			}
		} else {
			// Moving up: the current task depends on the candidate.
			for _, d := range m.taskByIndex(m.taskCur).Deps {
				if d == other.ID {
					linked = true
					break
				}
			}
		}
		if !linked {
			continue
		}
		d := p.center() - cur.center()
		if d < 0 {
			d = -d
		}
		if best == -1 || d < bestDist {
			best, bestDist = p.node, d
		}
	}
	return best
}

// moveHorizontal moves the cursor to the box left or right of the current
// one on the same rendered row.
//
// At the end of a row it wraps to the next row, which keeps h/l a complete
// traversal — otherwise a box that is alone on its row would be a dead end
// in both directions.
func (m *tuiModel) moveHorizontal(dir int) {
	if m.focus != focusTasks {
		return
	}
	if !m.boxMode {
		m.taskCur += dir
		m.clampCursors()
		return
	}

	positions := m.boxPositions()
	cur, ok := posOf(positions, m.taskCur)
	if !ok {
		m.taskCur += dir
		m.clampCursors()
		return
	}

	// Nearest box on the same row in the chosen direction.
	best, bestDist := -1, 0
	for _, p := range positions {
		if p.top != cur.top || p.node == cur.node {
			continue
		}
		d := p.x0 - cur.x0
		if dir > 0 && d <= 0 {
			continue
		}
		if dir < 0 && d >= 0 {
			continue
		}
		if d < 0 {
			d = -d
		}
		if best == -1 || d < bestDist {
			best, bestDist = p.node, d
		}
	}
	if best >= 0 {
		m.taskCur = best
		m.clampCursors()
		return
	}

	// Row exhausted: continue into the next one, entering from the side
	// you left. Reading order, so a full sweep of l visits every box.
	order := m.boxNodeOrder()
	at := -1
	for i, n := range order {
		if n == m.taskCur {
			at = i
			break
		}
	}
	if at < 0 {
		return
	}
	next := at + dir
	if next < 0 || next >= len(order) {
		return
	}
	m.taskCur = order[next]
	m.clampCursors()
}
