package daemon

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/keyolk/narwhal/internal/broker"
)

// waitFor polls cond until it holds or the deadline passes.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// stubWorker makes the launcher run a trivial command instead of a real
// Claude Code process, so the dispatch loop can be tested without spending
// tokens or waiting on a model.
func stubWorker(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	// A fake `ccproxy` that exits immediately.
	script := filepath.Join(dir, "ccproxy")
	body := "#!/bin/sh\nexit 0\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
}

func TestDispatcherLaunchesReadyTask(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	stubWorker(t)

	sess := NewSession()
	sess.URL = "http://127.0.0.1:1"
	run := sess.Broker.CreateRun("r1", "test", t.TempDir(), "main")
	sess.LauncherFor("r1", run.CWD)
	run.AddTask("a", "a", "do a", nil)

	d := NewDispatcher(sess)
	d.Start()
	defer d.Stop()

	waitFor(t, "task a to be dispatched", func() bool {
		return run.GetTask("a").DispatchCount() > 0
	})
}

func TestDispatcherLaunchesDependentAfterDepCompletes(t *testing.T) {
	// The bug this loop exists for: a task created with unmet deps became
	// ready when its dep completed and then never launched, because nothing
	// on the daemon path was watching.
	t.Setenv("HOME", t.TempDir())
	stubWorker(t)

	sess := NewSession()
	sess.URL = "http://127.0.0.1:1"
	run := sess.Broker.CreateRun("r2", "test", t.TempDir(), "main")
	sess.LauncherFor("r2", run.CWD)

	run.AddTask("first", "first", "do first", nil)
	run.AddTask("second", "second", "do second", []string{"first"})

	if run.GetTask("second").CurrentState() != broker.TaskPending {
		t.Fatal("second should start pending")
	}

	d := NewDispatcher(sess)
	d.Start()
	defer d.Stop()

	waitFor(t, "first to dispatch", func() bool {
		return run.GetTask("first").DispatchCount() > 0
	})

	// Simulate the worker declaring completion.
	run.GetTask("first").CompleteDispatch("done", run)

	waitFor(t, "second to dispatch after its dep completed", func() bool {
		return run.GetTask("second").DispatchCount() > 0
	})
}

func TestDispatcherDoesNotDoubleDispatch(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	stubWorker(t)

	sess := NewSession()
	sess.URL = "http://127.0.0.1:1"
	run := sess.Broker.CreateRun("r3", "test", t.TempDir(), "main")
	sess.LauncherFor("r3", run.CWD)
	run.AddTask("solo", "solo", "work", nil)

	d := NewDispatcher(sess)
	d.Start()
	defer d.Stop()

	waitFor(t, "solo to dispatch", func() bool {
		return run.GetTask("solo").DispatchCount() > 0
	})

	// The stub exits immediately, so the reaper will see it gone and mark a
	// failed dispatch — but a single tick must never launch it twice.
	time.Sleep(300 * time.Millisecond)
	if n := run.GetTask("solo").DispatchCount(); n > broker.MaxDispatchFailures {
		t.Fatalf("dispatch count = %d, exceeds the circuit breaker", n)
	}
}

func TestDispatcherRespectsPerRunConcurrencyCap(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// A worker that lingers, so slots stay occupied.
	dir := t.TempDir()
	script := filepath.Join(dir, "ccproxy")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nsleep 5\n"), 0o700); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))

	sess := NewSession()
	sess.URL = "http://127.0.0.1:1"
	run := sess.Broker.CreateRun("r4", "test", t.TempDir(), "main")
	l := sess.LauncherFor("r4", run.CWD)

	for _, id := range []string{"t1", "t2", "t3", "t4", "t5", "t6"} {
		run.AddTask(id, id, "work", nil)
	}

	d := NewDispatcher(sess)
	d.Start()
	defer d.Stop()

	// Give the loop a few ticks to saturate.
	time.Sleep(1500 * time.Millisecond)

	if got := len(l.ActiveWorkers()); got > MaxConcurrentPerRun {
		t.Fatalf("active workers = %d, exceeds cap %d", got, MaxConcurrentPerRun)
	}
}

func TestDispatcherRecordsFailureWhenWorkerExitsSilently(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	stubWorker(t) // exits immediately without calling task-done

	sess := NewSession()
	sess.URL = "http://127.0.0.1:1"
	run := sess.Broker.CreateRun("r5", "test", t.TempDir(), "main")
	sess.LauncherFor("r5", run.CWD)
	run.AddTask("ghost", "ghost", "work", nil)

	d := NewDispatcher(sess)
	d.Start()
	defer d.Stop()

	// The circuit breaker should eventually mark it failed rather than
	// retrying forever.
	waitFor(t, "ghost to fail after repeated silent exits", func() bool {
		return run.GetTask("ghost").CurrentState() == broker.TaskFailed
	})
}

func TestRunKeyHelpers(t *testing.T) {
	key := "run-1\x00task-a"
	if got := runOf(key); got != "run-1" {
		t.Fatalf("runOf = %q, want run-1", got)
	}
	if got := taskOf(key); got != "task-a" {
		t.Fatalf("taskOf = %q, want task-a", got)
	}
	// Two runs may legitimately hold tasks with the same id.
	if runOf("a\x00x") == runOf("b\x00x") {
		t.Fatal("keys from different runs must not collide")
	}
}
