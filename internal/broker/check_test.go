package broker

import "testing"

func TestATaskWithNoCheckIsNotWaitingOnOne(t *testing.T) {
	b := New()
	run := b.CreateRun("r1", "p", "/tmp", "main")
	task := run.AddTask("a", "a", "x", nil)
	if task.NeedsCheckResult() {
		t.Error("a task with no check is waiting for an answer to nothing")
	}
}

func TestATaskWithACheckWaitsUntilItIsAnswered(t *testing.T) {
	b := New()
	run := b.CreateRun("r1", "p", "/tmp", "main")
	task := run.AddTask("a", "a", "x", nil)
	task.SetCheck("confirm the names are exported")

	if !task.NeedsCheckResult() {
		t.Fatal("a task given a check is not waiting for its answer")
	}
	if !task.RecordCheckResult("none of them is") {
		t.Error("RecordCheckResult refused a task that has a check")
	}
	if task.NeedsCheckResult() {
		t.Error("the task is still waiting after answering")
	}
}

// Recording against a task that was asked nothing must be visible as a
// no-op rather than silently inventing an answer.
func TestRecordingAResultForATaskWithNoCheckIsRefused(t *testing.T) {
	b := New()
	run := b.CreateRun("r1", "p", "/tmp", "main")
	task := run.AddTask("a", "a", "x", nil)
	if task.RecordCheckResult("I checked, honest") {
		t.Error("RecordCheckResult accepted a result for a task with no check")
	}
	for _, ts := range run.SnapshotTasks() {
		if ts.ID == "a" && ts.CheckResult != "" {
			t.Errorf("CheckResult = %q, want empty", ts.CheckResult)
		}
	}
}

func TestCheckSurvivesARestore(t *testing.T) {
	restored := RestoreRun(Snapshot{
		RunID: "r1", Prompt: "p", CWD: "/tmp", State: RunActive,
		Tasks: []TaskSnapshot{{
			ID: "a", Name: "a", State: TaskDispatched, Dispatches: 1,
			Check: "confirm the names are exported",
		}},
	})
	task := restored.GetTask("a")
	if got := task.CurrentCheck(); got != "confirm the names are exported" {
		t.Errorf("CurrentCheck = %q, want the persisted check", got)
	}
	// A restored task that lost its check would complete without being
	// asked — the gate would have nothing to hand back.
	if !task.NeedsCheckResult() {
		t.Error("a restored task with an unanswered check is not waiting for it")
	}
}

// A task that answered before the restart must not be asked again for
// work it already did.
func TestAnAnsweredCheckSurvivesARestore(t *testing.T) {
	restored := RestoreRun(Snapshot{
		RunID: "r1", Prompt: "p", CWD: "/tmp", State: RunActive,
		Tasks: []TaskSnapshot{{
			ID: "a", Name: "a", State: TaskDispatched, Dispatches: 1,
			Check: "confirm", CheckResult: "confirmed, 3 of 3",
		}},
	})
	if restored.GetTask("a").NeedsCheckResult() {
		t.Error("a restored task is being asked for a check it already answered")
	}
}

func TestSetCheckTrimsSurroundingSpace(t *testing.T) {
	b := New()
	run := b.CreateRun("r1", "p", "/tmp", "main")
	task := run.AddTask("a", "a", "x", nil)
	task.SetCheck("   \n  ")
	if task.NeedsCheckResult() {
		t.Error("whitespace was accepted as a check")
	}
}
