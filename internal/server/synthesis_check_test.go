package server

import (
	"strings"
	"testing"
)

// Run s1787538246213-1 asked how many exported functions in
// internal/store lack a doc comment. The answer is 0 — every name the two
// workers reported (writeTask, registryPath, loadRegistry, writeRegistry,
// writeRunDigest, tokens) starts with a lowercase letter and is therefore
// not exported. The run reported 8 and finished 3/3 completed.
//
// The harness is not at fault: dispatch, the completion gate, and the
// snapshot all did their jobs, and the run carries the build stamp from
// #40 that says so. The instructions are. Synthesis is told it "does NOT
// investigate the codebase itself", so when two peers with disjoint scopes
// make the same category error there is nothing left for it to do but add
// them up. Nothing in the run is positioned to notice.
//
// The prohibition is worth keeping — a synthesis that redoes the
// investigation runs out of turn, which is why it was written. What it
// must be allowed to do is check the one number it is about to publish.

func TestSynthesisMayCheckTheNumberItPublishes(t *testing.T) {
	instr := BuildPlanInstructions("r1", "http://127.0.0.1:1", "tok", "count something")

	i := strings.Index(instr, "does NOT investigate")
	if i < 0 {
		t.Fatal("the synthesis prohibition is gone entirely; it should be narrowed, not dropped")
	}
	// The carve-out has to sit with the prohibition. Prose elsewhere in
	// the fragment does not reach a model that has already read the rule:
	// 154 of 154 tasks ran without a model set while the prose two
	// paragraphs from the example said to set one (#37).
	para := instr[i:]
	if j := strings.Index(para, "\n\n"); j > 0 {
		para = para[:j]
	}
	// The carve-out moved into the task's "check" field, so the
	// prohibition must point at it rather than restate it — but it must
	// still point, or the rule reads as absolute again.
	if !strings.Contains(para, "check") {
		t.Errorf("the prohibition has no carve-out for checking the headline:\n%s", para)
	}
}

// A rule stated in the abstract is a rule a planner writes down and a
// worker skips. #37 established that the thing that gets followed is the
// concrete example, so the definition of the number has to be in the
// synthesis assignment example itself.
func TestTheSynthesisExampleTellsItToConfirmTheHeadline(t *testing.T) {
	instr := BuildPlanInstructions("r1", "http://127.0.0.1:1", "tok", "count something")

	i := strings.Index(instr, `"id":"synthesis"`)
	if i < 0 {
		t.Fatal("no synthesis example in the instructions")
	}
	example := instr[i:]
	if j := strings.Index(example, "\n\n"); j > 0 {
		example = example[:j]
	}
	// Case-insensitive: the instruction now leads the "check" field, so
	// it is capitalised. What matters is that the example the planner
	// copies carries it, not which letter it starts with.
	lower := strings.ToLower(example)
	if !strings.Contains(lower, "confirm") && !strings.Contains(lower, "verify") {
		t.Errorf("the synthesis example the planner copies never mentions confirming the number:\n%s", example)
	}
	// And it must be in the check field, where the gate will ask for it,
	// rather than only in the assignment prose the worker may skim past.
	if !strings.Contains(example, `"check"`) {
		t.Errorf("the synthesis example sets no check, so the gate has nothing to ask for:\n%s", example)
	}
}
