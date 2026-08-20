// adopt.go reloads runs that were in flight when the daemon last stopped.
//
// The daemon held everything in memory and started empty every time, so a
// restart abandoned whatever it was running. The workers did not stop —
// they are detached processes with the broker's URL baked into their
// wrapper scripts — so they kept working and reported to a port that no
// longer answered.
//
// The cost is not hypothetical. One orphaned run had four workers; three
// of them finished, wrote their reports, and could not deliver them:
//
//	worker-blast-radius      outcome-blast-radius.json      6430 bytes
//	worker-iac-path          outcome-iac-path.json          1471 bytes
//	worker-native-migration  outcome-native-migration.json  2091 bytes
//
// The task-done script writes that file precisely so "the run can be
// reconciled from disk" when the broker is unreachable. Nothing reconciled
// it. This is that missing half.
package daemon

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/keyolk/narwhal/internal/broker"
	"github.com/keyolk/narwhal/internal/store"
)

// AdoptOutcome describes what adoption decided about one task.
type AdoptOutcome struct {
	TaskID string
	// Verdict is one of "harvested", "running", or "abandoned".
	Verdict string
}

// AdoptResult reports what a run's adoption recovered.
type AdoptResult struct {
	RunID     string
	Harvested int // completed from an outcome file the broker never saw
	Running   int // worker still alive, left dispatched
	Abandoned int // neither an outcome nor a process
	Tasks     []AdoptOutcome
}

// AdoptRuns reloads every unfinished run from disk into the session.
//
// Called once at daemon start, before the dispatcher runs. Adoption has to
// come first: the dispatcher's reap treats a dispatched task with no
// tracked worker as a failed dispatch and retries it, so a run adopted
// after the first tick would have its still-running work launched a second
// time — the duplicate the whole exercise is meant to prevent.
//
// The returned map is taskKey → agentID for tasks whose workers survived,
// which the dispatcher seeds its running set with for exactly that reason.
func AdoptRuns(sess *Session) ([]AdoptResult, map[string]string) {
	dispatched := map[string]string{}
	ids, err := store.ListRuns()
	if err != nil {
		log.Printf("[adopt] list runs: %v", err)
		return nil, dispatched
	}

	var results []AdoptResult
	for _, id := range ids {
		snap, err := store.LoadRun(id)
		if err != nil {
			continue
		}
		if !worthAdopting(snap) {
			continue
		}
		results = append(results, adoptRun(sess, snap, dispatched))
	}
	return results, dispatched
}

// worthAdopting reports whether a snapshot describes a run left mid-flight.
//
// A run the daemon retired is history; reloading it would fill the broker
// with every run ever executed and put finished work back in front of the
// dispatcher.
func worthAdopting(s broker.Snapshot) bool {
	if s.RunID == "" || len(s.Tasks) == 0 {
		return false
	}
	switch s.State {
	case broker.RunDone, broker.RunFailed, broker.RunCanceled:
		return false
	}
	for _, t := range s.Tasks {
		if t.State != broker.TaskCompleted && t.State != broker.TaskFailed {
			return true
		}
	}
	// Every task settled but the run was never marked done — the snapshot
	// was written just before the daemon died. Retiring it is the
	// dispatcher's job and it will do it on the first tick.
	return true
}

// adoptRun restores one run and classifies each unfinished task.
//
// Classification is the whole point. A dispatched task with no worker is
// ambiguous on its face, and guessing either way is expensive: assume it
// died and you re-run work that is still going; assume it lives and a
// crashed task hangs forever. So ask the two things that actually know —
// the outcome file the worker writes when it cannot reach the broker, and
// whether its process is still there.
func adoptRun(sess *Session, snap broker.Snapshot, dispatched map[string]string) AdoptResult {
	run := broker.RestoreRun(snap)
	sess.Broker.AdoptRun(run)
	// A launcher is what makes a run visible to the dispatch loop, and it
	// carries the cwd the run's workers were started in.
	sess.LauncherFor(snap.RunID, snap.CWD)

	res := AdoptResult{RunID: snap.RunID}
	for _, ts := range snap.Tasks {
		if ts.State == broker.TaskCompleted || ts.State == broker.TaskFailed {
			continue
		}
		task := run.GetTask(ts.ID)
		if task == nil {
			continue
		}

		if outcome, ok := readOutcome(snap.RunID, ts.ID); ok {
			// The work is done and was written down; only the delivery
			// failed. Completing it here is the reconciliation the
			// task-done script promised the worker.
			task.CompleteDispatch(outcome, run)
			res.Harvested++
			res.Tasks = append(res.Tasks, AdoptOutcome{ts.ID, "harvested"})
			continue
		}

		if workerAlive(snap.RunID, ts.ID) {
			// Still going. Leave it dispatched and hand the key back so
			// the dispatcher tracks it as busy — otherwise the next tick
			// calls it a failed dispatch and launches a second worker on
			// a task that is already being worked.
			dispatched[runKey(snap.RunID, ts.ID)] = "worker-" + ts.ID
			res.Running++
			res.Tasks = append(res.Tasks, AdoptOutcome{ts.ID, "running"})
			continue
		}

		// No result and no process. The task is genuinely unfinished, and
		// it stops here rather than being retried: an adopted run can be
		// hours old, its abandoned task may already have been redone in a
		// later run, and relaunching it on a restart spends real money on
		// work nobody asked for again. FailDispatch is the wrong tool —
		// it returns a task to ready, which is how a cancelled run kept
		// relaunching itself.
		if ts.State == broker.TaskDispatched {
			task.CancelDispatch("worker lost to a daemon restart")
		}
		res.Abandoned++
		res.Tasks = append(res.Tasks, AdoptOutcome{ts.ID, "abandoned"})
	}
	return res
}

// readOutcome returns what a worker recorded locally when it could not
// reach the broker, if anything.
func readOutcome(runID, taskID string) (string, bool) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", false
	}
	// The file lives in the agent's own directory, and the agent for a
	// task is named after it.
	path := filepath.Join(home, ".narwhal", "sessions", runID,
		"agents", "worker-"+taskID, "outcome-"+taskID+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	// The script writes the same JSON body it would have posted.
	var body struct {
		Outcome string `json:"outcome"`
	}
	if json.Unmarshal(data, &body) != nil || strings.TrimSpace(body.Outcome) == "" {
		return "", false
	}
	return body.Outcome, true
}

// workerAlive reports whether a task's worker process is still running.
//
// The pid is read from disk rather than remembered, because the process
// that remembered it is the one that died.
func workerAlive(runID, taskID string) bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	path := filepath.Join(home, ".narwhal", "sessions", runID,
		"agents", "worker-"+taskID, "claude-pid")
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// Signal 0 checks for existence without delivering anything.
	return proc.Signal(syscall.Signal(0)) == nil
}

// Summary renders an adoption result for the daemon's startup log.
func (r AdoptResult) Summary() string {
	parts := []string{}
	if r.Harvested > 0 {
		parts = append(parts, fmt.Sprintf("%d harvested", r.Harvested))
	}
	if r.Running > 0 {
		parts = append(parts, fmt.Sprintf("%d still running", r.Running))
	}
	if r.Abandoned > 0 {
		parts = append(parts, fmt.Sprintf("%d abandoned", r.Abandoned))
	}
	if len(parts) == 0 {
		return r.RunID + ": nothing outstanding"
	}
	return r.RunID + ": " + strings.Join(parts, ", ")
}
