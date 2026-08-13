package main

import (
	"testing"

	"github.com/keyolk/narwhal/internal/broker"
)

// navModel is a model showing siblingTasks in the box view with the graph
// focused — the shape where horizontal movement has somewhere to go.
func navModel(t *testing.T) tuiModel {
	t.Helper()
	m := testModel(0, 0)
	m.width, m.height = 100, 24
	m.focus = focusTasks
	m.boxMode = true
	m.snap.Tasks = siblingTasks()
	return m
}

func TestHorizontalKeysNavigateTheGraph(t *testing.T) {
	// Regression: h backed out to the run list, so the graph had no
	// horizontal navigation at all despite being drawn two-dimensionally.
	m := navModel(t)
	m.taskCur = 0

	m = press(m, "l")
	if m.picker {
		t.Fatal("l left the graph for the run list")
	}
	if m.taskCur != 1 {
		t.Fatalf("l should move to the next box, taskCur = %d, want 1", m.taskCur)
	}

	m = press(m, "h")
	if m.picker {
		t.Fatal("h backed out to the run list instead of moving in the graph")
	}
	if m.taskCur != 0 {
		t.Fatalf("h should move back, taskCur = %d, want 0", m.taskCur)
	}
}

func TestHorizontalMovementStopsAtTheEnds(t *testing.T) {
	m := navModel(t)
	m.taskCur = 0

	m = press(m, "h", "h")
	if m.picker {
		t.Fatal("h at the first box quit the graph")
	}
	if m.taskCur != 0 {
		t.Fatalf("taskCur = %d, want clamped to 0", m.taskCur)
	}

	last := len(m.snap.Tasks) - 1
	m.taskCur = last
	m = press(m, "l", "l")
	if m.taskCur != last {
		t.Fatalf("taskCur = %d, want clamped to %d", m.taskCur, last)
	}
}

func TestHorizontalOrderFollowsWhatIsDrawn(t *testing.T) {
	// The cursor indexes layout order (layer, then id); the boxes are drawn
	// in reading order. Walking with l must visit every box exactly once, in
	// the order they appear on screen, or the cursor jumps around the pane.
	m := navModel(t)
	order := m.boxNodeOrder()
	if len(order) != len(m.snap.Tasks) {
		t.Fatalf("boxNodeOrder covers %d of %d tasks", len(order), len(m.snap.Tasks))
	}

	m.taskCur = order[0]
	for i := 1; i < len(order); i++ {
		m = press(m, "l")
		if m.taskCur != order[i] {
			t.Fatalf("step %d: taskCur = %d, want %d (draw order %v)",
				i, m.taskCur, order[i], order)
		}
	}
}

func TestBackingOutStillWorksWithEsc(t *testing.T) {
	// h no longer backs out, so esc must still do it — otherwise the graph
	// becomes a room with no exit.
	m := navModel(t)
	m = press(m, "esc")
	if !m.picker {
		t.Fatal("esc should back out to the run list")
	}
}

func TestHorizontalKeysSwitchPanesFromTheRadio(t *testing.T) {
	// The radio is a flat list with no horizontal axis, so h/l are free to
	// mean "move between panes" there.
	m := navModel(t)
	m.snap.Messages = []*broker.Message{{Seq: 1, Sender: "w", Content: "x"}}
	m.focus = focusRadio

	m = press(m, "h")
	if m.focus != focusTasks {
		t.Fatalf("h from the radio should focus the graph, focus = %v", m.focus)
	}
	m = press(m, "l", "l", "l")
	if m.focus != focusTasks {
		t.Fatalf("l inside the graph should not leave it, focus = %v", m.focus)
	}
}

func TestHorizontalMovementWorksInLaneView(t *testing.T) {
	// The lane view is a vertical list, but h/l should still move the cursor
	// rather than doing nothing or backing out.
	m := navModel(t)
	m.boxMode = false
	m.taskCur = 0

	m = press(m, "l")
	if m.taskCur != 1 {
		t.Fatalf("l in lane view: taskCur = %d, want 1", m.taskCur)
	}
	if m.picker {
		t.Fatal("lane view h/l should not back out")
	}
}
