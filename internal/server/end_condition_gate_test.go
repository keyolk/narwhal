package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/keyolk/narwhal/internal/broker"
)

// checkFixture is a run with one dependency-free task that carries an end
// condition, so the check gate can be exercised without the dep gate
// interfering.
func checkFixture(t *testing.T, check string) (addr, token string, run *broker.Run, shutdown func()) {
	t.Helper()
	b := broker.New()
	reg := broker.NewAgentRegistry()

	runID := "run-check"
	run = b.CreateRun(runID, "test", "/tmp", "main")
	run.CreateStandardThreads()
	task := run.AddTask("task-1", "investigate", "a", nil)
	task.SetCheck(check)

	agent := reg.Register("worker-task-1", runID, false)
	srv := New(b, reg)
	addr, err := srv.Start()
	if err != nil {
		t.Fatalf("start server: %v", err)
	}
	return addr, agent.Token, run, srv.Shutdown
}

// callDoneWithCheck posts task-done carrying a check result (empty for
// the first call, which is what a worker sends before it has been asked).
func callDoneWithCheck(t *testing.T, addr, token, taskID, checkResult string) (int, map[string]any) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"outcome":      "here is the answer",
		"timeout_ms":   300,
		"check_result": checkResult,
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

// The gap this closes: run s1787538246213-1 answered 8 where the answer
// was 0 and finished 3/3 completed, with no retry, no failed task and no
// stuck frontier. Every field the snapshot recorded said it went well.
func TestATaskWithACheckDoesNotCompleteUntilItAnswersOne(t *testing.T) {
	addr, token, run, shutdown := checkFixture(t,
		"Confirm the names you report are actually exported.")
	defer shutdown()

	code, body := callDoneWithCheck(t, addr, token, "task-1", "")
	if code != http.StatusAccepted {
		t.Fatalf("task-done returned %d, want 202 asking for the check", code)
	}
	if body["check"] != "Confirm the names you report are actually exported." {
		t.Errorf("the 202 does not hand back the check: %v", body["check"])
	}
	if got := run.GetTask("task-1").CurrentState(); got == broker.TaskCompleted {
		t.Error("the task completed without answering its check")
	}
}

// 202 rather than 409, for the reason #17 established: a --print worker's
// process ends with its turn, so a worker told to come back later does not
// come back. The 202 keeps it inside a tool call.
func TestTheCheckGateAnswersRatherThanRefusing(t *testing.T) {
	addr, token, _, shutdown := checkFixture(t, "check something")
	defer shutdown()

	code, body := callDoneWithCheck(t, addr, token, "task-1", "")
	if code == http.StatusConflict {
		t.Fatal("the gate refused with 409; a refused worker dies rather than retrying")
	}
	if code != http.StatusAccepted {
		t.Fatalf("code = %d, want 202", code)
	}
	if body["hint"] == nil {
		t.Error("the 202 carries no hint telling the worker what to do next")
	}
}

func TestAnsweringTheCheckCompletesTheTask(t *testing.T) {
	addr, token, run, shutdown := checkFixture(t, "check something")
	defer shutdown()

	if code, _ := callDoneWithCheck(t, addr, token, "task-1", ""); code != http.StatusAccepted {
		t.Fatalf("first call returned %d, want 202", code)
	}
	code, _ := callDoneWithCheck(t, addr, token, "task-1", "ran it: 3 of 3 confirmed")
	if code != http.StatusOK {
		t.Fatalf("second call returned %d, want 200", code)
	}
	if got := run.GetTask("task-1").CurrentState(); got != broker.TaskCompleted {
		t.Errorf("state = %s, want completed", got)
	}
}

// The check and what it showed both land in the record. A wrong-but-
// completed answer is otherwise invisible, which is exactly how #41 had
// to be found by hand.
func TestTheCheckAndItsResultReachTheSnapshot(t *testing.T) {
	addr, token, run, shutdown := checkFixture(t, "confirm the names are exported")
	defer shutdown()

	callDoneWithCheck(t, addr, token, "task-1", "")
	callDoneWithCheck(t, addr, token, "task-1", "none of the six names is exported")

	var found bool
	for _, ts := range run.SnapshotTasks() {
		if ts.ID != "task-1" {
			continue
		}
		found = true
		if ts.Check != "confirm the names are exported" {
			t.Errorf("Check = %q, want the planner's end condition", ts.Check)
		}
		if ts.CheckResult != "none of the six names is exported" {
			t.Errorf("CheckResult = %q, want what the worker reported", ts.CheckResult)
		}
	}
	if !found {
		t.Fatal("task-1 missing from the snapshot")
	}
}

// Most tasks have no meaningful end condition, and a gate that stopped
// them all would make the field mandatory by the back door.
func TestATaskWithNoCheckCompletesOnTheFirstCall(t *testing.T) {
	addr, token, run, shutdown := checkFixture(t, "")
	defer shutdown()

	code, _ := callDoneWithCheck(t, addr, token, "task-1", "")
	if code != http.StatusOK {
		t.Fatalf("task-done returned %d, want 200 for a task with no check", code)
	}
	if got := run.GetTask("task-1").CurrentState(); got != broker.TaskCompleted {
		t.Errorf("state = %s, want completed", got)
	}
}

// A worker that already knows its check can answer it in one call rather
// than being sent round the loop for a result it is holding.
func TestACheckAnsweredOnTheFirstCallCompletesImmediately(t *testing.T) {
	addr, token, run, shutdown := checkFixture(t, "check something")
	defer shutdown()

	code, _ := callDoneWithCheck(t, addr, token, "task-1", "already ran it: confirmed")
	if code != http.StatusOK {
		t.Fatalf("task-done returned %d, want 200 when the check is answered up front", code)
	}
	if got := run.GetTask("task-1").CurrentState(); got != broker.TaskCompleted {
		t.Errorf("state = %s, want completed", got)
	}
}

// The check is set by the planner at decomposition time, before any
// worker has an answer to defend. A check composed at task-done would be
// a justification.
func TestTheTaskAPIAcceptsACheckAtCreation(t *testing.T) {
	b := broker.New()
	reg := broker.NewAgentRegistry()
	run := b.CreateRun("run-api", "test", "/tmp", "main")
	run.CreateStandardThreads()
	srv := New(b, reg)
	addr, err := srv.Start()
	if err != nil {
		t.Fatalf("start server: %v", err)
	}
	defer srv.Shutdown()

	body, _ := json.Marshal(map[string]any{
		"id": "task-9", "name": "n", "assignment": "a", "deps": []string{},
		"check": "the count equals the number of names listed",
	})
	resp, err := http.Post(addr+"/api/v1/run/run-api/task", "application/json",
		bytes.NewReader(body))
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	resp.Body.Close()

	if got := run.GetTask("task-9").CurrentCheck(); got != "the count equals the number of names listed" {
		t.Errorf("CurrentCheck = %q, want the check the planner posted", got)
	}
}
