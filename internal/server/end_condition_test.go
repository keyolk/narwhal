package server

import (
	"strings"
	"testing"

	"github.com/keyolk/narwhal/internal/broker"
)

// #37 established what a planner actually follows: the concrete example.
// 154 of 154 tasks ran with no model set while the prose two paragraphs
// away said to set one. A "check" documented only in the field list would
// go the same way.
func TestTheTaskExampleAPlannerCopiesCarriesACheck(t *testing.T) {
	instr := BuildPlanInstructions("r1", "http://127.0.0.1:1", "tok", "audit something")

	i := strings.Index(instr, `"id":"task-1"`)
	if i < 0 {
		t.Fatal("no investigation-task example in the instructions")
	}
	example := instr[i:]
	if j := strings.Index(example, "\n\n"); j > 0 {
		example = example[:j]
	}
	if !strings.Contains(example, `"check"`) {
		t.Errorf("the example the planner copies sets no check:\n%s", example)
	}
}

// A check written from the expected answer confirms itself. The one that
// would have caught run s1787538246213-1 tested the definition — "are
// these names actually exported" — not the number.
//
// What the field says now lives in broker.CheckContract, so the planner and
// the narwhal_spawn schema cannot describe it differently; the properties
// this test used to spell out are asserted there, against the constant. What
// is left here is that the planner still gets it at all, in the field list
// where it is being used.
func TestTheCheckFieldIsExplainedAgainstTheDefinitionNotTheAnswer(t *testing.T) {
	instr := BuildPlanInstructions("r1", "http://127.0.0.1:1", "tok", "count something")

	i := strings.Index(instr, "- check:")
	if i < 0 {
		t.Fatal("the check field is not documented in the field list")
	}
	doc := instr[i:]
	if j := strings.Index(doc, "\n\n3."); j > 0 {
		doc = doc[:j]
	}
	if !strings.Contains(doc, broker.CheckContract) {
		t.Errorf("the check field no longer carries the shared contract:\n%s", doc)
	}
	// The worked example is the planner's alone — it is the run that went
	// wrong here, and it is too long for a tool schema.
	if !strings.Contains(doc, "actually exported") {
		t.Errorf("the planner lost the example of a check that caught a wrong answer:\n%s", doc)
	}
}
