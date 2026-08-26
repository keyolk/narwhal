package server

import (
	"strings"
	"testing"
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
	if !strings.Contains(doc, "definition in the request") {
		t.Errorf("nothing tells the planner to write the check from the definition:\n%s", doc)
	}
	// Demanding one from every task is how a field becomes filler.
	if !strings.Contains(doc, "Skip it") {
		t.Errorf("nothing tells the planner a check is optional:\n%s", doc)
	}
}
