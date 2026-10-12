package broker

import "testing"

func finished(t *testing.T, failed bool) (*Run, *Task) {
	t.Helper()
	r := New().CreateRun("r", "p", t.TempDir(), "main")
	task := r.AddTask("a", "a", "do a", nil)
	for i := 0; i < MaxDispatchFailures; i++ {
		task.StartDispatch("d", "worker-a")
		if !failed {
			task.CompleteDispatch("the answer", r)
			return r, task
		}
		task.FailDispatch("died", r)
	}
	if task.CurrentState() != TaskFailed {
		t.Fatalf("setup: state %s", task.CurrentState())
	}
	return r, task
}

func TestReopenPutsAFinishedTaskBackWithItsInstruction(t *testing.T) {
	r, task := finished(t, false)
	task.RecordCheckResult("checked")
	if !task.Reopen("also cover the legacy payload") {
		t.Fatal("a completed task was not reopened")
	}
	if task.CurrentState() != TaskReady || len(r.DispatchableTasks()) != 1 {
		t.Fatalf("reopened task is %s and not dispatchable", task.CurrentState())
	}
	if task.CurrentFollowUp() != "also cover the legacy payload" || task.PreviousOutcome() != "the answer" {
		t.Errorf("follow-up %q, previous %q", task.CurrentFollowUp(), task.PreviousOutcome())
	}
	if task.snapshot().CheckResult != "" {
		t.Error("the check result for the old work survived the reopen")
	}
	// A second instruction before the worker starts joins the first.
	task.Reopen("and log it")
	if got := task.CurrentFollowUp(); got != "also cover the legacy payload\n\nand log it" {
		t.Errorf("follow-ups = %q", got)
	}
	task.StartDispatch("d2", "worker-a")
	task.CompleteDispatch("the new answer", r)
	if task.CurrentFollowUp() != "" {
		t.Error("the follow-up outlived the task completing again")
	}
}

func TestReopenLeavesUnfinishedTasksAlone(t *testing.T) {
	r := New().CreateRun("r", "p", t.TempDir(), "main")
	running := r.AddTask("a", "a", "x", nil)
	running.StartDispatch("d", "worker-a")
	ready := r.AddTask("b", "b", "x", nil)
	if running.Reopen("x") || ready.Reopen("x") {
		t.Error("an unfinished task was reopened")
	}
	if running.CurrentFollowUp() != "" || ready.CurrentFollowUp() != "" {
		t.Error("an unfinished task took a follow-up")
	}
}

func TestAReopenedFailedTaskGetsItsOwnRetries(t *testing.T) {
	r, task := finished(t, true)
	task.Reopen("try with the other endpoint")
	task.StartDispatch("d", "worker-a")
	task.FailDispatch("died again", r)
	if task.CurrentState() != TaskReady {
		t.Fatalf("one failure after a reopen failed the task: the old failures were counted")
	}
}

func TestAFollowUpSurvivesARestart(t *testing.T) {
	r, task := finished(t, false)
	task.Reopen("also cover the legacy payload")
	restored := RestoreRun(r.Snapshot())
	got := restored.GetTask("a")
	if got.CurrentState() != TaskReady || got.CurrentFollowUp() != "also cover the legacy payload" {
		t.Errorf("restored: %s, %q", got.CurrentState(), got.CurrentFollowUp())
	}
}
