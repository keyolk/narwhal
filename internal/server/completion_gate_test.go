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
	return callTaskDoneWith(t, addr, token, taskID, timeoutMs, false)
}

// callTaskDoneWith is callTaskDone with control over the final flag, which
// says the worker has already folded in what arrived during the wait.
func callTaskDoneWith(t *testing.T, addr, token, taskID string, timeoutMs int, final bool) (int, map[string]any) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"outcome":    "here is the answer",
		"timeout_ms": timeoutMs,
		"final":      final,
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
	// wait, so it gets an answer on its first call rather than being told
	// to try again — advice a --print worker cannot act on, because its
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
		// 202, not 200: the call returns as soon as the peers finish, and
		// hands back what landed during the wait rather than completing on
		// a stale outcome. What matters here is that it returned at all —
		// the worker's turn survived the wait.
		if code != http.StatusAccepted && code != http.StatusOK {
			t.Fatalf("task-done returned %d once peers finished, want 202 or 200", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("task-done did not return after every peer finished")
	}

	if got := run.PendingDeps("synth"); len(got) != 0 {
		t.Fatalf("the call returned with %v still pending", got)
	}
}

func TestPeerFindingsArrivingDuringTheWaitAreHandedBack(t *testing.T) {
	// Waiting fixes the ordering but leaves the content stale: the outcome
	// was written before the wait, so completing on it records a synthesis
	// that never saw what arrived. Seen on a real run — task-done held 100
	// seconds, the peer posted its finding during the hold, and the stored
	// outcome still read "nothing to synthesize".
	addr, token, run, shutdown := gateFixture(t)
	defer shutdown()

	type result struct {
		code int
		body map[string]any
	}
	done := make(chan result, 1)
	go func() {
		code, body := callTaskDone(t, addr, token, "synth", 10000)
		done <- result{code, body}
	}()

	time.Sleep(200 * time.Millisecond)
	// The peer posts its finding, then finishes — the exact ordering that
	// leaves the synthesis worker's outcome behind.
	run.PostMessage(broker.WorklogThread, "worker-task-1", nil, broker.PriorityNormal,
		"fact A: the color is blue")
	for _, id := range []string{"task-1", "task-2"} {
		task := run.GetTask(id)
		task.StartDispatch("d-"+id, "worker-"+id)
		task.CompleteDispatch("done", run)
	}

	select {
	case r := <-done:
		if r.code != http.StatusAccepted {
			t.Fatalf("task-done returned %d, want 202 when findings arrived during the wait", r.code)
		}
		msgs, _ := r.body["new_messages"].([]any)
		if len(msgs) == 0 {
			t.Fatalf("202 carried no messages: %v", r.body)
		}
		found := false
		for _, m := range msgs {
			if mm, ok := m.(map[string]any); ok {
				if c, _ := mm["Content"].(string); strings.Contains(c, "the color is blue") {
					found = true
				}
			}
		}
		if !found {
			t.Errorf("the peer's finding was not handed back: %v", msgs)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("task-done did not return")
	}

	if got := run.GetTask("synth").CurrentState(); got == broker.TaskCompleted {
		t.Fatal("the task completed on an outcome written before its peers posted")
	}
}

func TestFinalCallCompletesAfterFoldingFindingsIn(t *testing.T) {
	// The second call carries the updated answer and must complete — or
	// the worker would be handed the same messages forever.
	addr, token, run, shutdown := gateFixture(t)
	defer shutdown()

	run.PostMessage(broker.WorklogThread, "worker-task-1", nil, broker.PriorityNormal,
		"fact A: the color is blue")
	for _, id := range []string{"task-1", "task-2"} {
		task := run.GetTask(id)
		task.StartDispatch("d-"+id, "worker-"+id)
		task.CompleteDispatch("done", run)
	}

	code, _ := callTaskDoneWith(t, addr, token, "synth", 10000, true)
	if code != http.StatusOK {
		t.Fatalf("final task-done returned %d, want 200", code)
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
