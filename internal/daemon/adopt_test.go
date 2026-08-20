package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/keyolk/narwhal/internal/broker"
	"github.com/keyolk/narwhal/internal/store"
)

// The daemon started with an empty broker every time, so a restart
// abandoned whatever it was hosting. The workers did not stop — they are
// detached processes with the old broker's URL baked into their scripts —
// so they kept working and reported to a port that no longer answered.
//
// The fixture here is the shape of a real orphan: run s1786688412910-3,
// four tasks dispatched, three of whose workers had finished and written
// their reports to outcome-*.json. The broker knew none of it.

// orphanRun writes a snapshot of a run left mid-flight under a temp HOME.
func orphanRun(t *testing.T, runID string, tasks ...broker.TaskSnapshot) broker.Snapshot {
	t.Helper()
	snap := broker.Snapshot{
		RunID: runID, Prompt: "audit the cluster", CWD: t.TempDir(),
		State: broker.RunActive, Tasks: tasks,
	}
	if err := store.SaveRun(snap); err != nil {
		t.Fatal(err)
	}
	return snap
}

// writeOutcome puts a report where task-done leaves one when it cannot
// reach the broker.
func writeOutcome(t *testing.T, runID, taskID, outcome string) {
	t.Helper()
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".narwhal", "sessions", runID, "agents", "worker-"+taskID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"outcome": outcome, "final": false})
	if err := os.WriteFile(filepath.Join(dir, "outcome-"+taskID+".json"), body, 0o600); err != nil {
		t.Fatal(err)
	}
}

