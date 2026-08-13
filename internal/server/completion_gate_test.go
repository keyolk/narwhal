package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/keyolk/narwhal/internal/broker"
)

// gateFixture builds a run whose synthesis task depends on two peers, and
// returns the server address plus the synthesis worker's token.
func gateFixture(t *testing.T) (addr string, token string, run *broker.Run, shutdown func()) {
	t.Helper()
	b := broker.New()
	reg := broker.NewAgentRegistry()

	runID := "run-gate"
	run = b.CreateRun(runID, "test", "/tmp", "main")
	run.CreateStandardThreads()
	run.AddTask("task-1", "investigate", "a", nil)
	run.AddTask("task-2", "investigate", "b", nil)
	run.AddTask("synth", "synthesis", "integrate peer findings", []string{"task-1", "task-2"})

	agent := reg.Register("worker-synth", runID, false)
	srv := New(b, reg)
	addr, err := srv.Start()
	if err != nil {
		t.Fatalf("start server: %v", err)
	}
	return addr, agent.Token, run, srv.Shutdown
}

// callTaskDone posts task-done and returns the status and decoded body.
func callTaskDone(t *testing.T, addr, token, taskID string) (int, map[string]any) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"outcome": "here is the answer"})
	resp, err := http.Post(addr+"/api/v1/agents/"+token+"/task/"+taskID+"/done",
		"application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post task-done: %v", err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestTaskDoneRefusedWhilePeersAreRunning(t *testing.T) {
	// The failure this exists to stop: a synthesis worker declaring itself
	// done while a peer is still posting, so the final answer is written
	// from a partial picture. Observed on real runs before the gate.
	addr, token, run, shutdown := gateFixture(t)
	defer shutdown()

	code, body := callTaskDone(t, addr, token, "synth")
	if code != http.StatusConflict {
		t.Fatalf("task-done returned %d, want 409 while peers run", code)
	}
	pending, _ := body["pending_deps"].([]any)
	if len(pending) != 2 {
		t.Fatalf("pending_deps = %v, want both peers", body["pending_deps"])
	}
	if got := run.GetTask("synth").CurrentState(); got == broker.TaskCompleted {
		t.Fatal("a refused task-done still completed the task")
	}
}

func TestTaskDoneRefusalNamesWhoIsOutstandingOnTheRadio(t *testing.T) {
	// A 409 the worker cannot interpret is a dead end. The refusal is also
	// posted urgently so a worker sitting on its watcher learns why.
	addr, token, run, shutdown := gateFixture(t)
	defer shutdown()

	callTaskDone(t, addr, token, "synth")

	msgs := run.MessagesSince(0)
	var found *broker.Message
	for _, m := range msgs {
		if strings.HasPrefix(m.Content, "NOT_DONE|") {
			found = m
		}
	}
	if found == nil {
		t.Fatal("refusal was not posted to the radio")
	}
	if found.Priority != broker.PriorityUrgent {
		t.Errorf("refusal priority = %s, want urgent", found.Priority)
	}
	for _, peer := range []string{"task-1", "task-2"} {
		if !strings.Contains(found.Content, peer) {
			t.Errorf("refusal does not name %s: %q", peer, found.Content)
		}
	}
}

func TestTaskDoneAcceptedOncePeersFinish(t *testing.T) {
	addr, token, run, shutdown := gateFixture(t)
	defer shutdown()

	for _, id := range []string{"task-1", "task-2"} {
		task := run.GetTask(id)
		task.StartDispatch("d-"+id, "worker-"+id)
		task.CompleteDispatch("done", run)
	}

	code, _ := callTaskDone(t, addr, token, "synth")
	if code != http.StatusOK {
		t.Fatalf("task-done returned %d after every peer finished, want 200", code)
	}
	if got := run.GetTask("synth").CurrentState(); got != broker.TaskCompleted {
		t.Fatalf("synth state = %s, want completed", got)
	}
}

func TestTaskDoneUngatedForIndependentTasks(t *testing.T) {
	// The gate must not touch the ordinary case: a task with no deps
	// completes the moment it says so.
	addr, token, run, shutdown := gateFixture(t)
	defer shutdown()

	code, _ := callTaskDone(t, addr, token, "task-1")
	if code != http.StatusOK {
		t.Fatalf("independent task-done returned %d, want 200", code)
	}
	if got := run.GetTask("task-1").CurrentState(); got != broker.TaskCompleted {
		t.Fatalf("task-1 state = %s, want completed", got)
	}
}
