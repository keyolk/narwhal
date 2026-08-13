// parity_test.go drives the same graph through both dispatchers and
// asserts they reach the same outcome.
//
// Five separate defects in this codebase were the same shape: a rule
// implemented on the batch path and silently absent from the interactive
// one — early synthesis dispatch, synthesis detection, radio-activity
// completion, run persistence, and the whole graph-mutation intake. Each
// was found by accident, in production, sometimes months apart. Each was
// fixed with a test that pinned that one rule.
//
// None of those tests would have caught the next one. What was missing is
// a test that fails when the two paths *disagree*, whatever they disagree
// about. That is this file.
//
// It lives in the daemon package because the daemon is the path that kept
// coming up short; the coordinator is imported as the reference.
package daemon

import (
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/keyolk/narwhal/internal/broker"
	"github.com/keyolk/narwhal/internal/coordinator"
	"github.com/keyolk/narwhal/internal/launcher"
)

// parityRunner is a fake worker for the coordinator side. The daemon side
// uses the stub `ccproxy` on PATH, so both end up launching nothing real.
type parityRunner struct {
	onLaunch func(cfg launcher.WorkerConfig)

	mu     sync.Mutex
	active map[string]bool
}

func (f *parityRunner) SetupAgent(a *broker.Agent, cfg launcher.WorkerConfig) (string, error) {
	return "/tmp/parity/" + cfg.AgentID, nil
}

func (f *parityRunner) Launch(agentDir string, cfg launcher.WorkerConfig) error {
	f.mu.Lock()
	if f.active == nil {
		f.active = map[string]bool{}
	}
	f.active[cfg.AgentID] = true
	f.mu.Unlock()

	go func() {
		if f.onLaunch != nil {
			f.onLaunch(cfg)
		}
		f.mu.Lock()
		delete(f.active, cfg.AgentID)
		f.mu.Unlock()
	}()
	return nil
}

func (f *parityRunner) ActiveWorkers() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.active))
	for id := range f.active {
		out = append(out, id)
	}
	return out
}

// graphOutcome is what both paths must agree on.
type graphOutcome struct {
	states map[string]broker.TaskState
	models map[string]string
	// claimed is the paths that were claimed at any point. Live ownership
	// is not compared: both paths release a worker's claims when it exits,
	// so the map is empty on both once a run finishes. What must match is
	// that the claim was seen and applied at all — on the interactive path
	// it used to be ignored entirely.
	claimed map[string]bool
}

func outcomeOf(run *broker.Run) graphOutcome {
	out := graphOutcome{
		states:  map[string]broker.TaskState{},
		models:  map[string]string{},
		claimed: map[string]bool{},
	}
	for _, t := range run.SnapshotTasks() {
		out.states[t.ID] = t.State
		out.models[t.ID] = t.Model
	}
	for _, m := range run.MessagesSince(0) {
		if _, _, paths, ok := broker.ParseFileClaimRequest(m.Content); ok {
			for _, p := range paths {
				out.claimed[p] = true
			}
		}
	}
	return out
}

func (g graphOutcome) diff(other graphOutcome) []string {
	var out []string
	keys := map[string]bool{}
	for k := range g.states {
		keys[k] = true
	}
	for k := range other.states {
		keys[k] = true
	}
	ids := make([]string, 0, len(keys))
	for k := range keys {
		ids = append(ids, k)
	}
	sort.Strings(ids)

	for _, id := range ids {
		if g.states[id] != other.states[id] {
			out = append(out, "task "+id+": batch="+string(g.states[id])+
				" daemon="+string(other.states[id]))
		}
		if g.models[id] != other.models[id] {
			out = append(out, "task "+id+" model: batch="+g.models[id]+
				" daemon="+other.models[id])
		}
	}
	for p := range g.claimed {
		if !other.claimed[p] {
			out = append(out, "file "+p+" claimed on batch but not on daemon")
		}
	}
	for p := range other.claimed {
		if !g.claimed[p] {
			out = append(out, "file "+p+" claimed on daemon but not on batch")
		}
	}
	return out
}

// parityScenario is a graph plus the radio traffic its workers produce.
type parityScenario struct {
	name string
	// build creates the run's initial tasks.
	build func(run *broker.Run)
	// work is what a worker does when launched: post messages, finish, or
	// exit silently. It runs on both paths.
	work func(run *broker.Run, taskID string)
}

