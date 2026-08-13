package daemon

import (
	"testing"
	"time"

	"github.com/keyolk/narwhal/internal/broker"
)

// Session.DropLauncher was defined and never called, so a launcher lived
// for the life of the daemon. ActiveRuns drives the monitor's run picker,
// the dispatch tick, and the guard that refuses to stop while workers run
// — so finished runs piled up in all three. The picker filled with runs
// that ended hours ago, and the stop guard could be tripped by one of them.

// settledRun builds a run whose single task has already finished.
func settledRun(t *testing.T, sess *Session, id string) *broker.Run {
	t.Helper()
	run := sess.Broker.CreateRun(id, "test", t.TempDir(), "main")
	run.CreateStandardThreads()
	sess.LauncherFor(id, run.CWD)
	task := run.AddTask("t", "t", "x", nil)
	task.StartDispatch("d1", "worker-t")
	task.CompleteDispatch("done", run)
	return run
}

func TestSettledRunsAreRetired(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	stubWorker(t)

	sess := NewSession()
	sess.URL = "http://127.0.0.1:1"
	for _, id := range []string{"a", "b", "c"} {
		settledRun(t, sess, id)
	}

	d := NewDispatcher(sess)
	d.Start()
	defer d.Stop()

	waitFor(t, "finished runs to drop out of ActiveRuns", func() bool {
		return len(sess.ActiveRuns()) == 0
	})
}

func TestASettledRunIsMarkedDone(t *testing.T) {
	// Nothing on the interactive path set this, so every interactive run
	// stayed "active" forever — in the monitor header and in every
	// persisted snapshot.
	t.Setenv("HOME", t.TempDir())
	stubWorker(t)

	sess := NewSession()
	sess.URL = "http://127.0.0.1:1"
	run := settledRun(t, sess, "r-done")

	d := NewDispatcher(sess)
	d.tick()

	if got := run.CurrentState(); got != broker.RunDone {
		t.Fatalf("run state = %s, want done", got)
	}
}

func TestARunWithWorkLeftIsNotRetired(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	stubWorker(t)

	sess := NewSession()
	sess.URL = "http://127.0.0.1:1"
	run := sess.Broker.CreateRun("r-busy", "test", t.TempDir(), "main")
	run.CreateStandardThreads()
	sess.LauncherFor("r-busy", run.CWD)
	done := run.AddTask("done", "done", "x", nil)
	done.StartDispatch("d1", "worker-done")
	done.CompleteDispatch("finished", run)
	run.AddTask("pending", "pending", "x", nil) // still to do

	d := NewDispatcher(sess)
	d.retireIfSettled("r-busy", run, nil)

	if len(sess.ActiveRuns()) == 0 {
		t.Fatal("a run with unfinished work was retired")
	}
	if got := run.CurrentState(); got == broker.RunDone {
		t.Error("a run with unfinished work was marked done")
	}
}

func TestARunWithNoTasksYetIsNotRetired(t *testing.T) {
	// A run whose first spawn is still in flight has no tasks. Treating
	// that as finished would retire it the instant it was created.
	t.Setenv("HOME", t.TempDir())

	sess := NewSession()
	sess.URL = "http://127.0.0.1:1"
	run := sess.Broker.CreateRun("r-empty", "test", t.TempDir(), "main")
	sess.LauncherFor("r-empty", run.CWD)

	d := NewDispatcher(sess)
	d.retireIfSettled("r-empty", run, nil)

	if len(sess.ActiveRuns()) == 0 {
		t.Fatal("a run whose first task has not been created was retired")
	}
}

func TestARunWithALiveWorkerIsNotRetired(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	sess := NewSession()
	sess.URL = "http://127.0.0.1:1"
	run := settledRun(t, sess, "r-live")

	d := NewDispatcher(sess)
	d.retireIfSettled("r-live", run, []string{"worker-t"})

	if len(sess.ActiveRuns()) == 0 {
		t.Fatal("a run with a live worker was retired")
	}
}

func TestRetiringDoesNotOverwriteACancelledState(t *testing.T) {
	// Cancelled and done are different outcomes and the record should say
	// which one happened.
	t.Setenv("HOME", t.TempDir())
	stubWorker(t)

	sess := NewSession()
	sess.URL = "http://127.0.0.1:1"
	run := settledRun(t, sess, "r-cancelled-settled")
	run.SetState(broker.RunCanceled)

	d := NewDispatcher(sess)
	d.tick()

	if got := run.CurrentState(); got != broker.RunCanceled {
		t.Fatalf("run state = %s, want canceled to survive retirement", got)
	}
	waitFor(t, "the cancelled run to be retired", func() bool {
		return len(sess.ActiveRuns()) == 0
	})
}

func TestRetiredRunKeepsItsPersistedRecord(t *testing.T) {
	// Retiring releases the launcher, not the history: the run is exactly
	// what `narwhal show` is for afterwards.
	t.Setenv("HOME", t.TempDir())
	stubWorker(t)

	sess := NewSession()
	sess.URL = "http://127.0.0.1:1"
	settledRun(t, sess, "r-record")

	d := NewDispatcher(sess)
	d.Start()
	defer d.Stop()

	waitFor(t, "the run to be retired", func() bool {
		return len(sess.ActiveRuns()) == 0
	})

	// Give the final write a moment, then confirm it is readable.
	time.Sleep(100 * time.Millisecond)
	if !d.persistedAtLeastOnce("r-record") {
		t.Error("a retired run left no record")
	}
}

func TestRetiredRunStillAppearsInStatus(t *testing.T) {
	// Retiring releases the launcher, and ActiveRuns drops the run — but a
	// status listing built on ActiveRuns would make the run vanish at the
	// exact moment the user goes looking for its result. KnownRuns keeps
	// it.
	t.Setenv("HOME", t.TempDir())
	stubWorker(t)

	sess := NewSession()
	sess.URL = "http://127.0.0.1:1"
	settledRun(t, sess, "r-visible")

	d := NewDispatcher(sess)
	d.tick()

	if len(sess.ActiveRuns()) != 0 {
		t.Fatal("the settled run was not retired")
	}
	known := sess.KnownRuns()
	if len(known) != 1 || known[0] != "r-visible" {
		t.Fatalf("KnownRuns = %v, want the retired run to still be listed", known)
	}
}

func TestKnownRunsListsLiveAndRetiredTogether(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	stubWorker(t)

	sess := NewSession()
	sess.URL = "http://127.0.0.1:1"
	settledRun(t, sess, "old")
	live := sess.Broker.CreateRun("new", "test", t.TempDir(), "main")
	sess.LauncherFor("new", live.CWD)
	live.AddTask("t", "t", "x", nil)

	d := NewDispatcher(sess)
	d.retireIfSettled("old", sess.Broker.GetRun("old"), nil)

	known := sess.KnownRuns()
	if len(known) != 2 {
		t.Fatalf("KnownRuns = %v, want both the retired and the live run", known)
	}
	if known[0] != "old" || known[1] != "new" {
		t.Errorf("KnownRuns = %v, want oldest first", known)
	}
}
