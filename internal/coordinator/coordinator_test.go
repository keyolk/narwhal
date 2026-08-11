package coordinator

import (
	"sync"
	"testing"
	"time"

	"github.com/keyolk/narwhal/internal/broker"
	"github.com/keyolk/narwhal/internal/launcher"
)

// fakeRunner stands in for the real launcher. Each launched worker is
// "completed" by the test's completion policy rather than by a real
// Claude Code process.
type fakeRunner struct {
	mu       sync.Mutex
	active   map[string]bool
	launched []string
	// onLaunch is called after a worker is registered as active, so a test
	// can decide when and how that worker finishes.
	onLaunch func(cfg launcher.WorkerConfig)
}

func newFakeRunner() *fakeRunner {
	return &fakeRunner{active: make(map[string]bool)}
}

func (f *fakeRunner) SetupAgent(a *broker.Agent, cfg launcher.WorkerConfig) (string, error) {
	return "/tmp/fake/" + cfg.AgentID, nil
}

func (f *fakeRunner) Launch(agentDir string, cfg launcher.WorkerConfig) error {
	f.mu.Lock()
	f.active[cfg.AgentID] = true
	f.launched = append(f.launched, cfg.TaskID)
	f.mu.Unlock()
	if f.onLaunch != nil {
		go f.onLaunch(cfg)
	}
	return nil
}

func (f *fakeRunner) ActiveWorkers() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.active))
	for id := range f.active {
		out = append(out, id)
	}
	return out
}

func (f *fakeRunner) finish(agentID string) {
	f.mu.Lock()
	delete(f.active, agentID)
	f.mu.Unlock()
}

func (f *fakeRunner) launchOrder() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.launched...)
}

func testConfig() Config {
	return Config{
		MaxConcurrency: 3,
		TickInterval:   20 * time.Millisecond,
		Timeout:        10 * time.Second,
	}
}

func TestDispatchesIndependentTasksInParallel(t *testing.T) {
	b := broker.New()
	reg := broker.NewAgentRegistry()
	run := b.CreateRun("run-par", "test", "/tmp", "main")

	run.AddTask("a", "a", "do a", nil)
	run.AddTask("b", "b", "do b", nil)
	run.AddTask("c", "c", "do c", nil)

	f := newFakeRunner()
	c := New(run, reg, f, testConfig())

	// Each worker completes its task shortly after launch.
	f.onLaunch = func(cfg launcher.WorkerConfig) {
		time.Sleep(50 * time.Millisecond)
		if task := run.GetTask(cfg.TaskID); task != nil {
			task.CompleteDispatch("done", run)
		}
		f.finish(cfg.AgentID)
	}

	res := c.Run()

	if len(res.Completed) != 3 {
		t.Fatalf("completed = %v, want 3 tasks", res.Completed)
	}
	if len(res.Failed) != 0 {
		t.Fatalf("unexpected failures: %v", res.Failed)
	}
	if res.TimedOut {
		t.Fatal("run timed out")
	}
}

func TestRespectsDependencyOrder(t *testing.T) {
	b := broker.New()
	reg := broker.NewAgentRegistry()
	run := b.CreateRun("run-deps", "test", "/tmp", "main")

	run.AddTask("first", "first", "do first", nil)
	run.AddTask("second", "second", "do second", []string{"first"})

	f := newFakeRunner()
	c := New(run, reg, f, testConfig())

	f.onLaunch = func(cfg launcher.WorkerConfig) {
		time.Sleep(30 * time.Millisecond)
		if task := run.GetTask(cfg.TaskID); task != nil {
			task.CompleteDispatch("done", run)
		}
		f.finish(cfg.AgentID)
	}

	res := c.Run()

	if len(res.Completed) != 2 {
		t.Fatalf("completed = %v, want both tasks", res.Completed)
	}
	order := f.launchOrder()
	if len(order) != 2 || order[0] != "first" || order[1] != "second" {
		t.Fatalf("launch order = %v, want [first second]", order)
	}
}

