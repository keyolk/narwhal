package main

import (
	"strings"
	"testing"

	"github.com/keyolk/narwhal/internal/broker"
)

// renderPlain returns the gutter+label lines, for asserting on shape.
func renderPlain(tasks []broker.TaskSnapshot) []string {
	var out []string
	for _, r := range layoutGraph(tasks).render() {
		out = append(out, r.gutter+" "+r.label)
	}
	return out
}

func TestLayoutAssignsDependencyLayers(t *testing.T) {
	g := layoutGraph([]broker.TaskSnapshot{
		{ID: "a"},
		{ID: "b", Deps: []string{"a"}},
		{ID: "c", Deps: []string{"b"}},
	})
	if g.index["a"].layer != 0 {
		t.Fatalf("a layer = %d, want 0", g.index["a"].layer)
	}
	if g.index["b"].layer != 1 {
		t.Fatalf("b layer = %d, want 1", g.index["b"].layer)
	}
	if g.index["c"].layer != 2 {
		t.Fatalf("c layer = %d, want 2", g.index["c"].layer)
	}
}

func TestLayoutUsesLongestPathNotShortest(t *testing.T) {
	// d waits on both a (depth 0) and c (depth 2), so it must sit at 3.
	// Using the shortest path would place it at 1 and draw an edge pointing
	// upward, which the renderer cannot express.
	g := layoutGraph([]broker.TaskSnapshot{
		{ID: "a"},
		{ID: "b", Deps: []string{"a"}},
		{ID: "c", Deps: []string{"b"}},
		{ID: "d", Deps: []string{"a", "c"}},
	})
	if got := g.index["d"].layer; got != 3 {
		t.Fatalf("d layer = %d, want 3 (one past its deepest dep)", got)
	}
}

func TestLayoutKeepsChainInOneLane(t *testing.T) {
	// A linear pipeline should read as a single column, not a staircase.
	g := layoutGraph([]broker.TaskSnapshot{
		{ID: "s1"},
		{ID: "s2", Deps: []string{"s1"}},
		{ID: "s3", Deps: []string{"s2"}},
	})
	if g.width != 1 {
		t.Fatalf("chain used %d lanes, want 1", g.width)
	}
	for _, id := range []string{"s1", "s2", "s3"} {
		if g.index[id].col != 0 {
			t.Fatalf("%s is in lane %d, want 0", id, g.index[id].col)
		}
	}
}

func TestChainDrawsContinuousColumn(t *testing.T) {
	lines := renderPlain([]broker.TaskSnapshot{
		{ID: "s1"},
		{ID: "s2", Deps: []string{"s1"}},
	})
	// The second row must show the on-line node glyph, marking that it
	// continues its parent's lane rather than starting a fresh one.
	if !strings.Contains(lines[1], glyphNodeOnLine) {
		t.Fatalf("chained node should continue the lane: %q", lines[1])
	}
}

func TestFanInDrawsEveryEdge(t *testing.T) {
	// The bug the tree layout had: a node waiting on three parents showed
	// one edge and summarized the rest as "+2".
	lines := renderPlain([]broker.TaskSnapshot{
		{ID: "a"},
		{ID: "b"},
		{ID: "c"},
		{ID: "sink", Deps: []string{"a", "b", "c"}},
	})
	sink := lines[len(lines)-1]
	joins := strings.Count(sink, glyphElbow) + strings.Count(sink, glyphTee)
	if joins < 2 {
		t.Fatalf("fan-in row should turn every incoming lane toward the node, got %d in %q", joins, sink)
	}
	if !strings.Contains(sink, glyphHoriz) {
		t.Fatalf("fan-in row should draw a horizontal run: %q", sink)
	}
}

func TestIndependentTasksShareOneLane(t *testing.T) {
	// Nothing depends on anything, so lanes can be recycled freely.
	g := layoutGraph([]broker.TaskSnapshot{
		{ID: "one"}, {ID: "two"}, {ID: "three"},
	})
	if g.width != 1 {
		t.Fatalf("independent tasks used %d lanes, want 1", g.width)
	}
}

func TestDiamondKeepsBothBranchesVisible(t *testing.T) {
	g := layoutGraph([]broker.TaskSnapshot{
		{ID: "a"},
		{ID: "b", Deps: []string{"a"}},
		{ID: "c", Deps: []string{"a"}},
		{ID: "d", Deps: []string{"b", "c"}},
	})
	if g.width < 2 {
		t.Fatalf("diamond needs at least 2 lanes, got %d", g.width)
	}
	if g.index["b"].col == g.index["c"].col {
		t.Fatal("parallel branches must not share a lane")
	}
}

func TestLayoutSurvivesCycle(t *testing.T) {
	// A cycle has no root. Every node must still be placed exactly once
	// rather than sending the layout into infinite recursion.
	g := layoutGraph([]broker.TaskSnapshot{
		{ID: "x", Deps: []string{"y"}},
		{ID: "y", Deps: []string{"x"}},
	})
	if len(g.nodes) != 2 {
		t.Fatalf("cycle produced %d nodes, want 2", len(g.nodes))
	}
	seen := map[string]int{}
	for _, n := range g.nodes {
		seen[n.task.ID]++
	}
	for id, n := range seen {
		if n != 1 {
			t.Fatalf("%s placed %d times", id, n)
		}
	}
}

