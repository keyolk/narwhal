package daemon

import (
	"testing"

	"github.com/keyolk/narwhal/internal/broker"
)

// A worker whose broker went away finishes anyway: task-done retries four
// times and then writes its answer to outcome-<task>.json so the result is
// not lost. AdoptRuns reads that file — but AdoptRuns runs once, at daemon
// startup, so an outcome written *after* adoption had nobody to read it.
//
// Measured on a live daemon: a worker was started, the daemon was restarted
// under it (FORCE=1, deliberately), and the worker finished into the dead
// port. It wrote {"outcome":"L3_SURVIVED"} to disk; the task stayed
// `dispatched` for the two minutes it was polled and only flipped to
// `completed` when the daemon happened to be restarted again. On a run
// nobody restarts, that wait is however long it is.

func TestATickHarvestsAnOutcomeWrittenAfterStartup(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	b := broker.New()
	run := b.CreateRun("r-harvest", "p", "/tmp", "main")
	task := run.AddTask("task-1", "n", "a", nil)
	task.StartDispatch("d1", "worker-task-1")

	// The worker gave up on the broker and wrote its answer down.
	writeOutcome(t, "r-harvest", "task-1", "L3_SURVIVED")

	if got := harvestOrphanedOutcomes("r-harvest", run); got != 1 {
		t.Fatalf("the sweep harvested %d outcomes, want 1", got)
	}
	if got := task.CurrentState(); got != broker.TaskCompleted {
		t.Errorf("the task is %s after its outcome was on disk", got)
	}
	if got := run.Snapshot().Tasks[0].Outcome; got != "L3_SURVIVED" {
		t.Errorf("the harvested outcome is %q", got)
	}
}

func TestTheSweepLeavesARunningWorkerAlone(t *testing.T) {
	// No outcome file means the worker is still going. Completing it here
	// would end a task that is mid-flight.
	t.Setenv("HOME", t.TempDir())

	b := broker.New()
	run := b.CreateRun("r-live", "p", "/tmp", "main")
	task := run.AddTask("task-1", "n", "a", nil)
	task.StartDispatch("d1", "worker-task-1")

	if got := harvestOrphanedOutcomes("r-live", run); got != 0 {
		t.Errorf("the sweep harvested %d with no outcome on disk", got)
	}
	if got := task.CurrentState(); got != broker.TaskDispatched {
		t.Errorf("a running task was moved to %s", got)
	}
}

func TestHarvestingIsNotRepeated(t *testing.T) {
	// The file stays on disk after the task completes, and the sweep runs
	// every tick. A second harvest would re-complete a finished task on
	// every tick for the life of the daemon.
	t.Setenv("HOME", t.TempDir())

	b := broker.New()
	run := b.CreateRun("r-once", "p", "/tmp", "main")
	task := run.AddTask("task-1", "n", "a", nil)
	task.StartDispatch("d1", "worker-task-1")
	writeOutcome(t, "r-once", "task-1", "done once")

	if got := harvestOrphanedOutcomes("r-once", run); got != 1 {
		t.Fatalf("first sweep harvested %d, want 1", got)
	}
	if got := harvestOrphanedOutcomes("r-once", run); got != 0 {
		t.Errorf("a second sweep harvested the same outcome %d more times", got)
	}
	if got := task.CurrentState(); got != broker.TaskCompleted {
		t.Errorf("task ended up %s", got)
	}
}

func TestTheRunningDaemonHarvestsWithoutARestart(t *testing.T) {
	// The whole point: the sweep has to be wired into the tick. The unit
	// tests above pass whether or not tick calls it, so this drives the
	// real dispatcher and asserts a task completes while the daemon runs.
	t.Setenv("HOME", t.TempDir())
	stubWorker(t)

	sess := NewSession()
	sess.URL = "http://127.0.0.1:1"
	run := sess.Broker.CreateRun("r-live-sweep", "test", t.TempDir(), "main")
	sess.LauncherFor("r-live-sweep", run.CWD)
	task := run.AddTask("task-1", "n", "do a", nil)

	d := NewDispatcher(sess)
	d.Start()
	defer d.Stop()

	waitFor(t, "the task to be dispatched", func() bool {
		return task.DispatchCount() > 0
	})

	// The worker could not reach the broker and wrote its answer down.
	writeOutcome(t, "r-live-sweep", "task-1", "SWEEP_WORKS")

	waitFor(t, "the running daemon to harvest it", func() bool {
		return task.CurrentState() == broker.TaskCompleted
	})
	if got := run.Snapshot().Tasks[0].Outcome; got != "SWEEP_WORKS" {
		t.Errorf("harvested outcome is %q", got)
	}
}
