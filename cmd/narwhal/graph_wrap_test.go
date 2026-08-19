package main

import (
	"strings"
	"testing"

	"github.com/keyolk/narwhal/internal/broker"
)

// Run s1786889993427-3 is a flat fan: task-1, task-2 and task-3 are all
// roots, and task-4 depends on all three. At a pane wide enough for three
// boxes the diagram says exactly that. Narrow the pane and it stops being
// true — task-3 wraps to its own row under task-1 and task-2, and the edge
// carrying the first row's bar down to task-4 detours around it, drawing a
// line that enters task-3's row on the left and leaves on the right. It
// reads as "task-3 is a child of task-1", which is a different graph.
//
// The screenshot that started this: task-1's edge runs out to the left
// margin and back, task-3 sits under it looking like its child, and the
// drop into task-4 is a lone ┌ connected to nothing.

func wrapModel(t *testing.T) tuiModel {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	m := testModel(0, 0)
	m.boxMode = true
	m.width, m.height = 46, 40
	m.snap.RunID = "r1"
	m.snap.State = broker.RunActive
	m.snap.Tasks = []broker.TaskSnapshot{
		{ID: "task-1", State: broker.TaskCompleted},
		{ID: "task-2", State: broker.TaskCompleted},
		{ID: "task-3", State: broker.TaskCompleted},
		{ID: "task-4", State: broker.TaskCompleted,
			Deps: []string{"task-1", "task-2", "task-3"}},
	}
	return m
}

// boxRowsOf returns the rendered lines, and the index of the line holding
// the named box.
func rowOfBox(lines []string, id string) int {
	for i, l := range lines {
		if strings.Contains(l, id) {
			return i
		}
	}
	return -1
}

func TestADetourDoesNotWrapItselfAroundABox(t *testing.T) {
	// The screenshot's first lie. When the layer wraps, the edge carrying
	// the first row's bar down to task-4 cannot use the child's column —
	// task-3 is sitting in it — so it detours to the margin. The detour
	// runs down the left edge past task-3 and turns back in underneath it,
	// enclosing the box on three sides:
	//
	//	┌────────────────────────┘
	//	│           ┌──────────┐
	//	│           │  task-3  │
	//	└──────────────────────┘
	//
	// task-3 is a root. Drawn like this it reads as task-1's child, which
	// is a different graph from the one on disk.
	m := wrapModel(t)
	for _, width := range []int{46, 40, 36, 34, 30} {
		lines := strings.Split(stripEscapes(m.viewTasks(width, 30)), "\n")
		row := rowOfBox(lines, "task-3")
		if row < 1 {
			continue
		}
		// The box's own left border, on the line holding its label.
		runes := []rune(lines[row])
		border := -1
		for x, r := range runes {
			if r == '│' {
				border = x
				break
			}
		}
		if border < 0 {
			continue
		}
		// Anything vertical to the left of the leftmost box border on this
		// row is an edge running past the box rather than into it. On the
		// wide layout task-3 is not the leftmost box, so this only fires
		// where an edge really does pass outside everything.
		leftmost := border
		for _, l := range []string{lines[row]} {
			for x, r := range []rune(l) {
				if r == '│' && x < leftmost {
					leftmost = x
				}
			}
		}
		firstBoxCol := strings.IndexAny(lines[row-1], "┌")
		if firstBoxCol >= 0 && leftmost < firstBoxCol {
			t.Errorf("width=%d: an edge runs down the margin past task-3 "+
				"(col %d, outside the first box at %d):\n%s",
				width, leftmost, firstBoxCol, strings.Join(lines, "\n"))
		}
	}
}

func TestNoConnectorIsLeftDanglingWhenTheGraphWraps(t *testing.T) {
	// The other half of the screenshot: the drop into task-4 was a single
	// ┌ with nothing above or beside it. Every corner must continue
	// somewhere.
	m := wrapModel(t)
	for _, width := range []int{46, 40, 36, 34, 30} {
		lines := strings.Split(stripEscapes(m.viewTasks(width, 30)), "\n")
		for y, l := range lines {
			runes := []rune(l)
			for x, r := range runes {
				if r != '┌' {
					continue
				}
				// A ┌ turns down and to the right, so both have to exist.
				rightOK := x+1 < len(runes) && runes[x+1] != ' '
				downOK := false
				if y+1 < len(lines) {
					below := []rune(lines[y+1])
					downOK = x < len(below) && below[x] != ' '
				}
				if !rightOK || !downOK {
					t.Errorf("width=%d: dangling ┌ at row %d col %d "+
						"(right=%v down=%v):\n%s",
						width, y, x, rightOK, downOK, strings.Join(lines, "\n"))
				}
			}
		}
	}
}

