package main

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/keyolk/narwhal/internal/store"
)

func TestAbbreviatePath(t *testing.T) {
	t.Setenv("HOME", "/Users/x")
	cases := []struct{ in, want string }{
		{"", "—"},
		{"/Users/x", "~"},
		{"/Users/x/src", "~/src"},
		{"/Users/x/src/keyolk/narwhal", ".../keyolk/narwhal"},
		{"/tmp", "/tmp"},
	}
	for _, c := range cases {
		if got := abbreviatePath(c.in); got != c.want {
			t.Errorf("abbreviatePath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestRunStartTimePrefersStartedAt(t *testing.T) {
	want := time.Now().Add(-time.Hour).Truncate(time.Second)
	got := runStartTime(store.LiveRun{RunID: "s1786472797321-1", StartedAt: want.Unix()})
	if !got.Equal(want) {
		t.Fatalf("runStartTime = %v, want %v", got, want)
	}
}

func TestRunStartTimeFallsBackToTheRunID(t *testing.T) {
	// Daemon runs carry no StartedAt, but every id ends in the millisecond
	// timestamp it was minted from. Showing the epoch instead would make
	// every daemon run look like 1970.
	for _, id := range []string{"s1786472797321-1", "plan-1786543427573", "run-1786593102332"} {
		got := runStartTime(store.LiveRun{RunID: id})
		if got.IsZero() {
			t.Errorf("runStartTime(%q) is zero; want the id's timestamp", id)
			continue
		}
		if got.Year() < 2020 || got.Year() > 2100 {
			t.Errorf("runStartTime(%q) = %v, outside a plausible range", id, got)
		}
	}
}

func TestRunStartTimeIsZeroForAnUnparseableID(t *testing.T) {
	if got := runStartTime(store.LiveRun{RunID: "handmade"}); !got.IsZero() {
		t.Fatalf("runStartTime = %v, want zero for an id with no timestamp", got)
	}
}

func TestPickerIsStableWhenPollsArriveShuffled(t *testing.T) {
	// The daemon keeps runs in a map and Go randomizes map iteration, so
	// consecutive polls can deliver the same runs in a different order.
	// Rendering that order directly makes the picker flicker once a second
	// and moves rows out from under the cursor.
	runs := []store.LiveRun{
		{RunID: "s3-1", BrokerURL: "u", Prompt: "c", CWD: "/x", StartedAt: 300},
		{RunID: "s1-1", BrokerURL: "u", Prompt: "a", CWD: "/x", StartedAt: 100},
		{RunID: "s2-1", BrokerURL: "u", Prompt: "b", CWD: "/x", StartedAt: 200},
	}
	m := newTUIModel(runs, 0, time.Second, true)
	m.width, m.height = 100, 24

	first := m.View()
	for _, order := range [][]int{{2, 0, 1}, {1, 2, 0}, {0, 1, 2}} {
		shuffled := make([]store.LiveRun, 0, len(runs))
		for _, i := range order {
			shuffled = append(shuffled, runs[i])
		}
		m.mergeRuns(shuffled)
		if got := m.View(); got != first {
			t.Fatalf("picker changed between polls:\nfirst:\n%s\ngot:\n%s", first, got)
		}
	}
}

func TestPickerListsNewestRunFirst(t *testing.T) {
	runs := []store.LiveRun{
		{RunID: "old", BrokerURL: "u", Prompt: "older", StartedAt: 100},
		{RunID: "new", BrokerURL: "u", Prompt: "newer", StartedAt: 900},
	}
	m := newTUIModel(runs, 0, time.Second, true)
	if m.runs[0].RunID != "new" {
		t.Fatalf("first row = %q, want the newest run", m.runs[0].RunID)
	}
}

func TestPickerRowsDoNotLeakStyleAcrossLines(t *testing.T) {
	// Regression: the unselected row was assembled with an already-styled
	// fragment and then passed to truncate(), which counts escape bytes as
	// content. A cut landing inside an escape drops its reset, and the
	// style bleeds into every row below — on screen, several rows looked
	// selected at once.
	forceColor(t)

	runs := make([]store.LiveRun, 4)
	for i := range runs {
		runs[i] = store.LiveRun{
			RunID:     fmt.Sprintf("s%d-1", i),
			BrokerURL: "u",
			Prompt:    fmt.Sprintf("run number %d", i),
			CWD:       "/tmp/nw-flicker",
			StartedAt: int64(100 - i),
		}
	}
	m := newTUIModel(runs, 0, time.Second, true)
	// Narrow enough that rows have to be cut.
	m.width, m.height = 40, 24

	for _, line := range strings.Split(m.View(), "\n") {
		if strings.Count(line, "\x1b[") == 0 {
			continue
		}
		// Every style opened on a line must be closed on that line.
		if !strings.HasSuffix(stripTrailingSpace(line), "\x1b[0m") {
			t.Fatalf("line ends without a reset, style will bleed:\n%q", line)
		}
	}
}

// stripTrailingSpace drops padding so the reset check looks at the last
// meaningful byte.
func stripTrailingSpace(s string) string {
	return strings.TrimRight(s, " ")
}