func parityScenarios() []parityScenario {
	return []parityScenario{
		{
			name: "split request creates and dispatches a task",
			build: func(run *broker.Run) {
				run.AddTask("first", "first", "do first", nil)
			},
			work: func(run *broker.Run, taskID string) {
				if taskID == "first" {
					run.PostMessage(broker.PlanningThread, "worker-"+taskID, nil,
						broker.PriorityNormal,
						broker.FormatSplitRequest("found", "found", "investigate", nil))
				}
				run.GetTask(taskID).CompleteDispatch("done", run)
			},
		},
		{
			name: "dep edge added mid-run",
			build: func(run *broker.Run) {
				run.AddTask("a", "a", "do a", nil)
				run.AddTask("b", "b", "do b", nil)
			},
			work: func(run *broker.Run, taskID string) {
				if taskID == "a" {
					run.PostMessage(broker.WorklogThread, "worker-a", nil,
						broker.PriorityNormal,
						broker.FormatDepEdgeRequest(broker.DepAddPrefix, "b", []string{"a"}))
				}
				run.GetTask(taskID).CompleteDispatch("done", run)
			},
		},
		{
			// Both paths must record the claim. Whether a *conflicting*
			// claim is refused depends on two workers being alive at the
			// same moment, which the stub worker cannot arrange — that
			// case is covered directly in intake_test.go instead.
			name: "file claim is recorded",
			build: func(run *broker.Run) {
				run.AddTask("a", "a", "claims a file", nil)
			},
			work: func(run *broker.Run, taskID string) {
				run.PostMessage(broker.WorklogThread, "worker-"+taskID, nil,
					broker.PriorityNormal,
					broker.FormatFileClaimRequest(broker.FileClaimPrefix, taskID,
						[]string{"internal/api/router.go"}))
				run.GetTask(taskID).CompleteDispatch("done", run)
			},
		},
		{
			name: "model escalation moves the task up a tier",
			build: func(run *broker.Run) {
				t := run.AddTask("a", "a", "do a", nil)
				t.SetModel("haiku")
			},
			work: func(run *broker.Run, taskID string) {
				run.PostMessage(broker.WorklogThread, "worker-"+taskID, nil,
					broker.PriorityNormal,
					broker.FormatModelEscalateRequest(taskID, "", "harder than expected"))
				run.GetTask(taskID).CompleteDispatch("done", run)
			},
		},
		{
			name: "synthesis runs alongside its dependency",
			build: func(run *broker.Run) {
				run.AddTask("investigate", "investigate", "look", nil)
				run.AddTask("synthesis", "synthesis", "integrate", []string{"investigate"})
			},
			work: func(run *broker.Run, taskID string) {
				run.GetTask(taskID).CompleteDispatch("done", run)
			},
		},
		{
			name: "a worker that posts but forgets task-done still counts",
			build: func(run *broker.Run) {
				run.AddTask("a", "a", "do a", nil)
			},
			work: func(run *broker.Run, taskID string) {
				run.PostMessage(broker.WorklogThread, "worker-"+taskID, nil,
					broker.PriorityNormal, "here is what I found")
				// deliberately no task-done
			},
		},
	}
}

func TestBothDispatchersAgree(t *testing.T) {
	for _, sc := range parityScenarios() {
		t.Run(sc.name, func(t *testing.T) {
			batch := runOnCoordinator(t, sc)
			daemon := runOnDaemon(t, sc)

			if diffs := batch.diff(daemon); len(diffs) > 0 {
				for _, d := range diffs {
					t.Errorf("paths disagree: %s", d)
				}
				t.Fatal("the batch and interactive dispatchers reached different outcomes; " +
					"a rule was almost certainly added to one and not the other")
			}
		})
	}
}

func runOnCoordinator(t *testing.T, sc parityScenario) graphOutcome {
	t.Helper()
	b := broker.New()
	reg := broker.NewAgentRegistry()
	run := b.CreateRun("batch", "test", t.TempDir(), "main")
	run.CreateStandardThreads()
	sc.build(run)

	f := &parityRunner{}
	f.onLaunch = func(cfg launcher.WorkerConfig) {
		time.Sleep(20 * time.Millisecond)
		sc.work(run, cfg.TaskID)
	}

	cfg := coordinator.DefaultConfig()
	cfg.TickInterval = 20 * time.Millisecond
	cfg.Timeout = 5 * time.Second
	coordinator.New(run, reg, f, cfg).Run()

	return outcomeOf(run)
}

func runOnDaemon(t *testing.T, sc parityScenario) graphOutcome {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	stubWorker(t)

	sess := NewSession()
	sess.URL = "http://127.0.0.1:1"
	run := sess.Broker.CreateRun("daemon", "test", t.TempDir(), "main")
	run.CreateStandardThreads()
	sess.LauncherFor("daemon", run.CWD)
	sc.build(run)

	// The stub worker exits immediately, so the scenario's work is applied
	// here instead — the daemon launches a real process and cannot call
	// back into the test the way the fake runner does.
	d := NewDispatcher(sess)
	d.Start()
	defer d.Stop()

	// Apply each worker's behaviour as the daemon dispatches it. The stub
	// worker on PATH exits immediately and cannot call back into the test
	// the way the coordinator's fake runner does.
	//
	// Settling is not the stopping condition: a split request creates a
	// task *after* the graph first looks finished, and stopping there
	// would miss it — the run would look like it never grew. Keep going
	// until nothing new appears for a few rounds.
	deadline := time.Now().Add(5 * time.Second)
	applied := map[string]bool{}
	quiet := 0
	for time.Now().Before(deadline) {
		acted := false
		for _, snap := range run.SnapshotTasks() {
			if snap.State == broker.TaskDispatched && !applied[snap.ID] {
				applied[snap.ID] = true
				// Let the tick see the messages before the task finishes.
				// The coordinator's fake runner sleeps inside the worker
				// for the same reason: a worker that posts and completes
				// in the same instant gives the loop no window to read the
				// radio, which makes the harness look like a defect.
				sc.work(run, snap.ID)
				time.Sleep(2 * DispatchInterval)
				acted = true
			}
		}
		if acted || !allTerminal(run) {
			quiet = 0
		} else if quiet++; quiet >= 10 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	return outcomeOf(run)
}

func allTerminal(run *broker.Run) bool {
	tasks := run.SnapshotTasks()
	if len(tasks) == 0 {
		return false
	}
	for _, t := range tasks {
		switch t.State {
		case broker.TaskCompleted, broker.TaskFailed:
		default:
			return false
		}
	}
	return true
}
