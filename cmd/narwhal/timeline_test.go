package main

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/keyolk/narwhal/internal/broker"
)

// timedLiveModel is liveModel with its tasks' times recorded, so pane 3 has
// events of its own and keeps its column rather than folding.
func timedLiveModel(t *testing.T) tuiModel {
	t.Helper()
	m := liveModel(t)
	m.width, m.height = 160, 40
	at := time.Date(2026, 10, 8, 10, 0, 0, 0, time.Local)
	for i := range m.snap.Tasks {
		m.snap.Tasks[i].StartedAt = at.Add(time.Duration(i) * time.Second)
	}
	m.snap.Messages = briefModel(t).snap.Messages
	return m
}

// 3 shows pane 3 in the right column, in the radio's place.
func TestThreeShowsTheTimeline(t *testing.T) {
	m := timedLiveModel(t)
	m = press(m, "3")
	if m.focus != focusTimeline {
		t.Fatalf("3: focus=%v", m.focus)
	}
	got := ansi.Strip(m.View())
	if !strings.Contains(got, "1 Graph") || strings.Contains(got, "Radio (6)") {
		t.Fatalf("pane 3 is not beside the graph in the radio's place:\n%s", got)
	}
	if !strings.Contains(lineContaining(got, "지시:"), "@schema add the LockKey field") {
		t.Errorf("the timeline left out the operator's instruction:\n%s", got)
	}
}

// Tab reaches every pane in the order of their numbers.
func TestTabReachesTheTimeline(t *testing.T) {
	m := press(liveModel(t), "1")
	for i, want := range []focusPane{focusNode, focusTimeline, focusRadio, focusTasks} {
		if m = press(m, "tab"); m.focus != want {
			t.Fatalf("tab %d: focus=%v, want %v", i+1, m.focus, want)
		}
	}
}

// j/k move the selection in pane 3; past the newest it follows again.
func TestTheTimelineSelectionMoves(t *testing.T) {
	m := liveModel(t)
	m.width, m.height = 160, 40
	m.snap.Messages = briefModel(t).snap.Messages
	m = press(m, "3", "z")
	n := len(m.timelineItems())
	if n < 3 {
		t.Fatalf("setup: %d entries", n)
	}
	m = press(m, "k")
	if m.timelineSel != n-2 {
		t.Fatalf("k: sel=%d, want %d", m.timelineSel, n-2)
	}
	m = press(m, "g")
	if m.timelineSel != 0 {
		t.Fatalf("g: sel=%d", m.timelineSel)
	}
	m = press(m, "G")
	if m.timelineSel != timelineFollow {
		t.Fatalf("G: sel=%d", m.timelineSel)
	}
	m = press(m, "k", "j")
	if m.timelineSel != timelineFollow {
		t.Fatalf("j onto the newest did not follow again: %d", m.timelineSel)
	}
}

// The timeline records the tasks' lifecycle.
func TestTheTimelineRecordsTheTasks(t *testing.T) {
	m := timedLiveModel(t)
	m.snap.Tasks[2].EndedAt = m.snap.Tasks[2].StartedAt.Add(time.Minute)
	m.snap.Tasks[2].Outcome = "gamma found the leak"
	if got := ansi.Strip(press(m, "3").View()); !strings.Contains(got, "gamma found the leak") {
		t.Errorf("task lifecycle missing from the timeline:\n%s", got)
	}
}

// The footer always says the run can be steered, and what became of the
// last instruction.
func TestTheFooterCarriesTheSteerControl(t *testing.T) {
	m := briefModel(t)
	got := ansi.Strip(m.viewFooter())
	for _, want := range []string{"press i to tell the workers", "last: everyone: gofmt before committing", "✓"} {
		if !strings.Contains(got, want) {
			t.Errorf("footer lacks %q:\n%s", want, got)
		}
	}
	if lines := strings.Split(got, "\n"); len(lines) != 2 {
		t.Errorf("footer is %d lines, want 2:\n%s", len(lines), got)
	}
	if fresh := ansi.Strip(liveModel(t).viewFooter()); !strings.Contains(fresh, "press i") || strings.Contains(fresh, "last:") {
		t.Errorf("footer before any instruction:\n%s", fresh)
	}
}

