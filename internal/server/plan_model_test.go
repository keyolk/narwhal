package server

import (
	"strings"
	"testing"
)

// The planner is told to pick a model per task — opus for synthesis,
// something cheaper for narrow investigation — and every layer beneath it
// carries the choice: the task endpoint reads a "model" field, SetModel
// stores it, and the launcher passes it to claude as --model.
//
// Not one task in the history has a model set. 154 of 154 are empty, so
// every worker ran on whatever ccproxy's rotation handed it and the
// escalate wrapper was invoked exactly once.
//
// The reason is in the instructions themselves: the curl the planner is
// shown to copy has no model field in it. The prose two paragraphs later
// says to set one. A model follows the example.

func TestThePlannerIsShownAModelInTheExample(t *testing.T) {
	instr := BuildPlanInstructions("r1", "http://127.0.0.1:1", "tok", "do a thing")

	// The example task-creation call, which is what gets copied.
	i := strings.Index(instr, "/task \\")
	if i < 0 {
		t.Fatal("no task-creation example in the instructions")
	}
	example := instr[i:]
	if j := strings.Index(example, "\n\n"); j > 0 {
		example = example[:j]
	}
	if !strings.Contains(example, `"model"`) {
		t.Errorf("the example the planner copies has no model field:\n%s", example)
	}
}

func TestTheSynthesisExampleAsksForTheStrongerModel(t *testing.T) {
	// The one place the choice matters most, and the one the prose is
	// most specific about.
	instr := BuildPlanInstructions("r1", "http://127.0.0.1:1", "tok", "do a thing")
	if !strings.Contains(instr, `"model":"opus"`) {
		t.Error("no concrete synthesis example with model set to opus")
	}
}

func TestEveryTaskURLNamesTheRun(t *testing.T) {
	// Adding the second example shifted the Sprintf arguments and the
	// first curl came out as /run/PROMPT/task — the planner would have
	// posted every task to a run that does not exist. go vet caught the
	// count; only rendering catches the order.
	instr := BuildPlanInstructions("RUNID", "http://BROKER", "TOKEN", "PROMPT")
	n := 0
	for _, line := range strings.Split(instr, "\n") {
		if !strings.Contains(line, "/api/v1/run/") {
			continue
		}
		n++
		if !strings.Contains(line, "/run/RUNID/") {
			t.Errorf("a run URL does not name the run: %s", strings.TrimSpace(line))
		}
	}
	if n < 2 {
		t.Errorf("expected at least two run URLs in the instructions, found %d", n)
	}
}
