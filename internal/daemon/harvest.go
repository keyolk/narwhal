// harvest.go reconciles outcomes a worker wrote to disk because it could
// not deliver them to the broker.
//
// The task-done script retries the broker four times and then writes its
// answer to outcome-<task>.json, telling the worker the result is not lost.
// AdoptRuns makes good on that at daemon startup — but only there, so an
// outcome written after adoption had nobody to read it.
//
// Measured on a live daemon: a worker was started, the daemon restarted
// under it (deliberately, with FORCE=1), and the worker finished into the
// now-dead port. It wrote {"outcome":"L3_SURVIVED"} to disk; the task stayed
// `dispatched` for the two minutes it was polled and only flipped to
// `completed` when the daemon happened to be restarted again. On a run
// nobody restarts, that wait is however long it is — the work is done, paid
// for, and sitting in a file the running daemon never looks at.
package daemon

import (
	"log"

	"github.com/keyolk/narwhal/internal/broker"
)

// harvestOrphanedOutcomes completes any dispatched task whose worker left an
// outcome on disk. Returns how many were harvested.
//
// Only dispatched tasks are considered, which is also what keeps the sweep
// from repeating itself: the file stays on disk after the task completes,
// but a completed task is no longer a candidate.
//
// There is no race against a worker that is still going. The file is only
// written after task-done has failed to reach the broker four times, so its
// presence means delivery already gave up — a worker that could still
// deliver has not written one.
// harvestable says whether a task in this state may be completed from an
// outcome file.
//
// One predicate, because there were two. Adoption accepted anything not
// already completed or failed — pending and ready included — while the
// per-tick sweep accepted only dispatched. Recovery therefore depended on
// which path happened to run rather than on what was on disk: a pending
// task with an outcome file was harvested if the daemon restarted and
// ignored forever if it merely ticked.
//
// Dispatched is the honest answer. The file is written by task-done after
// four failed deliveries, so it belongs to a dispatch that was in flight;
// a task that is pending or ready has no dispatch for the outcome to
// attach to, and completing it would skip the work rather than recover it.
func harvestable(state broker.TaskState) bool {
	return state == broker.TaskDispatched
}

func harvestOrphanedOutcomes(runID string, run *broker.Run) int {
	harvested := 0
	for _, ts := range run.SnapshotTasks() {
		if !harvestable(ts.State) {
			continue
		}
		outcome, written, ok := readOutcomeStamped(runID, ts.ID)
		if !ok {
			continue
		}
		task := run.GetTask(ts.ID)
		if task == nil {
			continue
		}
		// The file is named after the task, not the attempt, so a worker
		// that failed after writing one leaves it for the next attempt to
		// be completed from. An outcome written before the dispatch now
		// running belongs to an earlier one; completing on it would
		// discard the work in flight and record the answer of the worker
		// before it.
		if started := task.DispatchStartedAt(); !written.IsZero() &&
			!started.IsZero() && written.Before(started) {
			continue
		}
		task.CompleteDispatch(outcome, run)
		harvested++
		log.Printf("[harvest] %s/%s completed from an outcome the worker "+
			"could not deliver", runID, ts.ID)
	}
	return harvested
}
