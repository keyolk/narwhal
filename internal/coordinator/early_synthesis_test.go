package coordinator

import (
	"sync"
	"testing"
	"time"

	"github.com/keyolk/narwhal/internal/broker"
	"github.com/keyolk/narwhal/internal/launcher"
)

// The completion gate is only worth having if the synthesis worker still
// runs alongside its peers. If it queued behind them the deps would be an
// ordinary dispatch dependency and the expensive frontier worker would sit
// idle for the length of the investigation.

func TestSynthesisRunsAlongsideItsDependencies(t *testing.T) {
	b := broker.New()
	reg := broker.NewAgentRegistry()
	run := b.CreateRun("run-early", "test", "/tmp", "main")
	run.CreateStandardThreads()

	run.AddTask("task-1", "investigate", "a", nil)
	run.AddTask("task-2", "investigate", "b", nil)
	run.AddTask("synth", "synthesis", "integrate peer findings", []string{"task-1", "task-2"})

	var mu sync.Mutex
	// concurrent records whether synthesis was running at the same time as
	// an investigation task — the property the design turns on.
	concurrent := false
	live := map[string]bool{}

	f := newFakeRunner()
	cfg := testConfig()
	cfg.MaxConcurrency = 3
	c := New(run, reg, f, cfg)

	f.onLaunch = func(wc launcher.WorkerConfig) {
		mu.Lock()
		live[wc.TaskID] = true
		if live["synth"] && (live["task-1"] || live["task-2"]) {
			concurrent = true
		}
		mu.Unlock()

		if wc.TaskID == "synth" {
			// The synthesis worker drains while peers work, then finishes
			// last — which is what the gate enforces anyway.
			for i := 0; i < 100; i++ {
				if len(run.PendingDeps("synth")) == 0 {
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
		} else {
			time.Sleep(40 * time.Millisecond)
		}

		mu.Lock()
		delete(live, wc.TaskID)
		mu.Unlock()
		run.GetTask(wc.TaskID).CompleteDispatch("done", run)
		f.finish(wc.AgentID)
	}

	res := c.Run()

	if !concurrent {
		t.Error("synthesis never ran at the same time as an investigation task — " +
			"its deps are serializing it, which is what the completion gate exists to avoid")
	}
	if len(res.Completed) != 3 {
		t.Fatalf("completed = %v, want all three", res.Completed)
	}
}

func TestNonSynthesisTasksStillWaitForTheirDeps(t *testing.T) {
	// Early dispatch is a synthesis-only exception. An ordinary dependent
	// task must not start before what it depends on: unlike synthesis, it
	// has no protocol for working with partial input.
	b := broker.New()
	reg := broker.NewAgentRegistry()
	run := b.CreateRun("run-ordinary", "test", "/tmp", "main")
	run.CreateStandardThreads()

	run.AddTask("first", "first", "a", nil)
	run.AddTask("second", "second", "b", []string{"first"})

	var mu sync.Mutex
	var order []string

	f := newFakeRunner()
	c := New(run, reg, f, testConfig())
	f.onLaunch = func(wc launcher.WorkerConfig) {
		mu.Lock()
		order = append(order, wc.TaskID)
		mu.Unlock()
		time.Sleep(20 * time.Millisecond)
		run.GetTask(wc.TaskID).CompleteDispatch("done", run)
		f.finish(wc.AgentID)
	}

	c.Run()

	mu.Lock()
	defer mu.Unlock()
	if len(order) != 2 || order[0] != "first" {
		t.Fatalf("launch order = %v, want first before second", order)
	}
}

func TestSynthesisIsNotDispatchedTwice(t *testing.T) {
	// earlySynthesis runs on every tick. It must only offer tasks that are
	// still pending, or the coordinator would relaunch a synthesis worker
	// that is already up.
	b := broker.New()
	reg := broker.NewAgentRegistry()
	run := b.CreateRun("run-once", "test", "/tmp", "main")
	run.CreateStandardThreads()

	run.AddTask("task-1", "investigate", "a", nil)
	run.AddTask("synth", "synthesis", "integrate", []string{"task-1"})

	f := newFakeRunner()
	c := New(run, reg, f, testConfig())
	f.onLaunch = func(wc launcher.WorkerConfig) {
		if wc.TaskID == "synth" {
			for i := 0; i < 100; i++ {
				if len(run.PendingDeps("synth")) == 0 {
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
		} else {
			time.Sleep(60 * time.Millisecond)
		}
		run.GetTask(wc.TaskID).CompleteDispatch("done", run)
		f.finish(wc.AgentID)
	}

	c.Run()

	if got := run.GetTask("synth").DispatchCount(); got != 1 {
		t.Fatalf("synthesis dispatched %d times, want exactly 1", got)
	}
}
