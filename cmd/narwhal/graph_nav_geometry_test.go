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
	m.width, m.height = 120, 30
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
	// Every sibling has the same child, so every one of them must find it.
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
