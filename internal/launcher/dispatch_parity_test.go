package launcher

import (
	"strings"
	"testing"
	"unicode/utf8"

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

	cfg := WorkerConfigFor(run, task)

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
	instr := buildAgentInstructions(a, WorkerConfigFor(run, task), "/scripts")

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
	instr := buildAgentInstructions(a, WorkerConfigFor(run, task), "/scripts")

	if strings.Contains(instr, "end condition") {
		t.Errorf("a task with no check is told about one:\n%s", instr)
	}
}

// The worker sees the run's request, not only its slice of it. On
// SWE-Atlas measure-2 three of four workers got "(read-only): trace…" for a
// question about what is visible on screen, and none of them knew.
func TestTheInstructionsCarryTheRunRequest(t *testing.T) {
	b := broker.New()
	run := b.CreateRun("r1", "report what kitty shows on screen at startup", "/tmp", "main")
	task := run.AddTask("task-2", "n", "(read-only): trace the config loader", nil)

	cfg := WorkerConfigFor(run, task)
	if cfg.Request != "report what kitty shows on screen at startup" {
		t.Fatalf("Request = %q, want the run's request", cfg.Request)
	}
	reg := broker.NewAgentRegistry()
	a := reg.Register("worker-task-2", "r1", false)
	instr := buildAgentInstructions(a, cfg, "/scripts")
	if !strings.Contains(instr, "<request>\nreport what kitty shows on screen at startup\n</request>") {
		t.Error("the run's request is not in the worker's instructions")
	}
	if !strings.Contains(instr, "Before you conclude it cannot be done") {
		t.Error("the instructions do not ask for alternatives before a negative conclusion")
	}

	// A one-task run whose assignment is the request does not repeat it.
	solo := run.AddTask("solo", "n", "report what kitty shows on screen at startup", nil)
	if strings.Contains(buildAgentInstructions(a, WorkerConfigFor(run, solo), "/scripts"), "<request>") {
		t.Error("an assignment identical to the request was quoted twice")
	}
}

func TestALongRequestIsClippedOnARuneBoundary(t *testing.T) {
	s := strings.Repeat("가", requestLimit) // 3 bytes each
	got := clipRequest(s)
	if !strings.HasSuffix(got, "(request truncated)") || !utf8.ValidString(got) {
		t.Errorf("clipped request is not valid UTF-8 or not marked: …%q", got[len(got)-40:])
	}
}
