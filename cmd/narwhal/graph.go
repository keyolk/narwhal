// graph.go lays out a task DAG for terminal rendering.
//
// The first version flattened the DAG into a tree: each task nested under
// its first dependency, with the rest summarized as "+2". That discards the
// thing the graph exists to show. A synthesis node waiting on three
// investigations is the shape that matters, and a tree cannot draw it.
//
// This assigns every task a column and draws real edges in the gutter to
// its left, the way a git commit graph does. Columns are stable across
// frames, edges are never dropped, and a fan-in reads as a fan-in.
package main

import (
	"sort"
	"strings"

	"github.com/keyolk/narwhal/internal/broker"
)

// graphNode is a task placed in the layout.
type graphNode struct {
	task  broker.TaskSnapshot
	layer int // dependency depth; every dep sits in a strictly lower layer
	col   int // gutter lane, stable across frames
}

// graphLayout is a laid-out DAG ready to render.
type graphLayout struct {
	nodes []graphNode          // render order: by layer, then by id
	index map[string]graphNode // task id → placement
	width int                  // number of gutter lanes in use
}

// layoutGraph assigns each task a layer and a lane.
//
// Layer is the longest path from a root, not the shortest: a task must sit
// below every dependency, so its layer is one past the deepest thing it
// waits on. Tasks in a cycle, or depending on something never created, land
// in layer 0 — visible rather than silently dropped.
//
// Lanes are assigned greedily in render order, reusing a lane as soon as
// the task occupying it has no unrendered dependents. That keeps the gutter
// narrow, which matters because the graph pane is a third of the screen.
func layoutGraph(tasks []broker.TaskSnapshot) graphLayout {
	byID := make(map[string]broker.TaskSnapshot, len(tasks))
	ids := make([]string, 0, len(tasks))
	for _, t := range tasks {
		byID[t.ID] = t
		ids = append(ids, t.ID)
	}
	sort.Strings(ids)

	layerOf := make(map[string]int, len(tasks))
	var resolve func(id string, onPath map[string]bool) int
	resolve = func(id string, onPath map[string]bool) int {
		if l, done := layerOf[id]; done {
			return l
		}
		if onPath[id] {
			return 0 // cycle: stop descending so the layout terminates
		}
		onPath[id] = true
		defer delete(onPath, id)

		best := 0
		for _, dep := range byID[id].Deps {
			if _, exists := byID[dep]; !exists {
				continue // dangling dep adds no depth
			}
			if d := resolve(dep, onPath) + 1; d > best {
				best = d
			}
		}
		layerOf[id] = best
		return best
	}
	for _, id := range ids {
		resolve(id, map[string]bool{})
	}

	// Render order: layer first, then id, so the sequence is deterministic.
	ordered := append([]string(nil), ids...)
	sort.SliceStable(ordered, func(i, j int) bool {
		li, lj := layerOf[ordered[i]], layerOf[ordered[j]]
		if li != lj {
			return li < lj
		}
		return ordered[i] < ordered[j]
	})

	// Lane assignment. A node inherits a lane from one of its dependencies
	// when that dependency has no further dependents — which keeps a chain
	// in one column and stops an edge from being drawn across a neighbour.
	// Only a node with no inheritable lane opens a new one.
	//
	// A lane is occupied while the task holding it still has dependents
	// waiting to be placed; the edge has to stay drawable until then.
	pending := make(map[string]int, len(tasks)) // task → dependents not yet placed
	for _, t := range tasks {
		for _, d := range existingDeps(t.Deps, byID) {
			pending[d]++
		}
	}

	out := graphLayout{index: make(map[string]graphNode, len(tasks))}
	lanes := []string{} // lane → id of the task holding it, "" when free

	release := func() {
		for i, holder := range lanes {
			if holder != "" && pending[holder] == 0 {
				lanes[i] = ""
			}
		}
	}

	for _, id := range ordered {
		deps := existingDeps(byID[id].Deps, byID)
		// Placing this node consumes one pending edge from each dep.
		for _, d := range deps {
			pending[d]--
		}
		release()

		col := -1
		// Prefer a dependency's now-free lane so the edge continues
		// straight down its column instead of jumping sideways.
		for _, d := range deps {
			dep := out.index[d]
			if dep.col < len(lanes) && lanes[dep.col] == "" {
				col = dep.col
				break
			}
		}
		if col == -1 {
			for i, holder := range lanes {
				if holder == "" {
					col = i
					break
				}
			}
		}
		if col == -1 {
			col = len(lanes)
			lanes = append(lanes, "")
		}
		lanes[col] = id

		n := graphNode{task: byID[id], layer: layerOf[id], col: col}
		out.nodes = append(out.nodes, n)
		out.index[id] = n

		if col+1 > out.width {
			out.width = col + 1
		}
	}
	return out
}

// existingDeps filters out dependencies that were never created.
func existingDeps(deps []string, byID map[string]broker.TaskSnapshot) []string {
	out := make([]string, 0, len(deps))
	for _, d := range deps {
		if _, ok := byID[d]; ok {
			out = append(out, d)
		}
	}
	return out
}