// writePID records a worker pid the way the launcher does.
func writePID(t *testing.T, runID, taskID string, pid int) {
	t.Helper()
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".narwhal", "sessions", runID, "agents", "worker-"+taskID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "claude-pid"),
		[]byte(itoa(pid)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func TestAdoptionRestoresARunLeftInFlight(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	orphanRun(t, "r1",
		broker.TaskSnapshot{ID: "a", Name: "scan", State: broker.TaskCompleted},
		broker.TaskSnapshot{ID: "b", Name: "fix", State: broker.TaskDispatched, Deps: []string{"a"}},
	)

	sess := NewSession()
	if got := sess.Broker.GetRun("r1"); got != nil {
		t.Fatal("a fresh session already knew about r1")
	}

	results, _ := AdoptRuns(sess)
	if len(results) != 1 {
		t.Fatalf("adopted %d runs, want 1", len(results))
	}
	run := sess.Broker.GetRun("r1")
	if run == nil {
		t.Fatal("the run was not restored into the broker")
	}
	// Restoring must preserve what happened, not restart the graph.
	if s := run.GetTask("a").CurrentState(); s != broker.TaskCompleted {
		t.Errorf("a completed task came back as %s", s)
	}
}

func TestAnOutcomeOnDiskIsHarvested(t *testing.T) {
	// The task-done script writes this file specifically so "the run can
	// be reconciled from disk" when the broker is unreachable. Nothing
	// reconciled it, so three finished reports were lost to a restart.
	t.Setenv("HOME", t.TempDir())
	orphanRun(t, "r1",
		broker.TaskSnapshot{ID: "a", State: broker.TaskDispatched},
	)
	writeOutcome(t, "r1", "a", "MostAllocated saves $41k/mo; report at /tmp/x.md")

	sess := NewSession()
	results, _ := AdoptRuns(sess)

	if len(results) != 1 || results[0].Harvested != 1 {
		t.Fatalf("outcome not harvested: %+v", results)
	}
	task := sess.Broker.GetRun("r1").GetTask("a")
	if task.CurrentState() != broker.TaskCompleted {
		t.Errorf("a task with a recorded outcome is %s", task.CurrentState())
	}
	// The report itself has to survive, not just the fact of completion.
	d := task.Dispatches[len(task.Dispatches)-1]
	if d.Output == "" {
		t.Error("the harvested outcome was discarded")
	}
}

func TestALiveWorkerIsNotRedispatched(t *testing.T) {
	// This is the trap. A dispatched task with no tracked worker looks
	// like a failed dispatch to the reap loop, which retries it — so
	// adopting without telling the dispatcher would launch a second
	// worker on a task already being worked, automating the exact
	// duplication this is meant to prevent.
	t.Setenv("HOME", t.TempDir())
	orphanRun(t, "r1", broker.TaskSnapshot{ID: "a", State: broker.TaskDispatched})
	// Our own pid: certainly alive.
	writePID(t, "r1", "a", os.Getpid())

	sess := NewSession()
	results, running := AdoptRuns(sess)

	if len(results) != 1 || results[0].Running != 1 {
		t.Fatalf("a live worker was not recognised: %+v", results)
	}
	// Keyed the way the dispatcher keys its running map. It used to be
	// "r1/a" here and NUL-joined there, so the entry matched no run and no
	// task and the adopted worker was never reaped — a defect this
	// assertion held in place.
	if got := running[runKey("r1", "a")]; got != "worker-a" {
		t.Fatalf("the dispatcher was not told about the live worker: %q", got)
	}
	if s := sess.Broker.GetRun("r1").GetTask("a").CurrentState(); s != broker.TaskDispatched {
		t.Errorf("a task with a running worker was moved to %s", s)
	}
}

func TestATaskWithNeitherIsCalledAbandoned(t *testing.T) {
	// No outcome and no process. Leaving it dispatched would hang the run
	// forever behind a worker that does not exist; silently retrying it
	// would re-run work whose status nobody actually knows.
	t.Setenv("HOME", t.TempDir())
	orphanRun(t, "r1", broker.TaskSnapshot{ID: "a", State: broker.TaskDispatched})

	sess := NewSession()
	results, running := AdoptRuns(sess)

	if len(results) != 1 || results[0].Abandoned != 1 {
		t.Fatalf("an abandoned task was not reported: %+v", results)
	}
	if len(running) != 0 {
		t.Errorf("a dead worker was tracked as running: %v", running)
	}
	// Terminal, not ready. FailDispatch would return it to ready and the
	// next tick would launch a fresh worker — an adopted run can be hours
	// old and its work already redone, so a restart must not quietly spend
	// money re-running it.
	s := sess.Broker.GetRun("r1").GetTask("a").CurrentState()
	if s == broker.TaskDispatched {
		t.Error("an abandoned task was left dispatched behind a worker that is gone")
	}
	if s == broker.TaskReady {
		t.Error("an abandoned task was queued for a silent re-run on the next tick")
	}
	if s != broker.TaskFailed {
		t.Errorf("an abandoned task is %s, want a terminal state", s)
	}
}

func TestAStalePIDIsNotMistakenForAWorker(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	orphanRun(t, "r1", broker.TaskSnapshot{ID: "a", State: broker.TaskDispatched})
	// Above any pid this OS will assign.
	writePID(t, "r1", "a", 4194305)

	sess := NewSession()
	results, _ := AdoptRuns(sess)
	if results[0].Running != 0 {
		t.Errorf("a stale pid file was read as a live worker: %+v", results)
	}
}

func TestFinishedRunsAreNotAdopted(t *testing.T) {
	// Reloading history would fill the broker with every run ever
	// executed and put finished work back in front of the dispatcher.
	t.Setenv("HOME", t.TempDir())
	for _, st := range []broker.RunState{broker.RunDone, broker.RunFailed, broker.RunCanceled} {
		snap := broker.Snapshot{
			RunID: "r-" + string(st), State: st,
			Tasks: []broker.TaskSnapshot{{ID: "a", State: broker.TaskCompleted}},
		}
		if err := store.SaveRun(snap); err != nil {
			t.Fatal(err)
		}
	}

	sess := NewSession()
	results, _ := AdoptRuns(sess)
	if len(results) != 0 {
		t.Fatalf("adopted finished runs: %+v", results)
	}
}

func TestAdoptionKeepsTheRetryBudgetSpent(t *testing.T) {
	// A restart is not a reason to hand a task a fresh set of retries: a
	// task that has already burned its attempts is exactly the one that
	// should not get more.
	t.Setenv("HOME", t.TempDir())
	orphanRun(t, "r1", broker.TaskSnapshot{
		ID: "a", State: broker.TaskDispatched, Dispatches: 3,
	})

	sess := NewSession()
	AdoptRuns(sess)

	task := sess.Broker.GetRun("r1").GetTask("a")
	if len(task.Dispatches) != 3 {
		t.Errorf("restored %d dispatches, want the 3 already spent", len(task.Dispatches))
	}
}

func TestTheRadioSurvivesAdoption(t *testing.T) {
	// A restored run that lost its messages would have peers referring to
	// findings no reader can see.
	t.Setenv("HOME", t.TempDir())
	snap := broker.Snapshot{
		RunID: "r1", State: broker.RunActive, CWD: t.TempDir(),
		Tasks: []broker.TaskSnapshot{{ID: "a", State: broker.TaskDispatched}},
		Messages: []*broker.Message{
			{Seq: 1, Sender: "worker-a", ThreadID: "worklog", Content: "found the leak"},
			{Seq: 2, Sender: "worker-b", ThreadID: "worklog", Content: "confirmed"},
		},
	}
	if err := store.SaveRun(snap); err != nil {
		t.Fatal(err)
	}

	sess := NewSession()
	AdoptRuns(sess)

	msgs := sess.Broker.GetRun("r1").MessagesSince(0)
	if len(msgs) != 2 {
		t.Fatalf("restored %d messages, want 2", len(msgs))
	}
	// The sequence counter has to resume past what was restored, or the
	// next message overwrites one that already exists.
	next := sess.Broker.GetRun("r1").PostMessage("worklog", "worker-a", nil, broker.PriorityNormal, "third")
	if next.Seq <= 2 {
		t.Errorf("the next message got seq %d, colliding with restored history", next.Seq)
	}
}

func TestTheDispatcherHonoursAdoptedWorkers(t *testing.T) {
	// AdoptRuns handing back the live workers is only half of it — the
	// dispatcher has to actually seed its running set from them, or the
	// first reap sees a dispatched task with nothing tracked, calls it a
	// failed dispatch, and launches a duplicate.
	t.Setenv("HOME", t.TempDir())
	orphanRun(t, "r1", broker.TaskSnapshot{ID: "a", State: broker.TaskDispatched})
	writePID(t, "r1", "a", os.Getpid())

	sess := NewSession()
	_, running := AdoptRuns(sess)

	d := NewDispatcher(sess)
	d.AdoptRunning(running)

	d.mu.Lock()
	tracked := d.running[runKey("r1", "a")]
	d.mu.Unlock()
	if tracked != "worker-a" {
		t.Fatalf("the dispatcher does not know about the adopted worker: %q", tracked)
	}
}
