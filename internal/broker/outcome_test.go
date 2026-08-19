package broker

import (
	"reflect"
	"testing"
)

// What a worker declared at task-done is the run's product. It was stored on
// the dispatch and read by nobody: absent from Snapshot, so absent from the
// API, the monitor, and every persisted run file on disk. A restarted daemon
// came back with a graph of completed tasks and no record of what any of them
// concluded.
//
// The model tier went the same way, one function apart: SnapshotTasks carried
// it and Snapshot did not, so 0 of 143 tasks across 36 persisted runs had a
// model recorded — including the ones MODEL_ESCALATE had moved.

func completedRun(t *testing.T) *Run {
	t.Helper()
	b := New()
	r := b.CreateRun("r1", "p", "/tmp", "main")
	task := r.AddTask("task-1", "n", "a", nil)
	task.SetModel("opus")
	task.StartDispatch("d1", "agent-1")
	task.CompleteDispatch("the gateway serves 4 of 7 SANs", r)
	return r
}

func TestSnapshotCarriesTheOutcome(t *testing.T) {
	s := completedRun(t).Snapshot()
	if got := s.Tasks[0].Outcome; got != "the gateway serves 4 of 7 SANs" {
		t.Errorf("the outcome did not reach the snapshot: %q", got)
	}
}

func TestSnapshotCarriesTheModel(t *testing.T) {
	s := completedRun(t).Snapshot()
	if got := s.Tasks[0].Model; got != "opus" {
		t.Errorf("the model did not reach the snapshot: %q", got)
	}
}

func TestBothSnapshotsAgree(t *testing.T) {
	// Two construction sites drifted apart once already. They answer the
	// same question and must answer it the same way.
	r := completedRun(t)
	full := r.Snapshot().Tasks[0]
	tasksOnly := r.SnapshotTasks()[0]
	if !reflect.DeepEqual(full, tasksOnly) {
		t.Errorf("the two snapshot paths disagree:\n  Snapshot():      %+v\n  SnapshotTasks(): %+v",
			full, tasksOnly)
	}
}

func TestRestoreKeepsTheOutcome(t *testing.T) {
	// The point of persisting it: a restarted daemon can still say what a
	// completed task concluded.
	restored := RestoreRun(completedRun(t).Snapshot())
	got := restored.Tasks["task-1"].Dispatches
	if len(got) == 0 {
		t.Fatal("the restored task has no dispatch to hold an outcome")
	}
	if out := got[len(got)-1].Output; out != "the gateway serves 4 of 7 SANs" {
		t.Errorf("the outcome was lost across restore: %q", out)
	}
	if again := restored.Snapshot().Tasks[0].Outcome; again == "" {
		t.Error("a restored run snapshots an empty outcome")
	}
}

func TestAFailureReasonIsAnOutcomeToo(t *testing.T) {
	// FailDispatch and CancelDispatch write the reason to the same field.
	// Why a task failed is the thing an operator most wants after a restart.
	b := New()
	r := b.CreateRun("r1", "p", "/tmp", "main")
	task := r.AddTask("task-1", "n", "a", nil)
	for i := 0; i < MaxDispatchFailures; i++ {
		task.StartDispatch("d", "agent-1")
		task.FailDispatch("worker exited 1", r)
	}
	if got := r.Snapshot().Tasks[0].Outcome; got != "worker exited 1" {
		t.Errorf("the failure reason did not reach the snapshot: %q", got)
	}
}