func TestLayoutIgnoresDanglingDeps(t *testing.T) {
	// A dep that was never created must not add depth or hide the task.
	g := layoutGraph([]broker.TaskSnapshot{
		{ID: "orphan", Deps: []string{"never-created"}},
	})
	if len(g.nodes) != 1 {
		t.Fatalf("nodes = %d, want 1", len(g.nodes))
	}
	if g.index["orphan"].layer != 0 {
		t.Fatalf("dangling dep should not add depth, layer = %d", g.index["orphan"].layer)
	}
}

func TestRenderIsDeterministic(t *testing.T) {
	// The broker stores tasks in a map; unstable output would make the
	// cursor jump between polls.
	tasks := []broker.TaskSnapshot{
		{ID: "b", Deps: []string{"a"}},
		{ID: "a"},
		{ID: "c", Deps: []string{"a"}},
	}
	first := strings.Join(renderPlain(tasks), "\n")
	for i := 0; i < 20; i++ {
		if got := strings.Join(renderPlain(tasks), "\n"); got != first {
			t.Fatalf("layout not deterministic:\n%s\n---\n%s", first, got)
		}
	}
}

func TestRenderRowsCarryTaskIdentity(t *testing.T) {
	rows := layoutGraph([]broker.TaskSnapshot{
		{ID: "a", State: broker.TaskCompleted, Dispatches: 2},
		{ID: "b", Deps: []string{"a"}, State: broker.TaskPending},
	}).render()

	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	if rows[0].id != "a" || rows[0].state != broker.TaskCompleted || rows[0].dispatches != 2 {
		t.Fatalf("row 0 lost task metadata: %+v", rows[0])
	}
	if rows[1].node != 1 {
		t.Fatalf("row 1 node index = %d, want 1", rows[1].node)
	}
}

func TestEmptyGraphRenders(t *testing.T) {
	if rows := layoutGraph(nil).render(); len(rows) != 0 {
		t.Fatalf("empty graph produced %d rows", len(rows))
	}
}

func TestLaneEndingHereDrawsElbowNotTee(t *testing.T) {
	// A tee says "this lane continues below". Deciding that from the state
	// before the node consumed its incoming edges made every fan-in lane
	// look like it kept going.
	lines := renderPlain([]broker.TaskSnapshot{
		{ID: "a"},
		{ID: "b"},
		{ID: "sink", Deps: []string{"a", "b"}},
	})
	sink := lines[len(lines)-1]
	if strings.Contains(sink, glyphTee) {
		t.Fatalf("lanes ending at the sink must not draw a tee: %q", sink)
	}
	if !strings.Contains(sink, glyphElbow) {
		t.Fatalf("a terminating lane should draw an elbow: %q", sink)
	}
}

func TestLaneContinuingDrawsTee(t *testing.T) {
	// a feeds both b and d, so at b's row lane 0 still has work below it.
	lines := renderPlain([]broker.TaskSnapshot{
		{ID: "a"},
		{ID: "b", Deps: []string{"a"}},
		{ID: "c", Deps: []string{"a"}},
	})
	// One of the two dependent rows must show the lane still branching.
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, glyphTee) {
		t.Fatalf("a lane with more dependents below should tee:\n%s", joined)
	}
}

func TestIconSetOverride(t *testing.T) {
	t.Setenv("NARWHAL_ICONS", "nerd")
	if got := resolveIcons(); got != nerdIcons {
		t.Fatal("NARWHAL_ICONS=nerd should select the Nerd Font set")
	}
	t.Setenv("NARWHAL_ICONS", "unicode")
	if got := resolveIcons(); got != unicodeIcons {
		t.Fatal("NARWHAL_ICONS=unicode should select the portable set")
	}
	// An explicit override must win over terminal detection.
	t.Setenv("TERM_PROGRAM", "ghostty")
	if got := resolveIcons(); got != unicodeIcons {
		t.Fatal("explicit override should beat terminal detection")
	}
}

func TestIconSetDefaultsToUnicode(t *testing.T) {
	// Guessing wrong toward Nerd Font fills the pane with tofu, so an
	// unknown terminal must fall back to portable glyphs.
	t.Setenv("NARWHAL_ICONS", "")
	t.Setenv("TERM_PROGRAM", "some-unknown-terminal")
	t.Setenv("NARWHAL_FONT", "")
	t.Setenv("TERM_FONT", "")
	if got := resolveIcons(); got != unicodeIcons {
		t.Fatal("unknown terminal should default to the portable set")
	}
}

func TestNerdFontDetectedFromFontName(t *testing.T) {
	t.Setenv("NARWHAL_ICONS", "")
	t.Setenv("TERM_PROGRAM", "")
	t.Setenv("NARWHAL_FONT", "JetBrainsMono Nerd Font")
	if got := resolveIcons(); got != nerdIcons {
		t.Fatal("a font name containing 'nerd' should select the Nerd Font set")
	}
}
