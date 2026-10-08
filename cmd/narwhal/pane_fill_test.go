package main

import (
	"github.com/charmbracelet/x/ansi"
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
	// Every row of the body, in both bands, carries a border at the same
	// three columns: the two columns end together.
	l := m.layout()
	lines := strings.Split(stripEscapes(m.viewBands(l)), "\n")
	if want := l.top + l.bottom + 2*bandBorderRows; len(lines) != want {
		t.Fatalf("the bands drew %d rows, want %d", len(lines), want)
	}
	for i, line := range lines {
		if w := ansi.StringWidth(line); w != l.left+l.right+bandBorderCols {
			t.Errorf("row %d is %d cells, want %d: %q", i, w, l.left+l.right+bandBorderCols, line)
		}
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
	m.boxMode = false
	body := m.bodyHeight()

	// A selected task with no worker output: headline, model, blocks,
	// activity heading, one line saying there is nothing. The band is
	// sized to the larger of that and the graph, and no more.
	got := m.nodeBandRows()
	want := max(m.graphContentHeight(m.graphPaneWidth())-1, m.inspectorContentHeight()-1, minTopBand)
	if got > want {
		t.Errorf("the top band claims %d rows for %d rows of content on a "+
			"%d-row body", got, want, body)
	}
}

func TestTheNodePaneStillGrowsForABusyWorker(t *testing.T) {
	// And the guard on the other side: activity is the reason this pane
	// exists, so a worker with plenty of it must still get room.
	m := fillModel(t, 17)
	m.height = 66
	m.taskCur = 0
	giveNodeActivity(t, &m, "task-1", 200)
	if got := m.nodeBandRows(); got < 12 {
		t.Errorf("a worker with 200 lines of activity got %d rows", got)
	}
}
