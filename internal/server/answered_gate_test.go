package server

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/keyolk/narwhal/internal/broker"
)

// After the check gate, a task-done whose outcome the judge says does not
// answer the task is handed back with a 202 once, and completes on the
// next call.
func TestANonAnswerIsHandedBackAtTaskDone(t *testing.T) {
	addr, token, run, shutdown := checkFixture(t, "")
	defer shutdown()
	run.SetJudge(broker.JudgeFunc(func(_ context.Context, _, name, _ string) (float64, bool) {
		return 0.03, name == broker.QuestionAnswered
	}))
	task := run.GetTask("task-1")
	task.StartDispatch("d1", "worker-task-1")

	code, out := callDoneWithCheck(t, addr, token, "task-1", "")
	if code != http.StatusAccepted || !strings.Contains(out["hint"].(string), "does not read as an answer") {
		t.Fatalf("first task-done: %d %v, want 202 with the reason", code, out)
	}
	if task.CurrentState() != broker.TaskDispatched {
		t.Fatalf("the task completed on a non-answer: %s", task.CurrentState())
	}
	if code, out = callDoneWithCheck(t, addr, token, "task-1", ""); code != http.StatusOK {
		t.Fatalf("second task-done: %d %v, want it completed", code, out)
	}
	if task.CurrentState() != broker.TaskCompleted {
		t.Errorf("state = %s", task.CurrentState())
	}
}
