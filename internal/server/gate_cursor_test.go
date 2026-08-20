package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/keyolk/narwhal/internal/broker"
)

// The 202 exists to hand a worker the messages it could not have folded in.
// It computed that set as `lastSeq(run.MessagesSince(0))` at gate entry —
// the server's global last seq, not the calling worker's cursor. So a peer
// message posted before task-done but after the worker's last drain is
// excluded by construction: it is already "seen" as far as the gate is
// concerned, and the worker is never told about it.
//
// Reproduced from the run that exposed it, s1786665646933-1:
//
//	seq=2  worker-investigate: "Read note.md — fact: the answer is 42"
//	seq=4  coordinator:        "WAITING|synthesis|..."
//
// The synthesis worker had drained to seq 1. The run's only finding was
// seq 2. The 202 handed back seq 4 — the coordinator's own control message
// — and nothing else. That worker recovered because it independently ran
// `drain 1` from its own cursor; one that trusted the 202 body, which is
// what the hint tells it to do, would have completed on an empty answer.
//
// The gate engaged 9 times across the stored history, and every engagement
// had between 1 and 42 substantive messages already on the channel.

// callTaskDoneAfter posts task-done carrying the worker's own cursor.
func callTaskDoneAfter(t *testing.T, addr, token, taskID string, after int64,
	timeoutMs int, final bool) (int, map[string]any) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"outcome":    "here is the answer",
		"timeout_ms": timeoutMs,
		"final":      final,
		"after":      after,
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

func TestTheGateHandsBackWhatTheWorkerHasNotSeen(t *testing.T) {
	addr, token, run, shutdown := gateFixture(t)
	defer shutdown()

	// The worker drained the channel, then a peer posted a finding, then
	// the worker called task-done. This is the ordinary interleaving: the
	// finding lands in the window between the last drain and the gate.
	run.PostMessage(broker.WorklogThread, "worker-task-1", nil,
		broker.PriorityNormal, "seen before the worker drained")
	drained := int64(1)
	run.PostMessage(broker.WorklogThread, "worker-task-1", nil,
		broker.PriorityUrgent, "the answer is 42")

	// Both deps are still running when the gate is entered, so it engages;
	// they finish during the wait, which is what the 202 path is for.
	run.GetTask("task-1").StartDispatch("d1", "worker-task-1")
	run.GetTask("task-2").StartDispatch("d2", "worker-task-2")
	go func() {
		time.Sleep(150 * time.Millisecond)
		run.GetTask("task-1").CompleteDispatch("done", run)
		run.GetTask("task-2").CompleteDispatch("done", run)
	}()

	code, body := callTaskDoneAfter(t, addr, token, "synth", drained, 3000, false)
	if code != http.StatusAccepted {
		t.Fatalf("the gate returned %d, want 202: %v", code, body)
	}

	msgs, _ := body["new_messages"].([]any)
	var joined string
	for _, m := range msgs {
		if mm, ok := m.(map[string]any); ok {
			s, _ := mm["Content"].(string)
			joined += s + "\n"
		}
	}
	if !contains(joined, "the answer is 42") {
		t.Errorf("the 202 did not hand back the finding the worker missed.\n"+
			"got %d messages:\n%s", len(msgs), joined)
	}
}

func TestTheGateDoesNotRepeatWhatTheWorkerAlreadyRead(t *testing.T) {
	// The other direction. A worker that drained everything must not be
	// handed its own history back — that is how a fold-in loop fails to
	// terminate.
	addr, token, run, shutdown := gateFixture(t)
	defer shutdown()

	run.PostMessage(broker.WorklogThread, "worker-task-1", nil,
		broker.PriorityNormal, "old news the worker already folded in")
	drained := lastSeq(run.MessagesSince(0))

	run.GetTask("task-1").StartDispatch("d1", "worker-task-1")
	run.GetTask("task-2").StartDispatch("d2", "worker-task-2")
	go func() {
		time.Sleep(150 * time.Millisecond)
		run.GetTask("task-1").CompleteDispatch("done", run)
		run.GetTask("task-2").CompleteDispatch("done", run)
	}()

	_, body := callTaskDoneAfter(t, addr, token, "synth", drained, 3000, false)
	msgs, _ := body["new_messages"].([]any)
	for _, m := range msgs {
		if mm, ok := m.(map[string]any); ok {
			if s, _ := mm["Content"].(string); contains(s, "old news") {
				t.Errorf("the 202 repeated a message the worker had read: %q", s)
			}
		}
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && bytes.Contains([]byte(haystack), []byte(needle))
}