// The timeline leads with the result and what each task's outcome rests on;
// i on a line opens a steer to that task, quoting the line.
func TestATimelineLineIsCorrectedFromWhereItIs(t *testing.T) {
	m := fixtureRun(t)
	m.snap.TaskReasons = map[string]broker.TaskReasons{
		"schema": {Unverified: []string{"old fixtures were not re-decoded"}},
	}
	m = press(m, "3")
	got := ansi.Strip(m.View())
	r, why, flow := strings.Index(got, "결과"), strings.Index(got, "근거"), strings.Index(got, "미검증 ⚠ old fixtures were not re-decoded")
	if r < 0 || why < r || flow < why {
		t.Fatalf("the timeline does not lead with the result and its grounds:\n%s", got)
	}
	for i, it := range m.timelineItems() {
		if it.reason != nil && it.reason.Kind == broker.ReasonUnverified {
			m.timelineSel = i
		}
	}
	m = press(m, "i")
	if !m.steering || !strings.HasPrefix(m.steerBuf.Value(), "@schema [미검증: old fixtures were not re-decoded] → ") {
		t.Errorf("i on the line: steering=%v buf=%q", m.steering, m.steerBuf.Value())
	}
}

// The flow section draws each task's course on one clock: a lane per task,
// in the order the scenes list them, and a ruler under them. One task, or a
// pane too narrow for a lane, draws nothing.
func TestTheTimelineDrawsTaskLanes(t *testing.T) {
	at := time.Date(2026, 10, 8, 10, 0, 0, 0, time.Local)
	scenes := []broker.Scene{
		{Kind: broker.SceneTask, Who: []string{"schema"}, Mark: "✓", Start: at, End: at.Add(4 * time.Minute)},
		{Kind: broker.SceneTask, Who: []string{"api"}, Mark: "▸", Start: at.Add(2 * time.Minute)},
		{Kind: broker.SceneNow, Start: at.Add(10 * time.Minute), End: at.Add(10 * time.Minute)},
	}
	lanes := taskLanes(scenes, 60)
	if len(lanes) != 3 {
		t.Fatalf("want 2 lanes and a ruler, got %d:\n%s", len(lanes), strings.Join(lanes, "\n"))
	}
	for i, l := range lanes {
		if w := ansi.StringWidth(l); w != 60 {
			t.Errorf("row %d is %d cells, want 60: %q", i, w, ansi.Strip(l))
		}
	}
	if !strings.HasPrefix(ansi.Strip(lanes[0]), "schema") || !strings.HasPrefix(ansi.Strip(lanes[1]), "api") {
		t.Errorf("lanes out of scene order:\n%s", ansi.Strip(strings.Join(lanes, "\n")))
	}
	if !strings.Contains(ansi.Strip(lanes[2]), "10:04") {
		t.Errorf("ruler has no tick:\n%s", ansi.Strip(lanes[2]))
	}
	// schema ended at 10:04: its lane is empty past the middle, while api,
	// still open, runs up to now.
	sch, api := []rune(ansi.Strip(lanes[0])), []rune(ansi.Strip(lanes[1]))
	if sch[len(sch)-5] == api[len(api)-5] {
		t.Errorf("a finished task's lane reaches now:\n%s\n%s", string(sch), string(api))
	}
	if got := taskLanes(scenes[:1], 60); got != nil {
		t.Errorf("one task drew lanes: %q", got)
	}
	if got := taskLanes(scenes, 20); got != nil {
		t.Errorf("a narrow pane drew lanes: %q", got)
	}
}
