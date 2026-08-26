package daemon

import (
	"testing"
	"time"

	"github.com/keyolk/narwhal/internal/broker"
)

// narwhal_cancel killed the workers and set RunCanceled, and nothing read
// that state back. The dispatch tick kept launching: a run canceled before
// its first task started would start it anyway, and a killed worker was
// recorded as a failed dispatch — blaming the task for the user's decision
// and driving a retry.

func canceledRun(t *testing.T, id string) (*Session, *broker.Run) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	stubWorker(t)

	sess := NewSession()
	sess.URL = "http://127.0.0.1:1"
	run := sess.Broker.CreateRun(id, "test", t.TempDir(), "main")
	run.CreateStandardThreads()
	sess.LauncherFor(id, run.CWD)
	return sess, run
}

func TestCanceledRunStopsDispatching(t *testing.T) {
	sess, run := canceledRun(t, "r-cancel")
	run.AddTask("a", "a", "do a", nil) // ready, never dispatched
	run.SetState(broker.RunCanceled)

	d := NewDispatcher(sess)
	d.Start()
	defer d.Stop()
	time.Sleep(1500 * time.Millisecond)

	if got := run.GetTask("a").DispatchCount(); got != 0 {
		t.Fatalf("a canceled run dispatched %d time(s)", got)
	}
}

func TestCancelRetiresUnfinishedTasks(t *testing.T) {
	// A task left ready is one the dispatcher picks up again the moment
	// anything changes; one left dispatched describes a worker that no
	// longer exists.
	_, run := canceledRun(t, "r-retire")
	ready := run.AddTask("ready", "ready", "x", nil)
	running := run.AddTask("running", "running", "x", nil)
	running.StartDispatch("d1", "worker-running")
	done := run.AddTask("done", "done", "x", nil)
	done.StartDispatch("d1", "worker-done")
	done.CompleteDispatch("finished", run)

	run.SetState(broker.RunCanceled)
	for _, snap := range run.SnapshotTasks() {
		switch snap.State {
		case broker.TaskCompleted, broker.TaskFailed:
		default:
			run.GetTask(snap.ID).CancelDispatch("run canceled", run)
		}
	}

	if got := ready.CurrentState(); got != broker.TaskFailed {
		t.Errorf("ready task after cancel = %s, want failed", got)
	}
	if got := running.CurrentState(); got != broker.TaskFailed {
		t.Errorf("running task after cancel = %s, want failed", got)
	}
	// A finished task keeps its result: cancelling the run does not
	// un-answer what was already answered.
	if got := done.CurrentState(); got != broker.TaskCompleted {
		t.Errorf("completed task after cancel = %s, want completed", got)
	}
}

func TestCancelDispatchDoesNotReopenTheTask(t *testing.T) {
	// FailDispatch returns a task to ready so the breaker can retry it.
	// That is exactly wrong here — becoming ready again is how a canceled
	// run kept relaunching.
	b := broker.New()
	run := b.CreateRun("r-reopen", "test", "/tmp", "main")
	task := run.AddTask("a", "a", "x", nil)
	task.StartDispatch("d1", "worker-a")

	task.CancelDispatch("run canceled", run)

	if got := task.CurrentState(); got != broker.TaskFailed {
		t.Fatalf("state = %s, want failed and staying there", got)
	}
	if got := run.DispatchableTasks(); len(got) != 0 {
		t.Errorf("a canceled task is still dispatchable: %d", len(got))
	}
}

func TestCanceledRunIsStillPersisted(t *testing.T) {
	// Skipping the dispatch work must not skip the record: the final state
	// of a canceled run is worth keeping.
	sess, run := canceledRun(t, "r-cancel-persist")
	run.AddTask("a", "a", "do a", nil)
	run.SetState(broker.RunCanceled)

	d := NewDispatcher(sess)
	d.tick()

	if !d.persistedAtLeastOnce("r-cancel-persist") {
		t.Error("a canceled run was never written to disk")
	}
}

// persistedAtLeastOnce reports whether the dispatcher has a saved
// fingerprint for a run, which it only has after writing it.
func (d *Dispatcher) persistedAtLeastOnce(runID string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, ok := d.saved[runID]
	return ok
}
