package broker

import (
	"strings"
	"testing"
)

// The contract strings exist because the knowledge lived on one path only.
// A test that just asserts they are non-empty would not have caught that —
// what these check is that each one still says the thing whose absence
// caused the drift.

func TestDepsContractStatesBothHalvesOfTheRule(t *testing.T) {
	// "must complete first" — the old wording — reads as serialization, so
	// a caller who wants parallel workers omits deps and nothing in the run
	// waits for anything. But the completion-gate exception is only for the
	// synthesis task (see DispatchableTasks); saying deps never gate
	// dispatch would be the same error pointed the other way.
	c := strings.ToLower(DepsContract)
	if !strings.Contains(c, "completion") {
		t.Error("the contract does not mention completion gating at all")
	}
	if !strings.Contains(c, "synthesis") {
		t.Error("the contract does not say which task the exception applies to")
	}
	if !strings.Contains(c, "task-done") {
		t.Error("the contract does not name what is actually blocked")
	}
	// The other half: every non-synthesis task really does wait.
	if !strings.Contains(c, "every other task") {
		t.Error("the contract does not say that other tasks do gate dispatch")
	}
}

func TestSynthesisContractNamesTheRecognitionRule(t *testing.T) {
	// isSynthesisName matches on the name, so a caller that describes the
	// job perfectly under another name gets a task that queues behind its
	// deps and hears nothing. That constraint has to be stated, not
	// assumed — the last six runs used names like "fix-model-landmine".
	if !strings.Contains(SynthesisContract, `"synthesis"`) {
		t.Error("the contract does not tell the caller what to name the task")
	}
	if !isSynthesisName("synthesis", "") {
		t.Error("the name the contract asks for is not the name the dispatcher recognizes")
	}
	// The contract's own example must survive the recognizer. If someone
	// relaxes or renames the rule, this is where the docs and the code
	// stop agreeing.
	if !isSynthesisName("synthesis", "integrate peer findings from the radio") {
		t.Error("a task built to the contract is not recognized as synthesis")
	}
}

func TestCheckContractAsksForSomethingThatCanFail(t *testing.T) {
	// A check chosen to agree with an answer already written is not a
	// check. The word that carries this is "wrong".
	if !strings.Contains(strings.ToUpper(CheckContract), "WRONG") {
		t.Error("the contract does not say the check must be able to come out wrong")
	}
	// And it must say when to skip one, or every task gets filler.
	if !strings.Contains(CheckContract, "Omit it") {
		t.Error("the contract does not say a task may have no meaningful check")
	}
}
