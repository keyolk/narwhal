package main

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// Graph and Node share one framed band, Timeline and Radio another, and
// each band is split at one height across both columns.
func TestThePanesSitInTwoBands(t *testing.T) {
	m := fixtureRun(t)
	m.width, m.height = 160, 40
	lines := strings.Split(ansi.Strip(m.View()), "\n")

	var tops []int
	for i, l := range lines {
		if strings.HasPrefix(l, "╭") {
			tops = append(tops, i)
		}
	}
	if len(tops) != 2 {
		t.Fatalf("%d band tops, want 2:\n%s", len(tops), strings.Join(lines, "\n"))
	}
	for i, want := range [][2]string{{"1 Graph", "2 Node"}, {"3 Timeline", "4 Radio"}} {
		top := lines[tops[i]]
		a, b := strings.Index(top, want[0]), strings.Index(top, want[1])
		if a < 0 || b < a || !strings.Contains(top[a:b], "┬") {
			t.Errorf("band %d top %q does not hold %s then %s", i+1, top, want[0], want[1])
		}
	}
	// One split column for both bands: the divider is at the same cell.
	col := func(l, r string) int { return ansi.StringWidth(l[:strings.Index(l, r)]) }
	if a, b := col(lines[tops[0]], "┬"), col(lines[tops[1]], "┬"); a != b {
		t.Errorf("the bands split at columns %d and %d", a, b)
	}
}

// Every band row is as wide as the screen, Hangul included, so the right
// border is one straight line.
func TestBandRowsAreTheScreenWidth(t *testing.T) {
	m := liveModel(t)
	m.width, m.height = 140, 36
	m.activity = map[string]agentLive{"alpha-worker": {said: "미들웨어로 가면 replay 경로까지 막힌다."}}
	l := m.layout()
	for i, line := range strings.Split(ansi.Strip(m.viewBands(l)), "\n") {
		if w := ansi.StringWidth(line); w != m.width {
			t.Errorf("row %d is %d cells, want %d: %q", i, w, m.width, line)
		}
	}
}

// The focused pane's title keeps its colour on the border.
func TestTheBorderTitleKeepsTheFocusColour(t *testing.T) {
	forceColor(t)
	m := liveModel(t)
	m.width, m.height = 140, 36
	m.focus = focusNode
	top := strings.Split(m.viewBands(m.layout()), "\n")[0]
	if !strings.Contains(top, styCyanBold.Render("Node")) {
		t.Errorf("the focused title lost its colour: %q", top)
	}
}

// A run with nothing of its own for pane 3 — recorded before task times —
// folds it: the radio takes the band, and the edge says why.
func TestASparseTimelineFoldsIntoTheRadiosBand(t *testing.T) {
	m := liveModel(t)
	m.width, m.height = 160, 40
	m.snap.Messages = briefModel(t).snap.Messages
	got := ansi.Strip(m.View())
	if strings.Contains(got, "╭─ 3 Timeline") {
		t.Fatalf("a near-empty timeline kept its column:\n%s", got)
	}
	if !strings.Contains(got, "3 Timeline folded · this run predates task times") {
		t.Errorf("the fold is not explained:\n%s", got)
	}
	// 3 still shows it.
	if got := ansi.Strip(press(m, "3").View()); !strings.Contains(got, "3 Timeline") || strings.Contains(got, "4 Radio") {
		t.Errorf("3 on a folded timeline:\n%s", got)
	}
	// With task times it has its column.
	at := time.Now()
	for i := range m.snap.Tasks {
		m.snap.Tasks[i].StartedAt = at
	}
	if got := ansi.Strip(m.View()); !strings.Contains(got, "╭─ 3 Timeline") {
		t.Errorf("a timeline with events did not get its column:\n%s", got)
	}
}
