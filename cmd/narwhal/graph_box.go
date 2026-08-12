// graph_box.go renders a laid-out DAG as connected boxes.
//
// The lane renderer in graph.go draws a git-style gutter: compact, but a
// task is a dot with loose text beside it, so it never reads as a diagram.
// Tools built for this — Graph::Easy, dgraph, boxart — draw nodes as
// rectangles joined by Manhattan-routed edges, which suits a dependency
// graph much better.
//
// Two things here depart from those tools, both forced by the pane:
//
//   - Direction is top-down, not left-to-right. The graph pane is a third
//     of the screen and a run can hold dozens of tasks, so the view has to
//     scroll vertically.
//   - One box per row. Placing siblings side by side (what a real Sugiyama
//     layout does) needs roughly 25 columns per box; two of them do not fit
//     in a third of an 80-column terminal. Depth is shown by indentation
//     and the edge gutter instead, so width stays bounded no matter how
//     wide the graph gets.
//
//	╭─ anthropic-path ────╮
//	│ ✓ completed         │
//	╰─────────┬───────────╯
//	          ├──────────────╮
//	╭─────────┴───────────╮  │
//	│ ▶ mixed-path     ×2 │  │
//	╰─────────────────────╯  │
package main

import (
	"strconv"
	"strings"

	"github.com/keyolk/narwhal/internal/broker"
)

// Box-drawing set. Rounded corners suit a status view; the selected box
// switches to heavy lines so the cursor is unmistakable without color.
const (
	boxTopLeft     = "╭"
	boxTopRight    = "╮"
	boxBottomLeft  = "╰"
	boxBottomRight = "╯"
	boxHoriz       = "─"
	boxVert        = "│"

	boxSelTopLeft     = "┏"
	boxSelTopRight    = "┓"
	boxSelBottomLeft  = "┗"
	boxSelBottomRight = "┛"
	boxSelHoriz       = "━"
	boxSelVert        = "┃"

	// Gutter glyphs for edges running alongside the boxes.
	gutVert   = "│"
	gutTeeIn  = "├" // an edge leaving a trunk toward a box
	gutElbow  = "╰" // the last edge off a trunk
	gutHoriz  = "─"
	gutJoinUp = "┴" // an edge arriving at a box below
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

// boxRow is one rendered line. Rows belonging to a task carry its index in
// layout order; pure routing rows carry -1.
type boxRow struct {
	text string
	node int
	part boxPart
	// depth drives indentation, mirroring the task's dependency layer.
	depth int
}

// renderBoxes draws the layout as a vertical stack of boxes.
//
// width is the pane width; boxes size themselves to it so a narrow pane
// degrades by truncating labels rather than by wrapping or overflowing.
func (g graphLayout) renderBoxes(width int, iconFor func(broker.TaskState) (string, string)) []boxRow {
	if len(g.nodes) == 0 {
		return nil
	}

	// Depth is capped so a long chain does not indent itself off-screen.
	const indentStep = 2
	maxIndent := width / 3
	indentFor := func(layer int) int {
		if n := layer * indentStep; n < maxIndent {
			return n
		}
		return maxIndent
	}

	// Which tasks still owe an edge to something further down? Their trunk
	// keeps running in the gutter past intervening rows.
	remaining := map[string]int{}
	for _, n := range g.nodes {
		for _, d := range n.task.Deps {
			if _, ok := g.index[d]; ok {
				remaining[d]++
			}
		}
	}

	// tapColumn is where an edge meets a box: two columns in from the box's
	// left border, which is where the top-border tee is drawn.
	const tapColumn = 2

	var rows []boxRow
	for i, n := range g.nodes {
		indent := indentFor(n.layer)
		inner := width - indent - 2 // minus the two border columns
		if inner < 8 {
			inner = 8
		}

		icon, _ := iconFor(n.task.State)
		label := n.task.ID
		badge := ""
		if n.task.Dispatches > 1 {
			badge = "×" + strconv.Itoa(n.task.Dispatches)
		}

		pad := strings.Repeat(" ", indent)
		deps := existingDepIDs(n.task.Deps, g.index)

		// An incoming edge lands as a tee in the top border, so the
		// connection touches the box instead of running past it.
		top := boxTopLeft + strings.Repeat(boxHoriz, inner) + boxTopRight
		if len(deps) > 0 {
			top = boxTopLeft + boxHoriz + gutJoinUp +
				strings.Repeat(boxHoriz, inner-2) + boxTopRight
		}

		rows = append(rows,
			boxRow{text: pad + top, node: i, part: partTop, depth: n.layer},
			boxRow{text: pad + boxVert + fitBody(icon, label, badge, inner) + boxVert,
				node: i, part: partBody, depth: n.layer},
			boxRow{text: pad + boxBottomLeft + strings.Repeat(boxHoriz, inner) + boxBottomRight,
				node: i, part: partBottom, depth: n.layer},
		)

		// Connector down to the next box. It has to line up with that
		// box's tap column, not this one's, or the line lands beside the
		// tee instead of on it.
		if remaining[n.task.ID] > 0 && i+1 < len(g.nodes) {
			nextIndent := indentFor(g.nodes[i+1].layer)
			rows = append(rows, boxRow{
				text:  strings.Repeat(" ", nextIndent+tapColumn) + gutVert,
				node:  -1,
				part:  partRoute,
				depth: n.layer,
			})
		}
		for _, d := range deps {
			remaining[d]--
		}
	}
	return rows
}

// fitBody lays out the interior of a box: icon and label on the left, the
// retry badge right-aligned, truncating the label when space runs out.
func fitBody(icon, label, badge string, inner int) string {
	left := " " + icon + " " + label
	if badge == "" {
		return padRight(truncate(left, inner), inner)
	}
	room := inner - displayWidth(badge) - 1
	if room < 4 {
		return padRight(truncate(left, inner), inner)
	}
	left = truncate(left, room)
	gap := inner - displayWidth(left) - displayWidth(badge) - 1
	if gap < 1 {
		gap = 1
	}
	return left + strings.Repeat(" ", gap) + badge + " "
}

// existingDepIDs filters dependencies down to tasks that were created.
func existingDepIDs(deps []string, index map[string]graphNode) []string {
	out := make([]string, 0, len(deps))
	for _, d := range deps {
		if _, ok := index[d]; ok {
			out = append(out, d)
		}
	}
	return out
}
