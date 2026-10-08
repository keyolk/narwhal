package main

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/keyolk/narwhal/internal/broker"
)

// 3 focuses pane 3 where it is, under the graph; it does not move into
// another pane's slot.
func TestThreeFocusesTheTimelineUnderTheGraph(t *testing.T) {
	m := liveModel(t)
	m.width, m.height = 160, 40
	m.snap.Messages = briefModel(t).snap.Messages
	m = press(m, "3")
	if m.focus != focusTimeline {
		t.Fatalf("3: focus=%v", m.focus)
	}
	got := ansi.Strip(m.View())
	if g, tl := strings.Index(got, "1 Graph"), strings.Index(got, "3 Timeline"); g < 0 || tl < g {
		t.Fatalf("pane 3 is not under the graph:\n%s", got)
	}
	if !strings.Contains(got, "4 Radio (6)") {
		t.Errorf("focusing pane 3 replaced the radio:\n%s", got)
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

// Where there is room for only one band, focusing pane 3 shows it across
// the body, and leaving it brings the band back.
func TestThreeOnAShortTerminalShowsTheTimelineZoomed(t *testing.T) {
	m := liveModel(t)
	m.width, m.height = 160, 14
	m.snap.Messages = briefModel(t).snap.Messages
	m = press(m, "3")
	got := ansi.Strip(m.View())
	if !strings.Contains(got, "3 Timeline") || strings.Contains(got, "1 Graph") {
		t.Fatalf("pane 3 not shown across the body:\n%s", got)
	}
	if !strings.Contains(got, "» operator @schema add the LockKey field") {
		t.Errorf("zoomed pane 3 left out the operator's instruction:\n%s", got)
	}
	m = press(m, "1")
	if got := ansi.Strip(m.View()); !strings.Contains(got, "1 Graph") || !strings.Contains(got, "4 Radio") {
		t.Errorf("leaving pane 3 did not restore the band:\n%s", got)
	}
}

// j/k scroll pane 4; scrolled back, it stays put as events arrive.
func TestTheTimelineScrolls(t *testing.T) {
	m := liveModel(t)
	m.width, m.height = 160, 14
	m.snap.Messages = briefModel(t).snap.Messages
	m = press(m, "3", "z")
	if rows := m.timelineRows(); rows >= len(broker.Timeline(m.snap)) {
		t.Skipf("all %d events fit in %d rows", len(broker.Timeline(m.snap)), rows)
	}
	m = press(m, "k")
	if m.timelineBack != 1 {
		t.Fatalf("k: back=%d, want 1", m.timelineBack)
	}
	m = press(m, "g")
	if m.timelineBack != m.timelineMaxBack() || m.timelineBack == 0 {
		t.Fatalf("g: back=%d, max=%d", m.timelineBack, m.timelineMaxBack())
	}
	m = press(m, "G")
	if m.timelineBack != 0 {
		t.Fatalf("G: back=%d", m.timelineBack)
	}
}

// The history is on screen without a key: under the graph, in the room the
// graph does not use.
func TestTheTimelineSitsUnderTheGraph(t *testing.T) {
	m := liveModel(t)
	m.width, m.height = 160, 40
	at := time.Date(2026, 10, 8, 10, 0, 0, 0, time.Local)
	m.snap.Tasks[2].StartedAt, m.snap.Tasks[2].EndedAt = at, at.Add(time.Minute)
	m.snap.Tasks[2].Outcome = "gamma found the leak"
	m.snap.Messages = briefModel(t).snap.Messages
	got := ansi.Strip(m.View())
	g, tl := strings.Index(got, "1 Graph"), strings.Index(got, "3 Timeline")
	if g < 0 || tl < 0 || tl < g {
		t.Fatalf("no timeline under the graph:\n%s", got)
	}
	if !strings.Contains(got, "gamma found the leak") {
		t.Errorf("task lifecycle missing from the timeline:\n%s", got)
	}
}

// With the radio beside it, the timeline under the graph does not repeat
// it: an instruction or urgent post appears once on screen, in the radio.
func TestTheTimelineBesideTheRadioDoesNotRepeatIt(t *testing.T) {
	m := liveModel(t)
	m.width, m.height = 160, 40
	m.snap.Messages = briefModel(t).snap.Messages
	got := ansi.Strip(m.View())
	for _, msg := range []string{"@schema add the LockKey field", "nonce passes twice on replay"} {
		if n := strings.Count(got, msg); n != 1 {
			t.Errorf("%q is on screen %d times, want once:\n%s", msg, n, got)
		}
	}
	// With the brief in the radio's place nothing else shows them, so the
	// timeline keeps them.
	m = press(m, "1", "4", "4")
	if got := ansi.Strip(m.View()); !strings.Contains(got, "» operator @schema add the LockKey field") {
		t.Errorf("the timeline beside the brief dropped the operator's instruction:\n%s", got)
	}
}

// A short terminal keeps the graph's whole column.
func TestAShortTerminalGivesTheGraphItsColumn(t *testing.T) {
	m := liveModel(t)
	m.width, m.height = 160, 14
	if got := ansi.Strip(m.View()); strings.Contains(got, "3 Timeline") {
		t.Errorf("timeline squeezed in on a short terminal:\n%s", got)
	}
}

// The footer always says the run can be steered, and what became of the
// last instruction.
func TestTheFooterCarriesTheSteerControl(t *testing.T) {
	m := briefModel(t)
	got := ansi.Strip(m.viewFooter())
	for _, want := range []string{"i » steer", "last: everyone: gofmt before committing", "✓"} {
		if !strings.Contains(got, want) {
			t.Errorf("footer lacks %q:\n%s", want, got)
		}
	}
	if lines := strings.Split(got, "\n"); len(lines) != 2 {
		t.Errorf("footer is %d lines, want 2:\n%s", len(lines), got)
	}
	if fresh := ansi.Strip(liveModel(t).viewFooter()); !strings.Contains(fresh, "i » steer") || strings.Contains(fresh, "last:") {
		t.Errorf("footer before any instruction:\n%s", fresh)
	}
}
