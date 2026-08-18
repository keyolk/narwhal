package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/keyolk/narwhal/internal/broker"
)

// Following a run over MCP meant calling drain again and again: the
// transcripts show 92 narwhal_drain calls across 5 runs, in sequences like
// spawn → status → drain → drain → drain → drain. The server had a
// long-poll watch all along, but only agents could reach it — the operator
// path had no way to wait.

func controlFixture(t *testing.T) (string, *broker.Run) {
	t.Helper()
	b := broker.New()
	reg := broker.NewAgentRegistry()
	run := b.CreateRun("r1", "p", t.TempDir(), "main")
	run.CreateStandardThreads()

	srv := New(b, reg)
	srv.SetController(&cancelController{})
	addr, err := srv.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Shutdown)
	return addr, run
}

func controlDrain(t *testing.T, addr string, body map[string]any) cursorReply {
	t.Helper()
	raw, _ := json.Marshal(body)
	resp, err := http.Post(addr+"/api/v1/control/drain",
		"application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out cursorReply
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestControlDrainCursorNeverGoesBackwards(t *testing.T) {
	// The operator path had the same defect as the agent one, and this is
	// the path the transcripts were looping on.
	addr, run := controlFixture(t)
	run.PostMessage(broker.WorklogThread, "worker-1", nil, broker.PriorityNormal, "one")

	first := controlDrain(t, addr, map[string]any{"run_id": "r1", "after": 0})
	if len(first.Messages) != 1 {
		t.Fatalf("first drain returned %d messages", len(first.Messages))
	}
	quiet := controlDrain(t, addr, map[string]any{"run_id": "r1", "after": first.Cursor})
	if quiet.Cursor != first.Cursor {
		t.Fatalf("a quiet drain moved the cursor from %d to %d", first.Cursor, quiet.Cursor)
	}
	again := controlDrain(t, addr, map[string]any{"run_id": "r1", "after": quiet.Cursor})
	if len(again.Messages) != 0 {
		t.Errorf("following the cursor re-read %d messages", len(again.Messages))
	}
}

func TestAWaitingDrainWakesOnANewMessage(t *testing.T) {
	// The point of the wait: a finding should cost one call, not a poll
	// loop. If the wake did not work the call would sit out its timeout
	// and the feature would be worse than polling.
	addr, run := controlFixture(t)

	done := make(chan cursorReply, 1)
	go func() {
		done <- controlDrain(t, addr,
			map[string]any{"run_id": "r1", "after": 0, "wait_ms": 5000})
	}()

	time.Sleep(100 * time.Millisecond) // let the poll register its watch
	start := time.Now()
	run.PostMessage(broker.WorklogThread, "worker-1", nil, broker.PriorityNormal, "a finding")

	select {
	case reply := <-done:
		elapsed := time.Since(start)
		if len(reply.Messages) != 1 {
			t.Fatalf("the waiting drain returned %d messages", len(reply.Messages))
		}
		if reply.Timeout {
			t.Error("a drain that got its message reported a timeout")
		}
		if elapsed > 2*time.Second {
			t.Errorf("the wake took %v — it sat out the timeout instead", elapsed)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("the waiting drain never returned")
	}
}

func TestAWaitingDrainGivesUpAndSaysSo(t *testing.T) {
	// A caller that waited and got nothing has to be able to tell that
	// from a caller that got nothing immediately, or it cannot pace
	// itself.
	addr, _ := controlFixture(t)

	start := time.Now()
	reply := controlDrain(t, addr,
		map[string]any{"run_id": "r1", "after": 0, "wait_ms": 300})
	elapsed := time.Since(start)

	if !reply.Timeout {
		t.Error("an expired wait did not report a timeout")
	}
	if elapsed < 250*time.Millisecond {
		t.Errorf("the drain returned after %v without waiting", elapsed)
	}
}

func TestAnImmediateDrainDoesNotWait(t *testing.T) {
	// Omitting wait_ms must keep the old behaviour: this is the call the
	// monitor and every existing caller make.
	addr, _ := controlFixture(t)

	start := time.Now()
	controlDrain(t, addr, map[string]any{"run_id": "r1", "after": 0})
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Errorf("a drain with no wait_ms took %v", elapsed)
	}
}

func TestAWaitingDrainReturnsAtOnceWhenThereIsAlreadyNews(t *testing.T) {
	// Waiting must not delay a message that is already there.
	addr, run := controlFixture(t)
	run.PostMessage(broker.WorklogThread, "worker-1", nil, broker.PriorityNormal, "already here")

	start := time.Now()
	reply := controlDrain(t, addr,
		map[string]any{"run_id": "r1", "after": 0, "wait_ms": 5000})
	if len(reply.Messages) != 1 {
		t.Fatalf("returned %d messages", len(reply.Messages))
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("a drain with news waiting still took %v", elapsed)
	}
}
