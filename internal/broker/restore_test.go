package broker

import "testing"

func TestRestoringDoesNotSpendARetryThatWasNotSpent(t *testing.T) {
	// Marking every restored dispatch failed would put a task on its first
	// attempt one failure from the circuit breaker — a restart would
	// quietly consume retries the task had not used.
	r := RestoreRun(Snapshot{
		RunID: "r1", State: RunActive,
		Tasks: []TaskSnapshot{{ID: "a", State: TaskDispatched, Dispatches: 1}},
	})

	failed := 0
	for _, d := range r.GetTask("a").Dispatches {
		if d.Status == DispatchFailed {
			failed++
		}
	}
	if failed != 0 {
		t.Errorf("a task on its first dispatch was restored with %d failures", failed)
	}
}

func TestRestoringKeepsFailuresAlreadySpent(t *testing.T) {
	// The other direction: a task that burned two attempts and is on its
	// third must not be handed those two back.
	r := RestoreRun(Snapshot{
		RunID: "r1", State: RunActive,
		Tasks: []TaskSnapshot{{ID: "a", State: TaskDispatched, Dispatches: 3}},
	})

	failed := 0
	for _, d := range r.GetTask("a").Dispatches {
		if d.Status == DispatchFailed {
			failed++
		}
	}
	if failed != 2 {
		t.Errorf("restored %d failures, want the 2 already spent", failed)
	}
}

func TestADispatchedTaskAlwaysHasSomewhereToPutItsResult(t *testing.T) {
	// A snapshot from before the dispatch count was recorded leaves a
	// dispatched task with no Dispatch at all, and a harvested outcome has
	// nothing to attach to — it is silently dropped.
	r := RestoreRun(Snapshot{
		RunID: "r1", State: RunActive,
		Tasks: []TaskSnapshot{{ID: "a", State: TaskDispatched}},
	})

	task := r.GetTask("a")
	if len(task.Dispatches) == 0 {
		t.Fatal("a dispatched task was restored with no dispatch to record into")
	}
	task.CompleteDispatch("the finding", r)
	if got := task.Dispatches[len(task.Dispatches)-1].Output; got != "the finding" {
		t.Errorf("the outcome did not survive: %q", got)
	}
}
