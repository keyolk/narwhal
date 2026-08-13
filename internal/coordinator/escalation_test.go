package coordinator

import (
	"testing"

	"github.com/keyolk/narwhal/internal/broker"
)

func TestEscalationMovesTaskUpATier(t *testing.T) {
	c, run := claimCoord(t)
	run.GetTask("task-1").SetModel("haiku")
	run.PostMessage("worklog", "worker-task-1", nil, broker.PriorityUrgent,
		broker.FormatModelEscalateRequest("task-1", "", "needs deeper reading"))

	c.intakeGraphRequests()

	if got := run.GetTask("task-1").CurrentModel(); got != "sonnet" {
		t.Fatalf("model = %q, want sonnet", got)
	}
}

func TestEscalationHonoursAnExplicitModel(t *testing.T) {
	c, run := claimCoord(t)
	run.GetTask("task-1").SetModel("haiku")
	run.PostMessage("worklog", "worker-task-1", nil, broker.PriorityUrgent,
		broker.FormatModelEscalateRequest("task-1", "opus", "runtime tracing"))

	c.intakeGraphRequests()

	if got := run.GetTask("task-1").CurrentModel(); got != "opus" {
		t.Fatalf("model = %q, want opus", got)
	}
}

func TestEscalationStopsAtTheTopTier(t *testing.T) {
	c, run := claimCoord(t)
	run.GetTask("task-1").SetModel("opus")
	run.PostMessage("worklog", "worker-task-1", nil, broker.PriorityUrgent,
		broker.FormatModelEscalateRequest("task-1", "", "still hard"))

	c.intakeGraphRequests()

	if got := run.GetTask("task-1").CurrentModel(); got != "opus" {
		t.Fatalf("model = %q, want it to stay opus", got)
	}
}

func TestEscalationRetriesAnInFlightTask(t *testing.T) {
	// The point of escalating mid-flight is to get the work redone on a
	// model that can actually do it.
	c, run := claimCoord(t)
	task := run.GetTask("task-1")
	task.SetModel("haiku")
	task.StartDispatch("task-1-d1", "worker-task-1")

	run.PostMessage("worklog", "worker-task-1", nil, broker.PriorityUrgent,
		broker.FormatModelEscalateRequest("task-1", "", "too hard for this tier"))
	c.intakeGraphRequests()

	if got := task.CurrentState(); got != broker.TaskReady {
		t.Fatalf("state = %q, want ready so the stronger model picks it up", got)
	}
}

func TestEscalationDoesNotRerunACompletedTask(t *testing.T) {
	// A completed task has already produced its answer, and the synthesis
	// task may have drained it. Re-running would discard that.
	c, run := claimCoord(t)
	task := run.GetTask("task-1")
	task.SetModel("haiku")
	task.StartDispatch("task-1-d1", "worker-task-1")
	task.CompleteDispatch("done", run)

	run.PostMessage("worklog", "worker-task-1", nil, broker.PriorityUrgent,
		broker.FormatModelEscalateRequest("task-1", "opus", "late regret"))
	c.intakeGraphRequests()

	if got := task.CurrentState(); got != broker.TaskCompleted {
		t.Fatalf("state = %q, want the completed task left alone", got)
	}
}

func TestParseModelEscalateRequest(t *testing.T) {
	taskID, model, reason, ok := broker.ParseModelEscalateRequest(
		"MODEL_ESCALATE|task-9|opus|needs runtime evidence")
	if !ok {
		t.Fatal("well-formed escalation did not parse")
	}
	if taskID != "task-9" || model != "opus" || reason != "needs runtime evidence" {
		t.Fatalf("got %q/%q/%q", taskID, model, reason)
	}

	if _, _, _, ok := broker.ParseModelEscalateRequest("DEP_ADD|task-1|task-2"); ok {
		t.Error("a dep-edge message parsed as an escalation")
	}
}

func TestNextModelTier(t *testing.T) {
	for _, c := range []struct {
		in, want string
		ok       bool
	}{
		{"", "sonnet", true},
		{"haiku", "sonnet", true},
		{"sonnet", "opus", true},
		{"opus", "", false},
	} {
		got, ok := broker.NextModelTier(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("NextModelTier(%q) = %q,%v; want %q,%v", c.in, got, ok, c.want, c.ok)
		}
	}
}
