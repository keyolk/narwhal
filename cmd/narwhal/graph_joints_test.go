package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/keyolk/narwhal/internal/broker"
)

// arms is which sides a box-drawing glyph connects to: up, down, left,
// right.
var arms = map[rune][4]bool{
	'│': {true, true, false, false},
	'─': {false, false, true, true},
	'┌': {false, true, false, true},
	'┐': {false, true, true, false},
	'└': {true, false, false, true},
	'┘': {true, false, true, false},
	'├': {true, true, false, true},
	'┤': {true, true, true, false},
	'┬': {false, true, true, true},
	'┴': {true, false, true, true},
	'┼': {true, true, true, true},
}

// brokenJoints lists every place a line glyph reaches toward a neighbour
// that does not reach back. A drawing with none has no dangling ends and
// no edge that runs into a wall.
func brokenJoints(rows []boxRow) []string {
	grid := make([][]rune, len(rows))
	for y, r := range rows {
		grid[y] = []rune(r.text)
	}
	at := func(x, y int) rune {
		if y < 0 || y >= len(grid) || x < 0 || x >= len(grid[y]) {
			return ' '
		}
		return grid[y][x]
	}
	var out []string
	for y := range grid {
		for x, r := range grid[y] {
			a, ok := arms[r]
			if !ok {
				continue
			}
			check := func(dir string, nx, ny, back int) {
				na, ok := arms[at(nx, ny)]
				if !ok || !na[back] {
					out = append(out, fmt.Sprintf("(%d,%d) %c reaches %s to %q", x, y, r, dir, at(nx, ny)))
				}
			}
			if a[0] {
				check("up", x, y-1, 1)
			}
			if a[1] {
				check("down", x, y+1, 0)
			}
			if a[2] {
				check("left", x-1, y, 3)
			}
			if a[3] {
				check("right", x+1, y, 2)
			}
		}
	}
	return out
}

// The layout from the screenshot: four siblings feeding a synthesis, the
// fourth wrapped onto its own row. The third sibling's edge used to end at
// a plain ─ — its box floated free of the bar — and the wrapped sibling's
// join read as └──────┤┘.
func TestWrappedFanInHasNoBrokenJoints(t *testing.T) {
	tasks := []broker.TaskSnapshot{
		{ID: "api", State: broker.TaskDispatched}, {ID: "client", State: broker.TaskDispatched},
		{ID: "fuzz", State: broker.TaskDispatched}, {ID: "schema", State: broker.TaskDispatched},
		{ID: "synthesis", State: broker.TaskPending, Deps: []string{"api", "client", "fuzz", "schema"}},
	}
	for _, w := range []int{36, 42, 48, 60, 80} {
		rows := layoutGraph(tasks).renderBoxes(w, taskIconPlain)
		if bad := brokenJoints(rows); len(bad) > 0 {
			var b strings.Builder
			for _, r := range rows {
				b.WriteString("|" + r.text + "|\n")
			}
			t.Errorf("width %d: %d broken joints: %v\n%s", w, len(bad), bad, b.String())
		}
	}
}

// Every graph shape the other tests build, at several widths, drawn without
// a dangling end. The shapes are where the router's special cases live.
func TestKnownShapesHaveNoBrokenJoints(t *testing.T) {
	shapes := map[string][]broker.TaskSnapshot{
		"fan":      fanShape(),
		"wide row": wideRowShape(),
		"diamond": {
			{ID: "root"}, {ID: "left", Deps: []string{"root"}}, {ID: "right", Deps: []string{"root"}},
			{ID: "join", Deps: []string{"left", "right"}},
		},
		"chain": {{ID: "a"}, {ID: "b", Deps: []string{"a"}}, {ID: "c", Deps: []string{"b"}}},
		"two fan-ins": {
			{ID: "p1"}, {ID: "p2"}, {ID: "p3"},
			{ID: "c1", Deps: []string{"p1", "p2"}}, {ID: "c2", Deps: []string{"p2", "p3"}},
		},
	}
	for name, tasks := range shapes {
		for _, w := range []int{30, 44, 60, 90} {
			rows := layoutGraph(tasks).renderBoxes(w, taskIconPlain)
			if bad := brokenJoints(rows); len(bad) > 0 {
				var b strings.Builder
				for _, r := range rows {
					b.WriteString("|" + r.text + "|\n")
				}
				t.Errorf("%s at width %d: %v\n%s", name, w, bad, b.String())
			}
		}
	}
}
