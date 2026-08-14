package main

import (
	"testing"

	"github.com/keyolk/narwhal/internal/broker"
)

// The box view draws a diagram, so the cursor should move the way the eye
// does. It did not: j and k stepped through layout order — (layer, then
// id) — so pressing down from the middle of three siblings went to the
// sibling on its right, while the box directly underneath was the one
// being looked at.

// fanShape is three siblings over one synthesis task: the layout that
// exposed the defect.
//
//	┌ task-1 ┐  ┌ task-2 ┐  ┌ task-3 ┐
//	     └──────────┬─────────┘
//	          ┌ task-4 ┐
func fanShape() []broker.TaskSnapshot {
	return []broker.TaskSnapshot{
		{ID: "task-1", State: broker.TaskDispatched},
		{ID: "task-2", State: broker.TaskDispatched},
		{ID: "task-3", State: broker.TaskCompleted},
		{ID: "task-4", State: broker.TaskCompleted,
			Deps: []string{"task-1", "task-2", "task-3"}},
	}
}

func navModelFor(t *testing.T, tasks []broker.TaskSnapshot) tuiModel {
	t.Helper()
	m := testModel(0, 0)
	// Wide enough that a five-box row fits on one line. The pane is capped
	// at 52 columns in box mode, so a narrower terminal wraps the row and
	// the layout under test is not the one being described.
	m.width, m.height = 200, 40
	m.focus = focusTasks
	m.boxMode = true
	m.snap.Tasks = tasks
	return m
}

// selectTask puts the cursor on a task by id.
func selectTask(t *testing.T, m *tuiModel, id string) {
	t.Helper()
	for i, task := range m.sortedTasks() {
		if task.ID == id {
			m.taskCur = i
			return
		}
	}
	t.Fatalf("no task %q", id)
}

// currentID is the id under the cursor.
func currentID(m tuiModel) string {
	return m.taskByIndex(m.taskCur).ID
}

func TestDownGoesToTheBoxBelow(t *testing.T) {
	// The reported case: on task-2, down went to task-3 — its right-hand
	// neighbour — instead of task-4, which is drawn directly underneath.
	m := navModelFor(t, fanShape())
	selectTask(t, &m, "task-2")

	m.moveVertical(1)
	if got := currentID(m); got != "task-4" {
		t.Fatalf("down from task-2 went to %s, want task-4 (the box below it)", got)
	}
}

func TestDownFromAnySiblingReachesTheChild(t *testing.T) {
	// Every sibling feeds the same child, and j follows the edge — which is
	// the relationship h and l cannot express, since they already own the
	// horizontal axis. So the outer siblings reach it too, even though the
	// child is drawn under the middle one.
	for _, from := range []string{"task-1", "task-2", "task-3"} {
		m := navModelFor(t, fanShape())
		selectTask(t, &m, from)
		m.moveVertical(1)
		if got := currentID(m); got != "task-4" {
			t.Errorf("down from %s went to %s, want task-4", from, got)
		}
	}
}

func TestUpPicksTheNearestBoxHorizontally(t *testing.T) {
	// Going back up from a fan-in has three candidates. The nearest by
	// centre is the honest answer — it is the one the eye is over.
	m := navModelFor(t, fanShape())
	selectTask(t, &m, "task-4")

	m.moveVertical(-1)
	if got := currentID(m); got != "task-2" {
		t.Fatalf("up from task-4 went to %s, want the nearest sibling task-2", got)
	}
}

func TestVerticalMovementStopsAtTheEdges(t *testing.T) {
	m := navModelFor(t, fanShape())

	selectTask(t, &m, "task-1")
	m.moveVertical(-1)
	if got := currentID(m); got != "task-1" {
		t.Errorf("up from the top row moved to %s", got)
	}

	selectTask(t, &m, "task-4")
	m.moveVertical(1)
	if got := currentID(m); got != "task-4" {
		t.Errorf("down from the bottom row moved to %s", got)
	}
}

func TestHorizontalMovementWalksTheRow(t *testing.T) {
	m := navModelFor(t, fanShape())
	selectTask(t, &m, "task-1")

	m.moveHorizontal(1)
	if got := currentID(m); got != "task-2" {
		t.Fatalf("right from task-1 went to %s, want task-2", got)
	}
	m.moveHorizontal(1)
	if got := currentID(m); got != "task-3" {
		t.Fatalf("right from task-2 went to %s, want task-3", got)
	}
	m.moveHorizontal(-1)
	if got := currentID(m); got != "task-2" {
		t.Fatalf("left from task-3 went to %s, want task-2", got)
	}
}

func TestHorizontalWrapsToTheNextRow(t *testing.T) {
	// A box alone on its row would otherwise be a dead end in both
	// directions, and l would stop covering the graph.
	m := navModelFor(t, fanShape())
	selectTask(t, &m, "task-3") // last box of the top row

	m.moveHorizontal(1)
	if got := currentID(m); got != "task-4" {
		t.Fatalf("right from the end of a row went to %s, want task-4", got)
	}
}

