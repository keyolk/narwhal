package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keyolk/narwhal/internal/broker"
	"github.com/keyolk/narwhal/internal/config"
	"github.com/keyolk/narwhal/internal/store"
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

// plan.history=false keeps the digest out. A benchmark repeats one question
// in one directory, and with the digest on, a repeat's planner reads how the
// previous trial's tasks ended before it plans.
func TestPlanHistoryCanBeSwitchedOff(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := filepath.Join(t.TempDir(), "config.json")
	config.UseFile(cfg)
	t.Cleanup(func() { config.UseFile("") })

	prompt := "launch kitty headless and report the startup log"
	if err := store.SaveRun(broker.Snapshot{
		RunID: "s200-1", Prompt: prompt, CWD: "/repo", State: broker.RunDone,
		// The outcome is padded past the store's floor for a run worth
		// showing (store.substantial, 1000 bytes of export).
		Tasks: []broker.TaskSnapshot{{ID: "task-1", Name: "launch", State: broker.TaskCompleted,
			Outcome: "LAUNCHED IN DOCKER: kitty started under Xvfb inside the task image.\n" +
				strings.Repeat("startup log line with a subsystem name and its timestamp\n", 30)}},
	}); err != nil {
		t.Fatal(err)
	}

	if on := PlanInstructionsFor("r1", "u", "tok", prompt, "/repo"); !strings.Contains(on, "LAUNCHED IN DOCKER") {
		t.Fatal("the seeded run is not in the digest; the test does not exercise the switch")
	}
	if err := config.Set("plan.history", "false"); err != nil {
		t.Fatal(err)
	}
	if off := PlanInstructionsFor("r1", "u", "tok", prompt, "/repo"); strings.Contains(off, "Past runs") {
		t.Error("plan.history=false still shows the planner past runs")
	}
}

// The planner is told not to write escape clauses. On measure-2 one
// ("if impossible on this host, report the errors") let a worker stop after
// five turns, and the run lost the six rubrics that needed what it skipped.
func TestThePlannerIsToldNotToWriteEscapeClauses(t *testing.T) {
	instr := BuildPlanInstructions("r1", "u", "tok", "run it and report the log")
	for _, want := range []string{"Never write an escape clause", "shown the full request"} {
		if !strings.Contains(instr, want) {
			t.Errorf("planner instructions are missing %q", want)
		}
	}
}

// Pilot-1's missed rubrics mostly asked for evidence nobody collected. A
// location-only criterion let those workers complete after naming a function,
// and synthesis outcomes merely said that an answer file was written.
func TestThePlannerRequiresClaimEvidenceAndASynthesisLedger(t *testing.T) {
	instr := BuildPlanInstructions("r1", "u", "tok", "run it and report what appears")
	for _, want := range []string{
		"CLAIM-AND-EVIDENCE",
		"captured observation",
		"tracing source code alone does not satisfy it",
		"compact evidence ledger",
		"UNOBSERVED",
		"includes that ledger in its task-done outcome",
	} {
		if !strings.Contains(instr, want) {
			t.Errorf("planner instructions are missing %q", want)
		}
	}
}

// On composite-1 (four repositories in one request) the planner gave each
// part one worker, under a five-task cap, and the hardest part lost 8-14 of
// its 17 rubrics. The cap now scales with parts, and synthesis is told not
// to shorten a part because the request had several.
func TestThePlannerScalesTasksWithParts(t *testing.T) {
	instr := BuildPlanInstructions("r1", "u", "tok", "answer parts 1-4")
	for _, want := range []string{
		"tasks PER PART",
		"one task per question",
		"4 per part (16 in all)",
		"not a reason to shorten each part",
	} {
		if !strings.Contains(instr, want) {
			t.Errorf("planner instructions are missing %q", want)
		}
	}
}
