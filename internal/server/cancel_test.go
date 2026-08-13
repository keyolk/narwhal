package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/keyolk/narwhal/internal/broker"
	"github.com/keyolk/narwhal/internal/launcher"
)

// cancelController is the minimum Controller a cancel needs: a launcher to
// kill and a run to retire.
type cancelController struct {
	l *launcher.Launcher
}

func (c *cancelController) NewRunID() string                                 { return "r-x" }
func (c *cancelController) NextID() int                                      { return 1 }
func (c *cancelController) LauncherFor(runID, cwd string) *launcher.Launcher { return c.l }
func (c *cancelController) Launcher(runID string) *launcher.Launcher         { return c.l }
func (c *cancelController) ActiveRuns() []string                             { return []string{"r-cancel"} }
func (c *cancelController) DropLauncher(runID string)                        {}

func TestCancelRetiresEveryUnfinishedTask(t *testing.T) {
	// Killing the workers stops the processes. A task left ready is one a
	// dispatcher picks up again; one left dispatched describes a worker
	// that no longer exists. Neither is a cancelled run.
	b := broker.New()
	reg := broker.NewAgentRegistry()
	run := b.CreateRun("r-cancel", "test", t.TempDir(), "main")
	run.CreateStandardThreads()

	ready := run.AddTask("ready", "ready", "x", nil)
	running := run.AddTask("running", "running", "x", nil)
	running.StartDispatch("d1", "worker-running")
	done := run.AddTask("done", "done", "x", nil)
	done.StartDispatch("d1", "worker-done")
	done.CompleteDispatch("finished", run)

	srv := New(b, reg)
	srv.SetController(&cancelController{l: launcher.New("http://127.0.0.1:1", "r-cancel", t.TempDir())})
	addr, err := srv.Start()
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer srv.Shutdown()

	body, _ := json.Marshal(map[string]any{"run_id": "r-cancel"})
	resp, err := http.Post(addr+"/api/v1/control/cancel", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("cancel returned %d", resp.StatusCode)
	}

	if got := run.CurrentState(); got != broker.RunCanceled {
		t.Errorf("run state = %s, want canceled", got)
	}
	if got := ready.CurrentState(); got != broker.TaskFailed {
		t.Errorf("ready task = %s, want failed", got)
	}
	if got := running.CurrentState(); got != broker.TaskFailed {
		t.Errorf("running task = %s, want failed", got)
	}
	if got := done.CurrentState(); got != broker.TaskCompleted {
		t.Errorf("completed task = %s; cancelling must not un-answer finished work", got)
	}
	if n := len(run.DispatchableTasks()); n != 0 {
		t.Errorf("%d task(s) still dispatchable after cancel", n)
	}
}
