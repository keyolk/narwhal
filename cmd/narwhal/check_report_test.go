package main

import (
	"strings"
	"testing"

	"github.com/keyolk/narwhal/internal/broker"
)

func TestCheckReportShowsWhatWasAskedAndWhatItShowed(t *testing.T) {
	s := broker.Snapshot{Tasks: []broker.TaskSnapshot{{
		ID: "synthesis", State: broker.TaskCompleted,
		Check:       "confirm the names are exported",
		CheckResult: "none of the six is",
	}}}
	got := CheckReport(s)
	if !strings.Contains(got, "confirm the names are exported") {
		t.Errorf("report omits the check:\n%s", got)
	}
	if !strings.Contains(got, "none of the six is") {
		t.Errorf("report omits the result:\n%s", got)
	}
}

// A completed task with an unanswered check went through harvest, where
// the broker was gone and the worker had already exited. An empty line
// there reads like the check passed.
func TestCheckReportNamesAnUnansweredCheck(t *testing.T) {
	s := broker.Snapshot{Tasks: []broker.TaskSnapshot{{
		ID: "task-1", State: broker.TaskCompleted, Check: "confirm something",
	}}}
	if got := CheckReport(s); !strings.Contains(got, "not answered") {
		t.Errorf("an unanswered check is not named:\n%s", got)
	}
}

func TestCheckReportIsEmptyWhenNoTaskCarriedOne(t *testing.T) {
	s := broker.Snapshot{Tasks: []broker.TaskSnapshot{{ID: "a", State: broker.TaskCompleted}}}
	if got := CheckReport(s); got != "" {
		t.Errorf("report = %q, want empty", got)
	}
}

// Asked and answered fail differently: none asked means the planner set
// none, asked-and-unanswered means the run completed off a path that
// could not ask.
func TestCheckLineCountsAskedAndAnsweredSeparately(t *testing.T) {
	s := broker.Snapshot{Tasks: []broker.TaskSnapshot{
		{ID: "a", Check: "x", CheckResult: "done"},
		{ID: "b", Check: "y"},
		{ID: "c"},
	}}
	if got := checkLine(s); got != "checks 1/2 answered" {
		t.Errorf("checkLine = %q, want \"checks 1/2 answered\"", got)
	}
}

func TestCheckLineIsEmptyWithNoChecks(t *testing.T) {
	s := broker.Snapshot{Tasks: []broker.TaskSnapshot{{ID: "a"}}}
	if got := checkLine(s); got != "" {
		t.Errorf("checkLine = %q, want empty", got)
	}
}
