package server

import (
	"os"
	"strings"
	"testing"

	"github.com/keyolk/narwhal/internal/broker"
)

// A planner decomposing a request has never been shown how the same
// repository was decomposed before. Every run starts from the prompt alone,
// so a decomposition that deadlocked last week is as likely to be produced
// again as one that worked. 44 snapshots sit in ~/.narwhal/runs and none of
// them is read at plan time.

func TestThePlannerIsShownPastDecompositions(t *testing.T) {
	past := []broker.Snapshot{{
		RunID: "s100-1", Prompt: "audit the broker for races", CWD: "/repo",
		State: broker.RunDone,
		Tasks: []broker.TaskSnapshot{
			{ID: "task-1", Name: "locks", Deps: []string{}, State: broker.TaskCompleted,
				Model: "haiku", Outcome: "broker.go holds mu across the dispatch call"},
			{ID: "synthesis", Name: "synthesis", Deps: []string{"task-1"},
				State: broker.TaskCompleted, Model: "opus", Outcome: "one real race"},
		},
	}}

	instr := BuildPlanInstructionsWithHistory(
		"r1", "http://127.0.0.1:1", "tok", "audit the broker again", "/repo", past)

	for _, want := range []string{"s100-1", "task-1", "synthesis", "haiku", "opus"} {
		if !strings.Contains(instr, want) {
			t.Errorf("past decomposition does not reach the planner: %q missing", want)
		}
	}
	if !strings.Contains(instr, "broker.go holds mu") {
		t.Error("a past task's outcome does not reach the planner")
	}
}

// A failed run is the highest-value planning signal there is — it says this
// decomposition does not work. Excluding failures would leave the planner
// able to repeat only successes it cannot distinguish from the rest.
func TestAFailedRunReachesThePlannerWithItsState(t *testing.T) {
	past := []broker.Snapshot{{
		RunID: "s101-1", Prompt: "same thing", CWD: "/repo", State: broker.RunFailed,
		Tasks: []broker.TaskSnapshot{
			{ID: "task-1", Deps: []string{"task-2"}, State: broker.TaskBlocked},
			{ID: "task-2", Deps: []string{"task-1"}, State: broker.TaskBlocked},
		},
	}}
	instr := BuildPlanInstructionsWithHistory("r1", "u", "tok", "p", "/repo", past)
	if !strings.Contains(instr, "failed") || !strings.Contains(instr, "blocked") {
		t.Error("a failed run reaches the planner without saying it failed")
	}
}

// With nothing to show, the section must not appear at all. An empty
// heading reads to a model as an instruction with no content behind it.
func TestNoPastRunsMeansNoHistorySection(t *testing.T) {
	instr := BuildPlanInstructionsWithHistory("r1", "u", "tok", "p", "/repo", nil)
	if strings.Contains(instr, "## Past runs") {
		t.Error("empty history still emits its heading")
	}
	if instr != BuildPlanInstructions("r1", "u", "tok", "p") {
		t.Error("with no history the instructions should be byte-identical to the plain form")
	}
}

// The prompt is interpolated into a Sprintf format string two lines up. A
// past prompt carrying a percent verb must not be able to corrupt it.
func TestAPastPromptWithPercentVerbsDoesNotCorruptTheFormat(t *testing.T) {
	past := []broker.Snapshot{{
		RunID: "s102-1", Prompt: "why is it %s and not %d", CWD: "/repo",
		State: broker.RunDone,
		Tasks: []broker.TaskSnapshot{{ID: "task-1", State: broker.TaskCompleted}},
	}}
	instr := BuildPlanInstructionsWithHistory("RUNID", "u", "tok", "p", "/repo", past)
	if strings.Contains(instr, "%!") {
		t.Errorf("a past prompt corrupted the format:\n%s", instr)
	}
	// #37 again: only rendering catches argument order.
	for _, line := range strings.Split(instr, "\n") {
		if strings.Contains(line, "/api/v1/run/") && !strings.Contains(line, "/run/RUNID/") {
			t.Errorf("a run URL does not name the run: %s", strings.TrimSpace(line))
		}
	}
}

// Both plan paths must go through the entry point that reads the store.
// Calling BuildPlanInstructions directly compiles and runs fine — it just
// silently gives the planner no history, which is the state this change
// exists to end. Asserted on the source because the alternative is
// launching a real planner subprocess.
func TestBothPlanPathsGoThroughTheHistoryLookup(t *testing.T) {
	for _, f := range []string{"control.go", "../../cmd/narwhal/plan.go"} {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(src), "PlanInstructionsFor(") {
			t.Errorf("%s does not go through the history lookup", f)
		}
	}
}