func TestVerticalMovementInLaneView(t *testing.T) {
	// The lane view is a plain vertical list: layout order is screen
	// order, so index arithmetic is the honest answer there.
	m := navModelFor(t, fanShape())
	m.boxMode = false
	m.taskCur = 0

	m.moveVertical(1)
	if m.taskCur != 1 {
		t.Fatalf("lane view down: taskCur = %d, want 1", m.taskCur)
	}
	m.moveVertical(-1)
	if m.taskCur != 0 {
		t.Fatalf("lane view up: taskCur = %d, want 0", m.taskCur)
	}
}

func TestKeysDriveGeometricMovement(t *testing.T) {
	// The functions are right; this checks j and k actually call them.
	m := navModelFor(t, fanShape())
	selectTask(t, &m, "task-2")

	m = press(m, "j")
	if got := currentID(m); got != "task-4" {
		t.Fatalf("pressing j from task-2 selected %s, want task-4", got)
	}
}

func TestRadioMovementIsUnaffected(t *testing.T) {
	// The radio is a flat list and must keep stepping one row at a time.
	m := testModel(0, 5)
	m.focus = focusRadio
	m.followTail = false
	m.radioCur = 0

	m = press(m, "j", "j")
	if m.radioCur != 2 {
		t.Fatalf("radioCur = %d, want 2", m.radioCur)
	}
}

// wideRowShape is five boxes on one row with a single child under the
// second — the layout that showed nearest-by-centre was not enough.
//
//	┌ t1 ┐ ┌ t2 ┐ ┌ t3 ┐ ┌ t4 ┐ ┌ t5 ┐
//	         └──┐
//	         ┌ t6 ┐
func wideRowShape() []broker.TaskSnapshot {
	return []broker.TaskSnapshot{
		{ID: "t1", State: broker.TaskDispatched},
		{ID: "t2", State: broker.TaskDispatched},
		{ID: "t3", State: broker.TaskDispatched},
		{ID: "t4", State: broker.TaskDispatched},
		{ID: "t5", State: broker.TaskReady},
		{ID: "t6", State: broker.TaskReady, Deps: []string{"t2"}},
	}
}

func TestDownDoesNotWanderToAnUnrelatedBox(t *testing.T) {
	// Nearest-by-centre sent every box on the row to t6, because it was the
	// only thing on the row below and won by default. Nothing connects t1,
	// t3, t4 or t5 to it and nothing sits under them, so the cursor stays —
	// moving to an unrelated box teaches you the arrow keys are
	// unpredictable.
	m := navModelFor(t, wideRowShape())

	for _, from := range []string{"t1", "t3", "t4", "t5"} {
		selectTask(t, &m, from)
		m.moveVertical(1)
		if got := currentID(m); got != from {
			t.Errorf("down from %s went to %s; nothing links or sits below it", from, got)
		}
	}
}

func TestDownFollowsTheEdgeToTheChild(t *testing.T) {
	m := navModelFor(t, wideRowShape())
	selectTask(t, &m, "t2")

	m.moveVertical(1)
	if got := currentID(m); got != "t6" {
		t.Fatalf("down from t2 went to %s, want its child t6", got)
	}
}

func TestUpFromALoneChildFindsItsParent(t *testing.T) {
	m := navModelFor(t, wideRowShape())
	selectTask(t, &m, "t6")

	m.moveVertical(-1)
	if got := currentID(m); got != "t2" {
		t.Fatalf("up from t6 went to %s, want its parent t2", got)
	}
}

func TestALoneChildIsDrawnUnderItsParent(t *testing.T) {
	// Rows are centred independently, which is right for siblings and
	// wrong for a lone child: t6 was drawn dead centre, under t3, with the
	// edge reaching sideways to t2. The diagram was correct and unreadable,
	// and it made "press down" land somewhere the eye did not expect.
	m := navModelFor(t, wideRowShape())
	positions := m.boxPositions()

	var parent, child boxPos
	for _, p := range positions {
		switch m.taskByIndex(p.node).ID {
		case "t2":
			parent = p
		case "t6":
			child = p
		}
	}
	if parent.x1 == 0 || child.x1 == 0 {
		t.Fatal("expected both t2 and t6 to be drawn")
	}
	if child.x1 <= parent.x0 || child.x0 >= parent.x1 {
		t.Fatalf("child at [%d,%d) does not sit under parent at [%d,%d)",
			child.x0, child.x1, parent.x0, parent.x1)
	}
}

func TestSiblingRowsStayCentred(t *testing.T) {
	// Aligning under a parent must not disturb a row of siblings — they
	// have no single parent to line up with, and moving them individually
	// would let them overlap.
	m := navModelFor(t, fanShape())
	positions := m.boxPositions()

	var first, last boxPos
	for _, p := range positions {
		switch m.taskByIndex(p.node).ID {
		case "task-1":
			first = p
		case "task-3":
			last = p
		}
	}
	if first.x0 == 0 && last.x1 >= m.graphPaneWidth() {
		t.Fatal("the sibling row was pushed to fill the pane")
	}
	if first.x0 >= last.x0 {
		t.Fatalf("sibling order changed: task-1 at %d, task-3 at %d", first.x0, last.x0)
	}
}
