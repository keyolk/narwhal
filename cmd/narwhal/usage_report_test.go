package main

import (
	"strings"
	"testing"

	"github.com/keyolk/narwhal/internal/broker"
)

// The mismatch note is the finding this accounting exists to surface: a
// task asked for one tier and something else served it, because ccproxy
// routes on account and quota rather than on what narwhal requested.
func TestUsageReportCallsOutATierItDidNotGet(t *testing.T) {
	s := broker.Snapshot{Tasks: []broker.TaskSnapshot{{
		ID: "synthesis", State: broker.TaskCompleted, Dispatches: 1, Model: "opus",
		Usage: &broker.Usage{ServedModel: "claude-haiku-4-5-20251001", OutputTokens: 4075, Turns: 15},
	}}}
	got := UsageReport(s)
	if !strings.Contains(got, "asked for opus") {
		t.Errorf("report does not flag the mismatch:\n%s", got)
	}
	if !strings.Contains(got, "claude-haiku-4-5-20251001") {
		t.Errorf("report does not name the model that served:\n%s", got)
	}
}

// A task served by the tier it asked for is not news and must not be
// decorated as though something went wrong.
func TestUsageReportIsQuietWhenTheTierMatches(t *testing.T) {
	s := broker.Snapshot{Tasks: []broker.TaskSnapshot{{
		ID: "a", State: broker.TaskCompleted, Dispatches: 1, Model: "haiku",
		Usage: &broker.Usage{ServedModel: "claude-haiku-4-5-20251001", OutputTokens: 10, Turns: 1},
	}}}
	if got := UsageReport(s); strings.Contains(got, "asked for") {
		t.Errorf("report flags a match as a mismatch:\n%s", got)
	}
}

// A task with no requested tier took the launcher default. There is
// nothing to compare it against, so there is nothing to flag.
func TestUsageReportDoesNotFlagATaskThatNamedNoTier(t *testing.T) {
	s := broker.Snapshot{Tasks: []broker.TaskSnapshot{{
		ID: "a", State: broker.TaskCompleted, Dispatches: 1,
		Usage: &broker.Usage{ServedModel: "claude-opus-5", OutputTokens: 10, Turns: 1},
	}}}
	if got := UsageReport(s); strings.Contains(got, "asked for") {
		t.Errorf("report flags a task that requested no tier:\n%s", got)
	}
}

// A dispatched task with no tally is a hole in the accounting. Reporting
// the total without saying so presents a partial number as the whole cost
// of the run — 80 of the dispatched tasks on this machine are in exactly
// this state, from before the launcher pinned session ids.
func TestUsageReportNamesWhatItCouldNotMeasure(t *testing.T) {
	s := broker.Snapshot{Tasks: []broker.TaskSnapshot{
		{ID: "a", State: broker.TaskCompleted, Dispatches: 1,
			Usage: &broker.Usage{OutputTokens: 100, Turns: 2}},
		{ID: "b", State: broker.TaskCompleted, Dispatches: 1},
	}}
	got := UsageReport(s)
	if !strings.Contains(got, "unmeasured") {
		t.Errorf("report does not name the unmeasured task:\n%s", got)
	}
	if !strings.Contains(got, "floor") {
		t.Errorf("report does not say the total is a floor:\n%s", got)
	}
}

// A task that was never dispatched cost nothing and is not a gap.
func TestUsageReportIgnoresATaskThatNeverRan(t *testing.T) {
	s := broker.Snapshot{Tasks: []broker.TaskSnapshot{
		{ID: "a", State: broker.TaskCompleted, Dispatches: 1,
			Usage: &broker.Usage{OutputTokens: 100, Turns: 2}},
		{ID: "b", State: broker.TaskPending},
	}}
	got := UsageReport(s)
	if strings.Contains(got, "unmeasured") || strings.Contains(got, "floor") {
		t.Errorf("a never-dispatched task is reported as a gap:\n%s", got)
	}
}

// A run from before accounting existed has nothing to show, and an empty
// section is worse than none.
func TestUsageReportIsEmptyForAnUnaccountedRun(t *testing.T) {
	s := broker.Snapshot{Tasks: []broker.TaskSnapshot{{ID: "a", State: broker.TaskCompleted}}}
	if got := UsageReport(s); got != "" {
		t.Errorf("report = %q, want empty", got)
	}
}

func TestUsageLineIsEmptyWhenNothingWasMeasured(t *testing.T) {
	s := broker.Snapshot{Tasks: []broker.TaskSnapshot{{ID: "a", State: broker.TaskCompleted, Dispatches: 1}}}
	if got := usageLine(s); got != "" {
		t.Errorf("usageLine = %q, want empty when nothing is measured", got)
	}
}

// A session served by two models is reported as both: a fallback
// mid-session is the fact worth keeping, not noise to round off.
func TestUsageReportShowsEveryModelThatServed(t *testing.T) {
	s := broker.Snapshot{Tasks: []broker.TaskSnapshot{{
		ID: "a", State: broker.TaskCompleted, Dispatches: 1,
		Usage: &broker.Usage{
			ServedModel:  "claude-opus-5",
			ServedModels: []string{"claude-opus-5", "claude-haiku-4-5-20251001"},
			OutputTokens: 10, Turns: 2,
		},
	}}}
	got := UsageReport(s)
	if !strings.Contains(got, "claude-opus-5+claude-haiku-4-5-20251001") {
		t.Errorf("report does not show both models:\n%s", got)
	}
}

func TestHumanTokensSpansTheRangeItHasTo(t *testing.T) {
	cases := map[int64]string{
		131: "131", 1500: "1.5k", 6_025_323: "6.0M", 0: "0",
	}
	for n, want := range cases {
		if got := humanTokens(n); got != want {
			t.Errorf("humanTokens(%d) = %q, want %q", n, got, want)
		}
	}
}
