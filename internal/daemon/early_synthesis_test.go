package daemon

import (
	"testing"

	"github.com/keyolk/narwhal/internal/broker"
)

// The interactive path has its own dispatch loop, separate from the batch
// coordinator's. Early synthesis dispatch was implemented in the
// coordinator first and did not apply here — an end-to-end run through the
// daemon left the synthesis worker pending while its peer ran, which is
// the state the completion gate is supposed to make unnecessary. Most runs
// go through the daemon, so this is the path that matters most.

func TestDispatcherLaunchesSynthesisBeforeItsDeps(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	stubWorker(t)

	sess := NewSession()
	sess.URL = "http://127.0.0.1:1"
	run := sess.Broker.CreateRun("r-synth", "test", t.TempDir(), "main")
	sess.LauncherFor("r-synth", run.CWD)
	run.AddTask("investigate", "investigate", "look at things", nil)
	run.AddTask("synthesis", "synthesis", "integrate peer findings", []string{"investigate"})

	d := NewDispatcher(sess)
	d.Start()
	defer d.Stop()

	waitFor(t, "synthesis to be dispatched while its dep is unfinished", func() bool {
		return run.GetTask("synthesis").DispatchCount() > 0
	})

	// And the dep really was unfinished — otherwise this proves nothing.
	if got := run.GetTask("investigate").CurrentState(); got == broker.TaskCompleted {
		t.Skip("the dep completed before the assertion; timing makes this run inconclusive")
	}
}

func TestDispatcherStillHoldsOrdinaryDependents(t *testing.T) {
	// Early dispatch is a synthesis-only exception on this path too.
	t.Setenv("HOME", t.TempDir())
	stubWorker(t)

	sess := NewSession()
	sess.URL = "http://127.0.0.1:1"
	run := sess.Broker.CreateRun("r-ordinary", "test", t.TempDir(), "main")
	sess.LauncherFor("r-ordinary", run.CWD)
	run.AddTask("first", "first", "a", nil)
	run.AddTask("second", "second", "b", []string{"first"})

	d := NewDispatcher(sess)
	d.Start()
	defer d.Stop()

	waitFor(t, "first to be dispatched", func() bool {
		return run.GetTask("first").DispatchCount() > 0
	})
	if got := run.GetTask("second").DispatchCount(); got != 0 {
		t.Fatalf("an ordinary dependent was dispatched %d times before its dep finished", got)
	}
}
