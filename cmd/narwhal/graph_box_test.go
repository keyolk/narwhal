package main

import (
	"strings"
	"testing"

	"github.com/keyolk/narwhal/internal/broker"
)

// renderArt returns the drawn diagram as lines, for asserting on shape.
func renderArt(width int, tasks []broker.TaskSnapshot) []string {
	var out []string
	for _, r := range layoutGraph(tasks).renderBoxes(width, taskIconPlain) {
		out = append(out, r.text)
	}
	return out
}

func TestBoxesAreSizedToContent(t *testing.T) {
	// Full-width boxes read as a bordered list, not a diagram. A box should
	// be as wide as its label and no wider.
	lines := renderArt(60, []broker.TaskSnapshot{
		{ID: "api", State: broker.TaskReady},
	})
	var top string
	for _, l := range lines {
		if strings.Contains(l, boxTopLeft) {
			top = strings.TrimSpace(l)
			break
		}
	}
	if top == "" {
		t.Fatalf("no box drawn:\n%s", strings.Join(lines, "\n"))
	}
	if w := displayWidth(top); w > 20 {
		t.Fatalf("box is %d wide for a 3-character label: %q", w, top)
	}
}

func TestBoxesAreCentered(t *testing.T) {
	// Margins on both sides are what give edges room to route.
	lines := renderArt(60, []broker.TaskSnapshot{
		{ID: "api", State: broker.TaskReady},
	})
	for _, l := range lines {
		if strings.Contains(l, boxTopLeft) {
			if !strings.HasPrefix(l, " ") {
				t.Fatalf("box hugs the left edge: %q", l)
			}
			return
		}
	}
	t.Fatal("no box drawn")
}

func TestChainConnectsWithTees(t *testing.T) {
	lines := renderArt(46, []broker.TaskSnapshot{
		{ID: "auth", State: broker.TaskCompleted},
		{ID: "api", State: broker.TaskReady, Deps: []string{"auth"}},
	})
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, jTeeDown) {
		t.Fatalf("edge should leave the parent through a tee:\n%s", joined)
	}
	if !strings.Contains(joined, jTeeUp) {
		t.Fatalf("edge should enter the child through a tee:\n%s", joined)
	}
}

// routingBars counts lines that carry a horizontal run but are not part of
// a box. Box lines are identified by their part tag rather than by their
// glyphs, since a routing bar legitimately contains corners that look like
// box corners.
func routingBars(rows []boxRow) int {
	n := 0
	for _, r := range rows {
		if r.part != partRoute {
			continue
		}
		if strings.Count(r.text, boxHoriz) > 2 {
			n++
		}
	}
	return n
}

func TestFanOutSharesOneBar(t *testing.T) {
	// Routing each arm separately made them overwrite each other; a fan-out
	// is one shape and should draw one horizontal bar.
	rows := layoutGraph([]broker.TaskSnapshot{
		{ID: "root", State: broker.TaskCompleted},
		{ID: "api", State: broker.TaskReady, Deps: []string{"root"}},
		{ID: "utils", State: broker.TaskReady, Deps: []string{"root"}},
	}).renderBoxes(46, taskIconPlain)

	if got := routingBars(rows); got != 1 {
		t.Fatalf("expected exactly one routing bar, got %d:\n%s",
			got, joinRows(rows))
	}
}

func TestFanInSharesOneBar(t *testing.T) {
	rows := layoutGraph([]broker.TaskSnapshot{
		{ID: "auth", State: broker.TaskCompleted},
		{ID: "api", State: broker.TaskCompleted},
		{ID: "utils", State: broker.TaskCompleted},
		{ID: "sink", State: broker.TaskReady, Deps: []string{"auth", "api", "utils"}},
	}).renderBoxes(60, taskIconPlain)

	if got := routingBars(rows); got != 1 {
		t.Fatalf("expected one shared bar for the fan-in, got %d:\n%s",
			got, joinRows(rows))
	}
}

func joinRows(rows []boxRow) string {
	var b strings.Builder
	for _, r := range rows {
		b.WriteString(r.text)
		b.WriteString("\n")
	}
	return b.String()
}

