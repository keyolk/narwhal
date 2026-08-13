package main

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/keyolk/narwhal/internal/broker"
)

// forceColor makes lipgloss emit escape sequences even without a TTY.
// Without it every style renders as plain text and a "was this styled?"
// assertion silently passes on unstyled output.
func forceColor(t *testing.T) {
	t.Helper()
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI256)
	t.Cleanup(func() { lipgloss.SetColorProfile(prev) })
}

// siblingTasks is a layer of three boxes over one synthesis box — the shape
// where per-line attribution breaks down.
func siblingTasks() []broker.TaskSnapshot {
	return []broker.TaskSnapshot{
		{ID: "auth", State: broker.TaskCompleted},
		{ID: "api", State: broker.TaskDispatched},
		{ID: "utils", State: broker.TaskReady},
		{ID: "synthesis", State: broker.TaskPending, Deps: []string{"auth", "api", "utils"}},
	}
}

// boxTestModel is a model whose graph is siblingTasks, focused on the graph.
func boxTestModel(t *testing.T) tuiModel {
	t.Helper()
	forceColor(t)
	m := testModel(0, 0)
	m.width, m.height = 100, 24
	m.focus = focusTasks
	m.snap.Tasks = siblingTasks()
	return m
}

func TestEveryBoxIsAddressableOnASharedRow(t *testing.T) {
	// Regression: boxRow carried a single node, so on a row with three
	// siblings the first box claimed the line and the other two could not
	// be selected at all.
	rows := layoutGraph(siblingTasks()).renderBoxes(50, taskIconPlain)

	for node := 0; node < 3; node++ {
		found := false
		for _, r := range rows {
			if r.owns(node) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("node %d owns no row; it cannot be selected", node)
		}
	}
}

func TestSpansOnASharedRowDoNotOverlap(t *testing.T) {
	rows := layoutGraph(siblingTasks()).renderBoxes(50, taskIconPlain)
	for i, r := range rows {
		for j := 1; j < len(r.spans); j++ {
			prev, cur := r.spans[j-1], r.spans[j]
			if cur.x0 < prev.x1 {
				t.Errorf("row %d: span %v overlaps %v", i, cur, prev)
			}
		}
	}
}

func TestSpanOfReturnsTheRightColumns(t *testing.T) {
	rows := layoutGraph(siblingTasks()).renderBoxes(50, taskIconPlain)
	var row boxRow
	for _, r := range rows {
		if len(r.spans) == 3 {
			row = r
			break
		}
	}
	if len(row.spans) != 3 {
		t.Fatal("no row with three sibling boxes")
	}
	// Each span must cover its own box and nothing else: slicing the text
	// by the span should yield a balanced box fragment.
	runes := []rune(row.text)
	for _, s := range row.spans {
		seg := string(runes[s.x0:s.x1])
		if strings.TrimSpace(seg) == "" {
			t.Errorf("span %v covers only whitespace", s)
		}
	}
}

func TestSelectionHighlightsOneBoxNotTheRow(t *testing.T) {
	m := boxTestModel(t)
	m.taskCur = 2 // utils, the third sibling

	rows := m.boxRows(50)
	var body boxRow
	for _, r := range rows {
		if len(r.spans) == 3 && r.part == partBody {
			body = r
			break
		}
	}
	if len(body.spans) != 3 {
		t.Fatal("no three-sibling body row")
	}

	out := m.styleBoxLine(body, body.text, m.taskCur)
	sel := body.spanOf(2)
	runes := []rune(body.text)
	selText := string(runes[sel.x0:sel.x1])

	// The selected fragment is rendered with the selection style; a sibling
	// fragment is not.
	if !strings.Contains(out, stySel.Render(selText)) {
		t.Errorf("selected box %q is not highlighted:\n%q", selText, out)
	}
	other := body.spanOf(0)
	otherText := string(runes[other.x0:other.x1])
	if strings.Contains(out, stySel.Render(otherText)) {
		t.Errorf("unselected sibling %q was highlighted too", otherText)
	}
}

func TestUnselectedSiblingsKeepTheirStateColour(t *testing.T) {
	// Before spans, the whole line took the first box's state colour, so a
	// completed box and a running one looked identical.
	m := boxTestModel(t)
	m.taskCur = 0

	rows := m.boxRows(50)
	var body boxRow
	for _, r := range rows {
		if len(r.spans) == 3 && r.part == partBody {
			body = r
			break
		}
	}
	out := m.styleBoxLine(body, body.text, m.taskCur)

	runes := []rune(body.text)
	for _, s := range body.spans {
		if s.node == m.taskCur {
			continue
		}
		seg := string(runes[s.x0:s.x1])
		_, style := taskIconStyle(m.taskByIndex(s.node).State)
		if !strings.Contains(out, style.Render(seg)) {
			t.Errorf("sibling %q lost its state colour", seg)
		}
	}
}
