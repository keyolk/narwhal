package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/keyolk/narwhal/internal/broker"
)

func judgeServer(t *testing.T, j broker.Judge) (addr string, token string, run *broker.Run) {
	t.Helper()
	b := broker.New()
	b.SetJudge(j)
	reg := broker.NewAgentRegistry()
	run = b.CreateRun("r-judge", "test", t.TempDir(), "main")
	a := reg.Register("worker-api", "r-judge", false)
	srv := New(b, reg)
	addr, err := srv.Start()
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { srv.Shutdown() })
	return addr, a.Token, run
}

func postJudge(t *testing.T, addr, token string, body map[string]any) (int, map[string]any) {
	t.Helper()
	raw, _ := json.Marshal(body)
	resp, err := http.Post(addr+"/api/v1/agents/"+token+"/judge", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestWorkerJudgeAnswersAndIsRecorded(t *testing.T) {
	var gotState, gotQ string
	j := broker.JudgeFunc(func(_ context.Context, state, _ string, q string) (float64, bool) {
		gotState, gotQ = state, q
		return 0.83, true
	})
	addr, token, run := judgeServer(t, j)

	code, out := postJudge(t, addr, token, map[string]any{
		"question": "Is this test failure flaky rather than a real bug?",
		"state":    "--- FAIL: TestReplay (0.01s) nonce reused",
	})
	if code != http.StatusOK || out["decided"] != true || out["probability"] != 0.83 {
		t.Fatalf("response = %d %v", code, out)
	}
	if gotQ != "Is this test failure flaky rather than a real bug?" || gotState == "" {
		t.Errorf("judge got q=%q state=%q", gotQ, gotState)
	}
	vs := run.Verdicts()
	if len(vs) != 1 || vs[0].Asker != "worker-api" || vs[0].TaskID != "api" ||
		vs[0].Action != broker.ActionAnswered || vs[0].Ask == "" {
		t.Errorf("verdict = %+v", vs)
	}
}

// No judge is not an error: the worker is told there is no answer and
// decides itself, and the question is still on the record.
func TestWorkerJudgeWithoutAJudgeIsUndecided(t *testing.T) {
	addr, token, run := judgeServer(t, nil)
	code, out := postJudge(t, addr, token, map[string]any{"question": "q?", "state": "s"})
	if code != http.StatusOK || out["decided"] != false {
		t.Fatalf("response = %d %v", code, out)
	}
	if _, has := out["probability"]; has {
		t.Error("an undecided answer carried a probability")
	}
	if vs := run.Verdicts(); len(vs) != 1 || vs[0].Decided {
		t.Errorf("verdicts = %+v", vs)
	}
}

func TestWorkerJudgeNeedsBothQuestionAndState(t *testing.T) {
	addr, token, _ := judgeServer(t, nil)
	if code, _ := postJudge(t, addr, token, map[string]any{"question": "q?"}); code != http.StatusBadRequest {
		t.Errorf("missing state: %d", code)
	}
	if code, _ := postJudge(t, addr, token, map[string]any{"state": "s"}); code != http.StatusBadRequest {
		t.Errorf("missing question: %d", code)
	}
}