func TestTheWideLayoutStillPutsThreeRootsSideBySide(t *testing.T) {
	// The regression guard: whatever fixes the narrow case must not change
	// the wide one, which is already correct.
	m := wrapModel(t)
	lines := strings.Split(stripEscapes(m.viewTasks(46, 30)), "\n")
	r1, r2, r3 := rowOfBox(lines, "task-1"), rowOfBox(lines, "task-2"), rowOfBox(lines, "task-3")
	if r1 != r2 || r2 != r3 {
		t.Errorf("the three roots are on rows %d/%d/%d at a width that fits them:\n%s",
			r1, r2, r3, strings.Join(lines, "\n"))
	}
	if r4 := rowOfBox(lines, "task-4"); r4 <= r1 {
		t.Errorf("task-4 is on row %d, not below its parents on %d", r4, r1)
	}
}

// junctionArms is which directions each junction rune claims to continue
// in: left, right, up, down.
var junctionArms = map[rune][4]bool{
	'┌': {false, true, false, true},
	'┐': {true, false, false, true},
	'└': {false, true, true, false},
	'┘': {true, false, true, false},
	'├': {false, true, true, true},
	'┤': {true, false, true, true},
	'┬': {true, true, false, true},
	'┴': {true, true, true, false},
	'┼': {true, true, true, true},
}

func TestEveryJunctionContinuesInTheDirectionsItClaims(t *testing.T) {
	// The general form of the dangling ┌. A junction that draws an arm
	// with nothing at the end of it is a line that stops in mid-air, and
	// two junctions back to back where one line should be — the ┐┘ pair
	// that made one bar read as two diverging lines — is the same defect
	// seen from the other side.
	//
	// Box borders are excluded: their corners are the same runes, and
	// their arms end at the box rather than continuing.
	m := wrapModel(t)
	for _, width := range []int{46, 40, 36, 34, 30} {
		lines := strings.Split(stripEscapes(m.viewTasks(width, 30)), "\n")
		boxRow := map[int]bool{}
		for y, l := range lines {
			if strings.ContainsAny(l, "─") && strings.Contains(l, "┌") &&
				strings.Contains(l, "┐") {
				boxRow[y] = true // a box's top border
			}
			if strings.Contains(l, "└") && strings.Contains(l, "┘") &&
				!strings.Contains(l, "│") {
				boxRow[y] = true // and its bottom
			}
		}
		for y, l := range lines {
			if boxRow[y] {
				continue
			}
			runes := []rune(l)
			for x, r := range runes {
				arms, ok := junctionArms[r]
				if !ok {
					continue
				}
				ink := func(dx, dy int) bool {
					ny, nx := y+dy, x+dx
					if ny < 0 || ny >= len(lines) {
						return false
					}
					row := []rune(lines[ny])
					return nx >= 0 && nx < len(row) && row[nx] != ' '
				}
				for i, dir := range [4][2]int{{-1, 0}, {1, 0}, {0, -1}, {0, 1}} {
					if arms[i] && !ink(dir[0], dir[1]) {
						t.Errorf("width=%d: %q at row %d col %d has an arm "+
							"going nowhere:\n%s", width, string(r), y, x,
							strings.Join(lines, "\n"))
					}
				}
			}
		}
	}
}

func TestADetourIsNotDrawnFlushAgainstABox(t *testing.T) {
	// A vertical run laid against a box border puts two verticals side by
	// side, and at that point the wall and the wire look the same.
	m := wrapModel(t)
	for _, width := range []int{46, 40, 36, 34, 30} {
		lines := strings.Split(stripEscapes(m.viewTasks(width, 30)), "\n")
		for y, l := range lines {
			runes := []rune(l)
			for x := 0; x+1 < len(runes); x++ {
				if runes[x] == '│' && runes[x+1] == '│' {
					t.Errorf("width=%d: two verticals side by side at row %d "+
						"col %d:\n%s", width, y, x, strings.Join(lines, "\n"))
				}
			}
		}
	}
}

func TestAParentsDropReachesItsBar(t *testing.T) {
	// The other direction: a junction can have every arm it draws land on
	// ink and still be wrong, by *omitting* one. Where a detour leaves the
	// bar at the same column a parent drops into it, an elbow erases the
	// upward arm — task-2's drop stops one row short and reads as
	// unconnected, even though every line that is drawn goes somewhere.
	//
	// Anchored on the box: the tee in a box's bottom border is a parent
	// dropping out, so whatever is directly beneath it has to claim an arm
	// going back up.
	m := wrapModel(t)
	for _, width := range []int{46, 40, 36, 34, 30} {
		lines := strings.Split(stripEscapes(m.viewTasks(width, 30)), "\n")
		for y, l := range lines {
			// A box's bottom border: corners at both ends, no label.
			if !strings.Contains(l, "└") || !strings.Contains(l, "┘") {
				continue
			}
			for x, r := range []rune(l) {
				if r != '┬' || y+1 >= len(lines) {
					continue
				}
				below := []rune(lines[y+1])
				if x >= len(below) || below[x] == ' ' {
					t.Errorf("width=%d: the drop out of a box at row %d col %d "+
						"goes nowhere:\n%s", width, y, x, strings.Join(lines, "\n"))
					continue
				}
				if arms, ok := junctionArms[below[x]]; ok && !arms[2] {
					t.Errorf("width=%d: %q under a box's drop at row %d col %d "+
						"does not connect upward:\n%s", width,
						string(below[x]), y, x, strings.Join(lines, "\n"))
				}
			}
		}
	}
}