func TestConcurrencyCap(t *testing.T) {
	b := broker.New()
	reg := broker.NewAgentRegistry()
	run := b.CreateRun("run-cap", "test", "/tmp", "main")

	for _, id := range []string{"t1", "t2", "t3", "t4", "t5"} {
		run.AddTask(id, id, "work", nil)
	}

	f := newFakeRunner()
	cfg := testConfig()
	cfg.MaxConcurrency = 2
	c := New(run, reg, f, cfg)

	// Track concurrency with an explicit counter incremented at launch and
	// decremented at finish, so the peak reflects genuine overlap rather
	// than a sampled snapshot.
	var concMu sync.Mutex
	inFlight := 0
	peak := 0

	f.onLaunch = func(wc launcher.WorkerConfig) {
		concMu.Lock()
		inFlight++
		if inFlight > peak {
			peak = inFlight
		}
		concMu.Unlock()

		time.Sleep(40 * time.Millisecond)
		if task := run.GetTask(wc.TaskID); task != nil {
			task.CompleteDispatch("done", run)
		}

		concMu.Lock()
		inFlight--
		concMu.Unlock()
		f.finish(wc.AgentID)
	}

	res := c.Run()

	if len(res.Completed) != 5 {
		t.Fatalf("completed = %d, want 5", len(res.Completed))
	}
	concMu.Lock()
	observed := peak
	concMu.Unlock()
	if observed > cfg.MaxConcurrency {
		t.Fatalf("peak concurrency = %d, exceeds cap %d", observed, cfg.MaxConcurrency)
	}
	if observed < 2 {
		t.Fatalf("peak concurrency = %d; expected genuine parallelism", observed)
	}
}

func TestWorkerExitWithoutDoneIsRetriedThenFails(t *testing.T) {
	b := broker.New()
	reg := broker.NewAgentRegistry()
	run := b.CreateRun("run-fail", "test", "/tmp", "main")
	run.AddTask("flaky", "flaky", "work", nil)

	f := newFakeRunner()
	c := New(run, reg, f, testConfig())

	// Worker always exits without declaring completion.
	f.onLaunch = func(cfg launcher.WorkerConfig) {
		time.Sleep(20 * time.Millisecond)
		f.finish(cfg.AgentID)
	}

	res := c.Run()

	if len(res.Failed) != 1 || res.Failed[0] != "flaky" {
		t.Fatalf("failed = %v, want [flaky]", res.Failed)
	}
	// Circuit breaker: exactly MaxDispatchFailures attempts.
	task := run.GetTask("flaky")
	if got := task.DispatchCount(); got != broker.MaxDispatchFailures {
		t.Fatalf("dispatch count = %d, want %d", got, broker.MaxDispatchFailures)
	}
}

func TestBlockedTaskIsReportedUnreached(t *testing.T) {
	b := broker.New()
	reg := broker.NewAgentRegistry()
	run := b.CreateRun("run-blocked", "test", "/tmp", "main")

	// "orphan" depends on a task that never exists, so it can never run.
	run.AddTask("orphan", "orphan", "work", []string{"missing"})

	f := newFakeRunner()
	c := New(run, reg, f, testConfig())

	res := c.Run()

	if len(res.Unreached) != 1 || res.Unreached[0] != "orphan" {
		t.Fatalf("unreached = %v, want [orphan]", res.Unreached)
	}
	if len(res.Completed) != 0 {
		t.Fatalf("nothing should complete, got %v", res.Completed)
	}
}

func TestDiamondDependency(t *testing.T) {
	// A → B, A → C, B+C → D. D must run only after both B and C finish.
	b := broker.New()
	reg := broker.NewAgentRegistry()
	run := b.CreateRun("run-diamond", "test", "/tmp", "main")

	run.AddTask("A", "A", "do A", nil)
	run.AddTask("B", "B", "do B", []string{"A"})
	run.AddTask("C", "C", "do C", []string{"A"})
	run.AddTask("D", "D", "do D", []string{"B", "C"})

	f := newFakeRunner()
	c := New(run, reg, f, testConfig())

	f.onLaunch = func(cfg launcher.WorkerConfig) {
		time.Sleep(20 * time.Millisecond)
		if task := run.GetTask(cfg.TaskID); task != nil {
			task.CompleteDispatch("done", run)
		}
		f.finish(cfg.AgentID)
	}

	res := c.Run()

	if len(res.Completed) != 4 {
		t.Fatalf("completed = %d, want 4", len(res.Completed))
	}
	if len(res.Failed) != 0 {
		t.Fatalf("unexpected failures: %v", res.Failed)
	}

	// Verify D ran last (its deps B and C both completed first).
	order := f.launchOrder()
	if len(order) != 4 {
		t.Fatalf("launch order length = %d, want 4", len(order))
	}
	if order[0] != "A" {
		t.Fatalf("first launched = %s, want A", order[0])
	}
	if order[3] != "D" {
		t.Fatalf("last launched = %s, want D", order[3])
	}
	// B and C must come before D but after A.
	bIdx, cIdx, dIdx := -1, -1, -1
	for i, id := range order {
		switch id {
		case "B":
			bIdx = i
		case "C":
			cIdx = i
		case "D":
			dIdx = i
		}
	}
	if bIdx > dIdx || cIdx > dIdx {
		t.Fatalf("B(%d) or C(%d) ran after D(%d)", bIdx, cIdx, dIdx)
	}
}