// graphRow is one rendered line: the gutter drawing plus the task label.
type graphRow struct {
	gutter string
	label  string
	// node indexes into layout.nodes, so the cursor maps back to a task.
	node int
	// state drives the label color.
	state broker.TaskState
	// dispatches > 1 marks a retry.
	dispatches int
	id         string
}

// Gutter glyphs.
//
// Two columns per lane: one for the node or line, one for spacing or a
// horizontal run. The set is plain box-drawing plus two filled circles —
// all in the Unicode BMP, so it renders in any terminal font without
// needing a patched one. Nerd Font glyphs are used for task state icons
// (see monitor_tui.go), where a missing glyph degrades to a visible box
// rather than a broken graph.
const (
	// glyphNode opens a lane: nothing above it feeds this column.
	glyphNode = "●"
	// glyphNodeOnLine continues the parent's lane, so a chain reads as one
	// unbroken column rather than a column of loose dots.
	glyphNodeOnLine = "◉"
	// glyphVertical carries an edge past this row to a node further down.
	glyphVertical = "│"
	// glyphElbow ends a lane and turns right toward the node. Edges arrive
	// from the left, so the corner has to open right — "┴" pointed the
	// wrong way and read as a merge from below.
	glyphElbow = "╰"
	// glyphTee is an elbow for a lane that also continues downward.
	glyphTee = "├"
	// glyphHoriz is the run joining a fan-in to its node.
	glyphHoriz = "─"
)

// render walks the layout and produces one row per task, drawing the edges
// that arrive at each node in the gutter to its left.
//
// Every edge points downward because layers are ordered, so a lane needs
// only two states: carrying a line through, or terminating here. An edge
// from the node's own lane (a chain, where the child inherited the parent's
// column) is drawn as a continuous vertical run rather than a join, which
// is what makes a linear pipeline read as one column.
func (g graphLayout) render() []graphRow {
	remaining := make(map[string]int, len(g.nodes))
	for _, n := range g.nodes {
		for _, d := range n.task.Deps {
			if _, ok := g.index[d]; ok {
				remaining[d]++
			}
		}
	}
	// active[lane] is true while an edge passes through it on its way to a
	// node further down.
	active := make([]bool, g.width)

	rows := make([]graphRow, 0, len(g.nodes))
	for i, n := range g.nodes {
		incoming := map[int]bool{}
		inherited := false
		for _, d := range n.task.Deps {
			dep, ok := g.index[d]
			if !ok {
				continue
			}
			if dep.col == n.col {
				// Parent and child share a lane: the edge is the column
				// itself, so mark it rather than drawing a join into it.
				inherited = true
				continue
			}
			incoming[dep.col] = true
		}

		// Consume this node's incoming edges before drawing, so a lane that
		// ends here is drawn as an elbow rather than a tee. Deciding from
		// the pre-consumption state made every fan-in lane look like it
		// continued downward.
		for _, d := range n.task.Deps {
			if dep, ok := g.index[d]; ok {
				remaining[d]--
				if remaining[d] == 0 && dep.col != n.col {
					active[dep.col] = false
				}
			}
		}
		// The node's own lane stays live only if something below needs it.
		active[n.col] = remaining[n.task.ID] > 0

		rows = append(rows, graphRow{
			gutter:     drawGutter(g.width, n.col, incoming, active, inherited),
			label:      n.task.ID,
			node:       i,
			state:      n.task.State,
			dispatches: n.task.Dispatches,
			id:         n.task.ID,
		})
	}
	return rows
}

// drawGutter renders the lane columns for one row.
//
// nodeCol is where the node sits. incoming marks lanes whose edge
// terminates here. active marks lanes carrying a line straight through.
// inherited says the node continues its parent's own lane.
//
// Edges always arrive from a lane to the left or right of the node and turn
// toward it, so a terminating lane draws a corner that opens in the node's
// direction, connected by a horizontal run. Using "┴" here read as a merge
// arriving from below, which is the one direction that cannot happen.
func drawGutter(width, nodeCol int, incoming map[int]bool, active []bool, inherited bool) string {
	if width == 0 {
		return ""
	}
	// The horizontal run spans from the outermost incoming lane to the node.
	lo, hi := nodeCol, nodeCol
	for c := range incoming {
		if c < lo {
			lo = c
		}
		if c > hi {
			hi = c
		}
	}

	var b strings.Builder
	for c := 0; c < width; c++ {
		switch {
		case c == nodeCol:
			if inherited {
				b.WriteString(glyphNodeOnLine)
			} else {
				b.WriteString(glyphNode)
			}
		case incoming[c]:
			// A lane still carrying edges to later rows keeps its trunk,
			// so it branches rather than ends.
			if active[c] {
				b.WriteString(glyphTee)
			} else {
				b.WriteString(glyphElbow)
			}
		case active[c]:
			b.WriteString(glyphVertical)
		case c > lo && c < hi:
			// An untouched lane the run passes over.
			b.WriteString(glyphHoriz)
		default:
			b.WriteString(" ")
		}
		// Spacer column, filled when it lies inside the horizontal run.
		if c < width-1 {
			if c >= lo && c < hi {
				b.WriteString(glyphHoriz)
			} else {
				b.WriteString(" ")
			}
		}
	}
	return b.String()
}