func TestEdgesDoNotCrossBoxes(t *testing.T) {
	// A layer too wide for the pane wraps onto extra rows. Routing straight
	// down the child's column then cuts through those boxes, so the run has
	// to detour through the margin.
	tasks := []broker.TaskSnapshot{
		{ID: "anthropic-path", State: broker.TaskCompleted},
		{ID: "mixed-path", State: broker.TaskCompleted},
		{ID: "session-identity", State: broker.TaskCompleted},
		{ID: "synthesis", State: broker.TaskReady,
			Deps: []string{"anthropic-path", "mixed-path", "session-identity"}},
	}
	rows := layoutGraph(tasks).renderBoxes(46, taskIconPlain)

	// Only the body line of a box is checked: its interior must hold the
	// label and nothing else. Border lines legitimately carry tees, and a
	// detour may share a line with a border without touching the box.
	for _, r := range rows {
		if r.part != partBody || r.node < 0 {
			continue
		}
		first := strings.Index(r.text, boxVert)
		last := strings.LastIndex(r.text, boxVert)
		if first < 0 || last <= first {
			continue
		}
		inner := r.text[first+len(boxVert) : last]
		for _, bad := range []string{boxHoriz, jTeeUp, jTeeDown, jCross} {
			if strings.Contains(inner, bad) {
				t.Fatalf("routing crosses a box body (%q in %q)", bad, r.text)
			}
		}
	}
}

func TestRowsCarryTaskIdentity(t *testing.T) {
	rows := layoutGraph([]broker.TaskSnapshot{
		{ID: "solo", State: broker.TaskCompleted},
	}).renderBoxes(40, taskIconPlain)

	var top, body, bottom bool
	for _, r := range rows {
		if r.node != 0 {
			continue
		}
		switch r.part {
		case partTop:
			top = true
		case partBody:
			body = true
		case partBottom:
			bottom = true
		}
	}
	if !top || !body || !bottom {
		t.Fatalf("all three box lines should map to the task: top=%v body=%v bottom=%v",
			top, body, bottom)
	}
}

func TestRetryBadgeIsShown(t *testing.T) {
	lines := renderArt(46, []broker.TaskSnapshot{
		{ID: "flaky", State: broker.TaskDispatched, Dispatches: 3},
	})
	if !strings.Contains(strings.Join(lines, "\n"), "×3") {
		t.Fatalf("retry count should be visible:\n%s", strings.Join(lines, "\n"))
	}
}

func TestNarrowPaneDoesNotOverflow(t *testing.T) {
	// The pane is a third of the screen; a long id must truncate rather
	// than push the diagram past the edge.
	const width = 24
	lines := renderArt(width, []broker.TaskSnapshot{
		{ID: "a-very-long-task-identifier-that-will-not-fit", State: broker.TaskReady},
	})
	for _, l := range lines {
		if w := displayWidth(l); w > width {
			t.Fatalf("line exceeds pane width %d: %d columns %q", width, w, l)
		}
	}
}

func TestEmptyGraphDrawsNothing(t *testing.T) {
	if rows := layoutGraph(nil).renderBoxes(40, taskIconPlain); len(rows) != 0 {
		t.Fatalf("empty graph produced %d rows", len(rows))
	}
}

func TestNoTrailingBlankLines(t *testing.T) {
	lines := renderArt(46, []broker.TaskSnapshot{
		{ID: "auth", State: broker.TaskCompleted},
		{ID: "api", State: broker.TaskReady, Deps: []string{"auth"}},
	})
	if len(lines) == 0 {
		t.Fatal("no output")
	}
	if strings.TrimSpace(lines[len(lines)-1]) == "" {
		t.Fatalf("diagram ends with dead space:\n%s", strings.Join(lines, "\n"))
	}
}

func TestMergeGlyphProducesCrossings(t *testing.T) {
	if got := mergeGlyph('─', '│'); got != '┼' {
		t.Fatalf("crossing lines should merge to ┼, got %q", got)
	}
	if got := mergeGlyph(' ', '│'); got != '│' {
		t.Fatalf("drawing onto blank space should keep the new glyph, got %q", got)
	}
	if got := mergeGlyph('│', '│'); got != '│' {
		t.Fatalf("identical glyphs should stay put, got %q", got)
	}
}
