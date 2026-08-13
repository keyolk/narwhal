package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

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
// timeoutMs bounds how long the server holds the call waiting on deps.
func callTaskDone(t *testing.T, addr, token, taskID string, timeoutMs int) (int, map[string]any) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"outcome":    "here is the answer",
		"timeout_ms": timeoutMs,
	})
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

func TestTaskDoneBlocksWhilePeersAreRunning(t *testing.T) {
	// The failure this exists to stop: a synthesis worker declaring itself
	// done while a peer is still posting, so the final answer is written
	// from a partial picture. Observed on real runs before the gate.
	addr, token, run, shutdown := gateFixture(t)
	defer shutdown()

	start := time.Now()
	code, body := callTaskDone(t, addr, token, "synth", 300)
	elapsed := time.Since(start)

	if code != http.StatusConflict {
		t.Fatalf("task-done returned %d, want 409 after waiting out the timeout", code)
	}
	if elapsed < 250*time.Millisecond {
		t.Errorf("task-done returned after %v — it did not hold the request open", elapsed)
	}
	pending, _ := body["pending_deps"].([]any)
	if len(pending) != 2 {
		t.Fatalf("pending_deps = %v, want both peers", body["pending_deps"])
	}
	if got := run.GetTask("synth").CurrentState(); got == broker.TaskCompleted {
		t.Fatal("a gated task-done still completed the task")
	}
}

func TestTaskDoneReturnsWhenTheLastPeerFinishes(t *testing.T) {
	// The point of blocking: the worker's turn stays alive across the
	// wait, so it completes on its first call rather than being told to
	// try again — advice a --print worker cannot act on, because its
	// process ends when its turn does.
	addr, token, run, shutdown := gateFixture(t)
	defer shutdown()

	done := make(chan int, 1)
	go func() {
		code, _ := callTaskDone(t, addr, token, "synth", 10000)
		done <- code
	}()

	// Let the call block, then finish the peers.
	time.Sleep(200 * time.Millisecond)
	select {
	case code := <-done:
		t.Fatalf("task-done returned %d while peers were still running", code)
	default:
	}

	for _, id := range []string{"task-1", "task-2"} {
		task := run.GetTask(id)
		task.StartDispatch("d-"+id, "worker-"+id)
		task.CompleteDispatch("done", run)
	}

	select {
	case code := <-done:
		if code != http.StatusOK {
			t.Fatalf("task-done returned %d once peers finished, want 200", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("task-done did not return after every peer finished")
	}

	if got := run.GetTask("synth").CurrentState(); got != broker.TaskCompleted {
		t.Fatalf("synth state = %s, want completed", got)
	}
}

func TestTaskDoneAnnouncesTheWaitOnTheRadio(t *testing.T) {
	// A worker holding for minutes with no trace is indistinguishable from
	// a hang, both to a peer and to whoever is watching the monitor.
	addr, token, run, shutdown := gateFixture(t)
	defer shutdown()

	callTaskDone(t, addr, token, "synth", 200)

	var found *broker.Message
	for _, m := range run.MessagesSince(0) {
		if strings.HasPrefix(m.Content, "WAITING|") {
			found = m
		}
	}
	if found == nil {
		t.Fatal("the wait was not announced on the radio")
	}
	for _, peer := range []string{"task-1", "task-2"} {
		if !strings.Contains(found.Content, peer) {
			t.Errorf("announcement does not name %s: %q", peer, found.Content)
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

	code, _ := callTaskDone(t, addr, token, "synth", 10000)
	if code != http.StatusOK {
		t.Fatalf("task-done returned %d after every peer finished, want 200", code)
	}
	if got := run.GetTask("synth").CurrentState(); got != broker.TaskCompleted {
		t.Fatalf("synth state = %s, want completed", got)
	}
}

func TestTaskDoneUngatedForIndependentTasks(t *testing.T) {
	// The gate must not touch the ordinary case: a task with no deps
	// completes the moment it says so, without waiting on anything.
	addr, token, run, shutdown := gateFixture(t)
	defer shutdown()

	start := time.Now()
	code, _ := callTaskDone(t, addr, token, "task-1", 10000)
	if code != http.StatusOK {
		t.Fatalf("independent task-done returned %d, want 200", code)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("an independent task-done took %v — it is being gated", elapsed)
	}
	if got := run.GetTask("task-1").CurrentState(); got != broker.TaskCompleted {
		t.Fatalf("task-1 state = %s, want completed", got)
	}
}
