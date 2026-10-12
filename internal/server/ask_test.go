package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/keyolk/narwhal/internal/broker"
	"github.com/keyolk/narwhal/internal/launcher"
)

func postJSON(t *testing.T, url string, body any) map[string]any {
	t.Helper()
	b, _ := json.Marshal(body)
	resp, err := http.Post(url, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return out
}

func askFixture(t *testing.T) (addr, token string, run *broker.Run) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	b := broker.New()
	reg := broker.NewAgentRegistry()
	run = b.CreateRun("r-ask", "p", t.TempDir(), "main")
	run.CreateStandardThreads()
	task := run.AddTask("api", "api", "validate the lock key", nil)
	task.StartDispatch("d1", "worker-api")
	agent := reg.Register("worker-api", "r-ask", false)
	srv := New(b, reg)
	srv.SetController(&reopenController{cancelController: cancelController{l: launcher.New("http://x", "r-ask", t.TempDir())}})
	addr, err := srv.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Shutdown)
	return addr, agent.Token, run
}

// An assume ask is recorded and returns at once; the operator reads it with
// the run's others and answers; a running worker is told on the radio.
func TestAnAssumedQuestionIsRecordedAndAnsweredLater(t *testing.T) {
	addr, token, run := askFixture(t)
	out := postJSON(t, addr+"/api/v1/agents/"+token+"/ask",
		map[string]any{"question": "Reject an expired key or warn?", "assumed": "reject"})
	if out["status"] != "recorded" {
		t.Fatalf("ask: %v", out)
	}
	list := postJSONGet(t, addr+"/api/v1/control/questions?run_id=r-ask")
	if list["open"].(float64) != 1 {
		t.Fatalf("questions: %v", list)
	}
	ans := postJSON(t, addr+"/api/v1/control/answer", map[string]any{"run_id": "r-ask",
		"answers": []map[string]any{{"id": 1, "answer": "warn, and log it"}}})
	if ans["answered"].(float64) != 1 || ans["open"].(float64) != 0 {
		t.Fatalf("answer: %v", ans)
	}
	var told bool
	for _, m := range run.MessagesSince(0) {
		if m.Sender == "main" && strings.Contains(m.Content, "warn, and log it") && strings.Contains(m.Content, "differs") {
			told = true
		}
	}
	if !told {
		t.Error("the running worker was not told the answer differs")
	}
}

// A blocking ask waits for the answer and returns it.
func TestABlockingQuestionWaitsForTheAnswer(t *testing.T) {
	addr, token, run := askFixture(t)
	got := make(chan map[string]any, 1)
	go func() {
		got <- postJSON(t, addr+"/api/v1/agents/"+token+"/ask",
			map[string]any{"question": "Which branch?", "assumed": "main", "blocking": true})
	}()
	deadline := time.Now().Add(3 * time.Second)
	for len(run.OpenQuestions()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case out := <-got:
		t.Fatalf("a blocking ask returned before an answer: %v", out)
	case <-time.After(100 * time.Millisecond):
	}
	postJSON(t, addr+"/api/v1/control/answer", map[string]any{"run_id": "r-ask",
		"answers": []map[string]any{{"id": 1, "answer": "release-2"}}})
	select {
	case out := <-got:
		if out["status"] != "answered" || out["answer"] != "release-2" || out["as_assumed"] != false {
			t.Errorf("blocking ask returned %v", out)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the blocking ask did not return after the answer")
	}
}

// An answer that differs from what a finished task assumed reopens it.
func TestADifferingAnswerReopensAFinishedTask(t *testing.T) {
	addr, token, run := askFixture(t)
	postJSON(t, addr+"/api/v1/agents/"+token+"/ask",
		map[string]any{"question": "Reject an expired key or warn?", "assumed": "reject"})
	task := run.GetTask("api")
	task.CompleteDispatch("expired keys are rejected", run)

	out := postJSON(t, addr+"/api/v1/control/answer", map[string]any{"run_id": "r-ask",
		"answers": []map[string]any{{"id": 1, "answer": "warn"}}})
	if got, _ := json.Marshal(out["reopened"]); string(got) != `["api"]` {
		t.Fatalf("reopened %s, want api", got)
	}
	if fu := task.CurrentFollowUp(); !strings.Contains(fu, "Answer: warn") || !strings.Contains(fu, "You assumed: reject") {
		t.Errorf("follow-up = %q", fu)
	}
	// Confirming the assumption reopens nothing.
	postJSON(t, addr+"/api/v1/agents/"+token+"/ask", map[string]any{"question": "q2", "assumed": "a"})
	out = postJSON(t, addr+"/api/v1/control/answer", map[string]any{"run_id": "r-ask",
		"answers": []map[string]any{{"id": 2, "as_assumed": true}}})
	if got, _ := json.Marshal(out["reopened"]); string(got) != `[]` && string(got) != "null" {
		t.Errorf("confirming the assumption reopened %s", got)
	}
}

func postJSONGet(t *testing.T, url string) map[string]any {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return out
}
