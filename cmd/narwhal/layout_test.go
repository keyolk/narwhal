package main

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/keyolk/narwhal/internal/broker"
)

// The run view is the graph beside one pane being read, in one frame; the
// pane's title names the others by the keys that show them.
func TestTheRunViewIsTheGraphBesideOneReader(t *testing.T) {
	m := fixtureRun(t)
	m.width, m.height = 160, 40
	lines := strings.Split(ansi.Strip(m.View()), "\n")

	var tops []int
	for i, l := range lines {
		if strings.HasPrefix(l, "╭") {
			tops = append(tops, i)
		}
	}
	if len(tops) != 1 {
		t.Fatalf("%d frames, want 1:\n%s", len(tops), strings.Join(lines, "\n"))
	}
	top := lines[tops[0]]
	g, split, tabs := strings.Index(top, "1 Graph"), strings.Index(top, "┬"), strings.Index(top, "2 Task · 3 Timeline · 4 Radio")
	if g < 0 || split < g || tabs < split {
		t.Errorf("the frame's top %q does not hold the graph, then the tabs", top)
	}
	// Each number shows its pane in the right column, alone.
	for key, want := range map[string]string{"2": "activity", "3": "now", "4": "LockKey is missing"} {
		got := ansi.Strip(press(m, "1", key).View())
		if !strings.Contains(got, want) {
			t.Errorf("%s does not show %q:\n%s", key, want, got)
		}
	}
	if got := ansi.Strip(press(m, "3").View()); strings.Contains(got, "Radio (") {
		t.Errorf("the radio is still on screen beside the timeline:\n%s", got)
	}
}

// Moving to the graph keeps the right column on what was being read.
func TestTheGraphKeepsWhatIsBeingRead(t *testing.T) {
	m := fixtureRun(t)
	m.width, m.height = 160, 40
	m = press(m, "3", "1", "j")
	if m.readerPane() != focusTimeline {
		t.Errorf("after 3 then the graph, the right column shows %v", m.readerPane())
	}
	// l steps through the tabs from the right column.
	m = press(m, "3", "l")
	if m.focus != focusRadio {
		t.Errorf("l from the timeline went to %v, want the radio", m.focus)
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
	if !strings.Contains(top, styCyanBold.Render("Task")) {
		t.Errorf("the focused title lost its colour: %q", top)
	}
}

// Under the graph, in the room its drawing does not use, the run's result:
// how far it has got while it works, its answer and what it produced once
// it has one.
func TestTheResultSitsUnderTheGraph(t *testing.T) {
	m := fixtureRun(t)
	m.width, m.height = 160, 40
	m.live.BrokerURL = "http://127.0.0.1:1"
	got := ansi.Strip(m.View())
	if !strings.Contains(got, "no answer yet — 2 of 4 tasks finished") {
		t.Errorf("a working run's result does not say how far it has got:\n%s", got)
	}
	for i := range m.snap.Tasks {
		if m.snap.Tasks[i].ID == "synthesis" {
			m.snap.Tasks[i].State = broker.TaskCompleted
			m.snap.Tasks[i].Name = "synthesis"
			m.snap.Tasks[i].Outcome = "LockKey is validated in one middleware"
		}
	}
	got = ansi.Strip(m.View())
	if !strings.Contains(got, "Result  from synthesis") || !strings.Contains(got, "LockKey is validated in one middleware") {
		t.Errorf("the answer is not under the graph:\n%s", got)
	}
	if !strings.Contains(got, "3 answers · o to read them") {
		t.Errorf("what the run produced is not counted:\n%s", got)
	}
	// On a short terminal the graph keeps the column.
	m.height = 16
	if got := ansi.Strip(m.View()); strings.Contains(got, "Result") {
		t.Errorf("the result squeezed the graph on a short terminal:\n%s", got)
	}
}
