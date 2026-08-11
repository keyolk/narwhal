package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/keyolk/narwhal/internal/broker"
)

func TestWatchLongPollReceivesMessage(t *testing.T) {
	b := broker.New()
	reg := broker.NewAgentRegistry()

	runID := "test-run"
	run := b.CreateRun(runID, "test", "/tmp", "main")
	run.CreateThread("worklog", "worklog", []string{"worker-1", "worker-2"})

	sender := reg.Register("sender", runID, false)
	receiver := reg.Register("worker-1", runID, false)

	srv := New(b, reg)
	addr, err := srv.Start()
	if err != nil {
		t.Fatalf("start server: %v", err)
	}
	defer srv.Shutdown()

	// Start a watch in a goroutine. It should block until a message arrives.
	type watchResp struct {
		Messages []*broker.Message `json:"messages"`
		Cursor   int64             `json:"cursor"`
	}
	result := make(chan watchResp, 1)
	go func() {
		body, _ := json.Marshal(map[string]any{"after": 0, "timeout_ms": 5000})
		resp, err := http.Post(addr+"/api/v1/agents/"+receiver.Token+"/watch", "application/json", bytes.NewReader(body))
		if err != nil {
			result <- watchResp{}
			return
		}
		defer resp.Body.Close()
		var wr watchResp
		_ = json.NewDecoder(resp.Body).Decode(&wr)
		result <- wr
	}()

	// Give the watch a moment to register, then post a message.
	time.Sleep(200 * time.Millisecond)
	postBody, _ := json.Marshal(map[string]any{
		"thread_id": "worklog",
		"content":   "hello from sender",
		"mentions":  []string{"worker-1"},
		"priority":  "urgent",
	})
	resp, err := http.Post(addr+"/api/v1/agents/"+sender.Token+"/send", "application/json", bytes.NewReader(postBody))
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	resp.Body.Close()

	// The watch should resolve with the message.
	select {
	case wr := <-result:
		if len(wr.Messages) != 1 {
			t.Fatalf("expected 1 message, got %d", len(wr.Messages))
		}
		if wr.Messages[0].Content != "hello from sender" {
			t.Fatalf("content = %q, want 'hello from sender'", wr.Messages[0].Content)
		}
		if wr.Messages[0].Priority != broker.PriorityUrgent {
			t.Fatalf("priority = %q, want urgent", wr.Messages[0].Priority)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("watch did not resolve within timeout")
	}
}

func TestDrainReturnsMentionedMessages(t *testing.T) {
	b := broker.New()
	reg := broker.NewAgentRegistry()

	runID := "drain-run"
	run := b.CreateRun(runID, "test", "/tmp", "main")
	run.CreateThread("worklog", "worklog", []string{"worker-1", "worker-2"})

	sender := reg.Register("sender", runID, false)
	receiver := reg.Register("worker-1", runID, false)

	srv := New(b, reg)
	addr, err := srv.Start()
	if err != nil {
		t.Fatalf("start server: %v", err)
	}
	defer srv.Shutdown()

	// Post a message mentioning worker-1.
	postBody, _ := json.Marshal(map[string]any{
		"thread_id": "worklog",
		"content":   "drain test message",
		"mentions":  []string{"worker-1"},
		"priority":  "normal",
	})
	resp, err := http.Post(addr+"/api/v1/agents/"+sender.Token+"/send", "application/json", bytes.NewReader(postBody))
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	resp.Body.Close()

	// Drain should return the message.
	drainBody, _ := json.Marshal(map[string]any{"after": 0})
	resp, err = http.Post(addr+"/api/v1/agents/"+receiver.Token+"/drain", "application/json", bytes.NewReader(drainBody))
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	defer resp.Body.Close()

	var drainResp struct {
		Messages []*broker.Message `json:"messages"`
		Cursor   int64             `json:"cursor"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&drainResp); err != nil {
		t.Fatalf("decode drain: %v", err)
	}
	if len(drainResp.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(drainResp.Messages))
	}
	if drainResp.Cursor != 1 {
		t.Fatalf("cursor = %d, want 1", drainResp.Cursor)
	}

	// Second drain with after=1 should return nothing.
	drainBody2, _ := json.Marshal(map[string]any{"after": 1})
	resp2, err := http.Post(addr+"/api/v1/agents/"+receiver.Token+"/drain", "application/json", bytes.NewReader(drainBody2))
	if err != nil {
		t.Fatalf("drain2: %v", err)
	}
	defer resp2.Body.Close()
	var drainResp2 struct {
		Messages []*broker.Message `json:"messages"`
	}
	_ = json.NewDecoder(resp2.Body).Decode(&drainResp2)
	if len(drainResp2.Messages) != 0 {
		t.Fatalf("expected 0 messages after cursor, got %d", len(drainResp2.Messages))
	}
}

func TestWatchTimeout(t *testing.T) {
	b := broker.New()
	reg := broker.NewAgentRegistry()

	runID := "timeout-run"
	b.CreateRun(runID, "test", "/tmp", "main")
	receiver := reg.Register("worker-1", runID, false)

	srv := New(b, reg)
	addr, err := srv.Start()
	if err != nil {
		t.Fatalf("start server: %v", err)
	}
	defer srv.Shutdown()

	// Watch with a short timeout and no messages posted.
	body, _ := json.Marshal(map[string]any{"after": 0, "timeout_ms": 500})
	start := time.Now()
	resp, err := http.Post(addr+"/api/v1/agents/"+receiver.Token+"/watch", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("watch: %v", err)
	}
	defer resp.Body.Close()

	var result struct {
		Timeout bool `json:"timeout"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&result)
	elapsed := time.Since(start)

	if !result.Timeout {
		t.Fatal("expected timeout=true")
	}
	if elapsed < 400*time.Millisecond {
		t.Fatalf("watch returned too fast: %v", elapsed)
	}
}
