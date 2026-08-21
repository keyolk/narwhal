package main

import (
	"strings"
	"testing"

	"github.com/keyolk/narwhal/internal/broker"
)

// Each pane is told a height and is expected to occupy it. The inspector
// pads — it has to, or the Radio rule beneath it slides up and down as the
// graph cursor moves. The radio and the graph do not: they render the rows
// they have content for and stop.
//
// On a tall terminal with a short channel that leaves the radio's rule
// floating in the middle of the right-hand column with nothing under it,
// and the whole layout reads as unfinished. The screenshot that prompted
// this had 17 messages against a 60-row body.
//
// The panes are joined with lipgloss.JoinHorizontal, which aligns to the
// tallest column, so a short right side also means the left column's rule
// and the right column's rule do not describe the same region.

func fillModel(t *testing.T, msgs int) tuiModel {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	m := testModel(0, msgs)
	m.width, m.height = 160, 60
	m.snap.RunID = "r1"
	m.snap.State = broker.RunActive
	m.snap.Tasks = []broker.TaskSnapshot{
		{ID: "task-1", State: broker.TaskCompleted, Dispatches: 1},
		{ID: "task-2", State: broker.TaskDispatched, Dispatches: 1},
	}
	return m
}

func rowsOf(s string) int { return len(strings.Split(s, "\n")) }

func TestTheRadioFillsTheHeightItIsGiven(t *testing.T) {
	m := fillModel(t, 3)
	const h = 20
	if got := rowsOf(m.viewRadio(60, h)); got != h {
		t.Errorf("the radio drew %d rows of the %d it was given; the rest of "+
			"the column is empty and its rule floats", got, h)
	}
}

func TestTheGraphFillsTheHeightItIsGiven(t *testing.T) {
	m := fillModel(t, 3)
	const h = 30
	if got := rowsOf(m.viewTasks(60, h)); got != h {
		t.Errorf("the graph drew %d rows of the %d it was given", got, h)
	}
}

func TestBothColumnsAreTheSameHeight(t *testing.T) {
	// The symptom as the eye sees it: two columns of different length
	// beside each other.
	m := fillModel(t, 3)
	body := m.height - 3
	left := rowsOf(m.viewTasks(m.graphPaneWidth(), body))
	inspect := m.inspectorHeight(body)
	right := rowsOf(m.viewInspector(60, inspect-1)) + 1 +
		rowsOf(m.viewRadio(60, body-inspect))
	if left != right {
		t.Errorf("left column is %d rows and right is %d", left, right)
	}
}

func TestAFullChannelIsUnaffected(t *testing.T) {
	// The regression guard: padding must not push a busy channel around.
	m := fillModel(t, 40)
	const h = 12
	out := m.viewRadio(60, h)
	if got := rowsOf(out); got != h {
		t.Errorf("a full radio drew %d rows, want %d", got, h)
	}
	if !strings.Contains(stripEscapes(out), "Radio") {
		t.Error("the title is gone")
	}
}

func TestTheNodePaneDoesNotTakeSpaceItCannotUse(t *testing.T) {
	// The other half of what makes the layout look unfinished. The
	// inspector took a fixed two fifths of the body — 25 rows of a 63-row
	// terminal — whether or not it had that much to show. A node with no
	// activity yet fills five of them and pads the other twenty, so the
	// Radio rule starts a third of the way down the screen with nothing
	// above it, which is what reads as floating.
	m := fillModel(t, 17)
	m.height = 66
	body := m.height - 3

	// A selected task with no worker output: headline, model, blocks,
	// activity heading, one line saying there is nothing. Nowhere near
	// two fifths of the screen.
	want := m.inspectorHeight(body)
	content := m.inspectorContentHeight()
	if want > content {
		t.Errorf("the node pane claims %d rows for %d rows of content on a "+
			"%d-row body", want, content, body)
	}
	// And the number that matters to the eye: the Radio rule must not
	// start a third of the way down an otherwise empty column.
	if want > body/4 {
		t.Errorf("an empty node pane took %d of %d body rows", want, body)
	}
}

func TestTheNodePaneStillGrowsForABusyWorker(t *testing.T) {
	// And the guard on the other side: activity is the reason this pane
	// exists, so a worker with plenty of it must still get room.
	m := fillModel(t, 17)
	m.height = 66
	m.taskCur = 0
	giveNodeActivity(t, &m, "task-1", 200)
	body := m.height - 3
	if got := m.inspectorHeight(body); got < 12 {
		t.Errorf("a worker with 200 lines of activity got %d rows", got)
	}
}
