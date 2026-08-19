package main

import (
	"testing"

	"github.com/keyolk/narwhal/internal/broker"
)

// The node pane's scroll offset belongs to the worker being read. Moving
// the cursor by hand goes through selectNode, which rewinds it — carrying
// an offset to another node lands you in the middle of a different
// worker's transcript at a position that means nothing there.
//
// A poll can move the cursor too. graph_update_test.go covers the cursor
// itself: it stays on its task when one is inserted above, and is clamped
// back into range when the selected task is gone. What it does not cover is
// what the node pane is showing afterwards. restoreTaskCursor and
// clampCursors both assign taskCur directly rather than going through
// selectNode, so when a snapshot moves the cursor the offset stays where
// the last node left it — and the pane opens on another worker's feed
// scrolled to a line number borrowed from somewhere else.

func TestTheNodePaneRewindsWhenASnapshotMovesTheCursor(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := testModel(0, 0)
	m.width, m.height = 120, 40
	m.snap.RunID = "r1"
	m.snap.State = broker.RunActive
	m.snap.Tasks = []broker.TaskSnapshot{
		{ID: "task-1", State: broker.TaskCompleted},
		{ID: "task-2", State: broker.TaskDispatched},
	}
	selectTask(t, &m, "task-2")
	m.focus = focusNode
	giveNodeActivity(t, &m, "task-2", 60)

	m = press(m, "k", "k", "k")
	if m.nodeScroll == nodeScrollTail {
		t.Fatal("setup: the node pane is still following the tail")
	}

	// The selected task is gone from the next poll, so the cursor has to
	// land on another node.
	next := m.snap
	next.Tasks = []broker.TaskSnapshot{{ID: "task-1", State: broker.TaskCompleted}}
	out, _ := m.Update(snapshotMsg{snap: next})
	m = out.(tuiModel)

	if currentID(m) == "task-2" {
		t.Fatal("setup: the cursor is still on the departed task")
	}
	if m.nodeScroll != nodeScrollTail {
		t.Errorf("the node pane is at offset %d, carried over from the task "+
			"the cursor was on before the graph changed", m.nodeScroll)
	}
}

func TestAPollThatLeavesTheCursorAloneKeepsYourPlace(t *testing.T) {
	// The other half: polls happen every second, and rewinding on every
	// one of them would make the pane impossible to read while a worker is
	// running. Only a cursor that actually moved rewinds.
	t.Setenv("HOME", t.TempDir())
	m := testModel(0, 0)
	m.width, m.height = 120, 40
	m.snap.RunID = "r1"
	m.snap.State = broker.RunActive
	m.snap.Tasks = []broker.TaskSnapshot{
		{ID: "task-1", State: broker.TaskCompleted},
		{ID: "task-2", State: broker.TaskDispatched},
	}
	selectTask(t, &m, "task-2")
	m.focus = focusNode
	giveNodeActivity(t, &m, "task-2", 60)

	m = press(m, "k", "k", "k")
	parked := m.nodeScroll

	// A task is added elsewhere; the cursor still resolves to task-2.
	next := m.snap
	next.Tasks = append(append([]broker.TaskSnapshot(nil), next.Tasks...),
		broker.TaskSnapshot{ID: "task-3", State: broker.TaskPending})
	out, _ := m.Update(snapshotMsg{snap: next})
	m = out.(tuiModel)

	if currentID(m) != "task-2" {
		t.Fatalf("setup: the cursor moved to %s", currentID(m))
	}
	if m.nodeScroll != parked {
		t.Errorf("an ordinary poll moved the node pane from %d to %d",
			parked, m.nodeScroll)
	}
}
