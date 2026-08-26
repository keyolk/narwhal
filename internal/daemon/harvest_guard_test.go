package daemon

import (
	"testing"

	"github.com/keyolk/narwhal/internal/broker"
)

// The outcome file is read by two paths with different guards. Adoption
// (adopt.go) accepts any task that is not already completed or failed —
// pending and ready included. The per-tick sweep (harvest.go) accepts only
// dispatched. So whether a run recovers depends on which path happened to
// run, not on what is on disk: a pending task with an outcome file is
// harvested if the daemon restarts and ignored forever if it merely ticks.
//
// The deeper problem is what the file does not say. It is named
// outcome-<taskID>.json, so it cannot name the dispatch that wrote it. A
// worker that failed, wrote a file, and was retried leaves that file behind
// for the next attempt to be completed from.

func TestAStaleOutcomeFileDoesNotCompleteALaterDispatch(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	b := broker.New()
	run := b.CreateRun("r1", "p", "/tmp", "main")
	task := run.AddTask("task-1", "n", "a", nil)

	// Attempt 1 fails after writing its answer down.
	task.StartDispatch("d1", "worker-task-1")
	writeOutcome(t, "r1", "task-1", "PARTIAL_ANSWER_FROM_ATTEMPT_1")
	task.FailDispatch("worker died", run)

	// Attempt 2 is dispatched and is doing real work.
	task.StartDispatch("d2", "worker-task-1")

	if n := harvestOrphanedOutcomes("r1", run); n > 0 {
		t.Errorf("the sweep completed a running dispatch from the previous "+
			"attempt's file (%d harvested, outcome=%q)",
			n, run.Snapshot().Tasks[0].Outcome)
	}
	if got := task.CurrentState(); got != broker.TaskDispatched {
		t.Errorf("the task is %s while its second worker is still going", got)
	}
}

func TestBothRecoveryPathsAgreeOnWhatIsHarvestable(t *testing.T) {
	// A pending task with an outcome file. Whatever the right answer is,
	// the two paths must give the same one — recovery cannot depend on
	// whether the daemon restarted or merely ticked.
	t.Setenv("HOME", t.TempDir())

	b := broker.New()
	run := b.CreateRun("r1", "p", "/tmp", "main")
	run.AddTask("task-1", "n", "a", []string{"task-0"}) // pending: dep missing
	writeOutcome(t, "r1", "task-1", "an answer from somewhere")

	sweep := harvestOrphanedOutcomes("r1", run) > 0

	// The adoption path, on the same state.
	adopted := false
	if outcome, _, ok := readOutcome("r1", "task-1"); ok {
		adopted = harvestable(run.GetTask("task-1").CurrentState())
		_ = outcome
	}

	if sweep != adopted {
		t.Errorf("the sweep says harvestable=%v and adoption says %v for the "+
			"same task and the same file", sweep, adopted)
	}
}
