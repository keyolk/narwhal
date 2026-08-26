package launcher

import (
	"strings"
	"testing"

	"github.com/keyolk/narwhal/internal/broker"
)

// Two dispatch paths build a WorkerConfig — the batch coordinator and
// this one — and they drifted. Model was passed by the coordinator and
// dropped here, so a tier the planner chose reached the graph, the
// monitor and the snapshot but never the worker.
//
// It is measurable in the corpus: of the 13 tasks on disk that named a
// tier explicitly, the 5 served by a different model are all interactive
// runs, and the batch runs have none. Accounting is what made a drift
// that had been invisible for the life of the feature countable.
//
// This test guards the fields, not the mechanism: adding one to
// WorkerConfig and wiring it in only one of the two paths is the shape of
// the defect, and it recurs because the two sites look nothing alike.
func TestDispatchCarriesEveryTaskFieldTheWorkerNeeds(t *testing.T) {
	b := broker.New()
	run := b.CreateRun("r1", "p", "/tmp", "main")
	task := run.AddTask("task-1", "n", "do the thing", nil)
	task.SetModel("haiku")
	task.SetCheck("confirm the names are exported")

	cfg := WorkerConfigFor(task)

	if cfg.TaskID != "task-1" {
		t.Errorf("TaskID = %q", cfg.TaskID)
	}
	if cfg.Assignment != "do the thing" {
		t.Errorf("Assignment = %q", cfg.Assignment)
	}
	if cfg.Model != "haiku" {
		t.Errorf("Model = %q, want the tier the planner chose — a model set "+
			"on the task and not passed here never reaches the worker", cfg.Model)
	}
	if cfg.Check != "confirm the names are exported" {
		t.Errorf("Check = %q, want the task's end condition", cfg.Check)
	}
}

// The check has to be in the instructions, not only in the 202. A worker
// that first meets it at task-done has already written its answer and
// dropped the context it needed to test it.
func TestTheInstructionsCarryTheCheckWhenTheTaskHasOne(t *testing.T) {
	b := broker.New()
	run := b.CreateRun("r1", "p", "/tmp", "main")
	task := run.AddTask("task-1", "n", "do the thing", nil)
	task.SetCheck("confirm the names you report are actually exported")

	reg := broker.NewAgentRegistry()
	a := reg.Register("worker-task-1", "r1", false)
	instr := buildAgentInstructions(a, WorkerConfigFor(task), "/scripts")

	if !strings.Contains(instr, "confirm the names you report are actually exported") {
		t.Errorf("the check is missing from the instructions:\n%s", instr)
	}
	// And how to answer it, or the worker loops on the 202.
	if !strings.Contains(instr, "what the check showed") {
		t.Errorf("the instructions do not show how to report the result:\n%s", instr)
	}
	// A check that only ever confirms is not a check.
	if !strings.Contains(instr, "contradicts your") {
		t.Errorf("nothing tells the worker a contradicting result is the valuable one:\n%s", instr)
	}
}

// Most tasks carry no check, and a section explaining one they do not
// have is noise that competes with the instructions that matter.
func TestTheInstructionsSayNothingAboutChecksWhenThereIsNone(t *testing.T) {
	b := broker.New()
	run := b.CreateRun("r1", "p", "/tmp", "main")
	task := run.AddTask("task-1", "n", "do the thing", nil)

	reg := broker.NewAgentRegistry()
	a := reg.Register("worker-task-1", "r1", false)
	instr := buildAgentInstructions(a, WorkerConfigFor(task), "/scripts")

	if strings.Contains(instr, "end condition") {
		t.Errorf("a task with no check is told about one:\n%s", instr)
	}
}
