package server

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/keyolk/narwhal/internal/broker"
)

// The outcome a worker posts to task-done was stored on the in-memory
// dispatch and read by nobody. This walks the path an operator actually
// takes — worker posts, `narwhal_status` asks — and asserts the answer
// comes back.
func TestTheOutcomeAWorkerPostsComesBackFromStatus(t *testing.T) {
	addr, token, run, shutdown := gateFixture(t)
	defer shutdown()

	// A real worker only holds a token because the dispatcher started a
	// dispatch for it, and the outcome is recorded on that dispatch.
	run.GetTask("task-1").StartDispatch("d1", "worker-synth")

	// task-1 has no deps, so the completion gate lets it through.
	code, _ := callTaskDone(t, addr, token, "task-1", 50)
	if code != http.StatusOK {
		t.Fatalf("task-done returned %d", code)
	}

	resp, err := http.Get(addr + "/api/v1/run/" + run.ID)
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	defer resp.Body.Close()
	var snap broker.Snapshot
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}

	for _, ts := range snap.Tasks {
		if ts.ID != "task-1" {
			continue
		}
		if ts.Outcome != "here is the answer" {
			t.Errorf("the API reports outcome %q, want what the worker posted", ts.Outcome)
		}
		return
	}
	t.Fatal("task-1 is missing from the snapshot")
}