func TestPartialFailureBlocksDependents(t *testing.T) {
	// A always fails (circuit-breaks). B depends on A. B should be
	// unreached because A never completes.
	b := broker.New()
	reg := broker.NewAgentRegistry()
	run := b.CreateRun("run-partial-fail", "test", "/tmp", "main")

	run.AddTask("A", "A", "always fail", nil)
	run.AddTask("B", "B", "depends on A", []string{"A"})

	f := newFakeRunner()
	c := New(run, reg, f, testConfig())

	// Worker for A always exits without task-done, causing circuit breaker.
	f.onLaunch = func(cfg launcher.WorkerConfig) {
		time.Sleep(10 * time.Millisecond)
		f.finish(cfg.AgentID)
	}

	res := c.Run()

	if len(res.Failed) != 1 || res.Failed[0] != "A" {
		t.Fatalf("failed = %v, want [A]", res.Failed)
	}
	if len(res.Unreached) != 1 || res.Unreached[0] != "B" {
		t.Fatalf("unreached = %v, want [B]", res.Unreached)
	}
}

func TestSplitRequestAddsTaskMidRun(t *testing.T) {
	// Worker on the initial task posts a split-request to the planning
	// thread. The coordinator should create the new task and dispatch it.
	b := broker.New()
	reg := broker.NewAgentRegistry()
	run := b.CreateRun("run-split", "test", "/tmp", "main")
	run.CreateThread("planning", "planning", []string{"main"})

	run.AddTask("initial", "initial", "do initial", nil)

	f := newFakeRunner()
	c := New(run, reg, f, testConfig())

	f.onLaunch = func(cfg launcher.WorkerConfig) {
		if cfg.TaskID == "initial" {
			// Worker discovers independent work and requests a split.
			body := broker.FormatSplitRequest("discovered", "discovered", "investigate new area", nil)
			run.PostMessage("planning", cfg.AgentID, nil, broker.PriorityNormal, body)
			time.Sleep(10 * time.Millisecond)
			run.GetTask("initial").CompleteDispatch("done + found new area", run)
			f.finish(cfg.AgentID)
		} else {
			time.Sleep(10 * time.Millisecond)
			run.GetTask(cfg.TaskID).CompleteDispatch("done", run)
			f.finish(cfg.AgentID)
		}
	}

	res := c.Run()

	if len(res.Completed) != 2 {
		t.Fatalf("completed = %d, want both initial and discovered, got %v", len(res.Completed), res.Completed)
	}
	if run.GetTask("discovered") == nil {
		t.Fatal("split-requested task 'discovered' was never created")
	}
	// The discovered task should have been dispatched (at least 1 dispatch).
	if got := run.GetTask("discovered").DispatchCount(); got < 1 {
		t.Fatalf("discovered task dispatch count = %d, want >= 1", got)
	}
}

func TestSplitRequestWithDeps(t *testing.T) {
	// Worker on task-1 requests a split for task-2 that depends on task-1.
	// The coordinator should create task-2 with the dep, and it should
	// become ready only after task-1 completes.
	b := broker.New()
	reg := broker.NewAgentRegistry()
	run := b.CreateRun("run-split-deps", "test", "/tmp", "main")
	run.CreateThread("planning", "planning", []string{"main"})

	run.AddTask("task-1", "task-1", "do task-1", nil)

	f := newFakeRunner()
	c := New(run, reg, f, testConfig())

	f.onLaunch = func(cfg launcher.WorkerConfig) {
		if cfg.TaskID == "task-1" {
			body := broker.FormatSplitRequest("task-2", "task-2", "follow-up", []string{"task-1"})
			run.PostMessage("planning", cfg.AgentID, nil, broker.PriorityNormal, body)
			time.Sleep(10 * time.Millisecond)
			run.GetTask("task-1").CompleteDispatch("done", run)
			f.finish(cfg.AgentID)
		} else {
			time.Sleep(10 * time.Millisecond)
			run.GetTask(cfg.TaskID).CompleteDispatch("done", run)
			f.finish(cfg.AgentID)
		}
	}

	res := c.Run()

	if len(res.Completed) != 2 {
		t.Fatalf("completed = %d, want 2", len(res.Completed))
	}
	// Verify task-2 has the dep.
	t2 := run.GetTask("task-2")
	if t2 == nil {
		t.Fatal("task-2 was not created")
	}
	snap := run.SnapshotTasks()
	for _, ts := range snap {
		if ts.ID == "task-2" {
			if len(ts.Deps) != 1 || ts.Deps[0] != "task-1" {
				t.Fatalf("task-2 deps = %v, want [task-1]", ts.Deps)
			}
		}
	}
	// Verify task-2 launched after task-1.
	order := f.launchOrder()
	if len(order) != 2 || order[0] != "task-1" || order[1] != "task-2" {
		t.Fatalf("launch order = %v, want [task-1 task-2]", order)
	}
}
