package main

import (
	"os"
	"strings"
	"testing"

	"github.com/keyolk/narwhal/internal/server"
)

// The header on plan_instructions.go says the fragment is "shared between
// the batch CLI (`narwhal plan`) and the daemon's /control/plan endpoint,
// so both paths produce identical DAGs". It was not shared — the CLI kept
// its own copy behind an identical-looking name, and #37's fix (a "model"
// field in the curl the planner copies, plus a synthesis example) landed
// only on the daemon side. The copy is gone; this asserts it stays gone.
func TestTheCLIHasNoSecondCopyOfThePlannerInstructions(t *testing.T) {
	src, err := os.ReadFile("plan.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(src), "You are the COORDINATOR") {
		t.Error("plan.go carries its own copy of the planner fragment again; " +
			"call server.PlanInstructionsFor instead")
	}
	if !strings.Contains(string(src), "server.PlanInstructionsFor(") {
		t.Error("plan.go no longer goes through the shared entry point")
	}
}

// Both paths must also feed the planner the same history, which means both
// have to pass a cwd. Passing "" would silently disable the lookup on one
// side and the two would decompose differently again.
func TestTheCLIPassesItsCWDToThePlanner(t *testing.T) {
	src, err := os.ReadFile("plan.go")
	if err != nil {
		t.Fatal(err)
	}
	i := strings.Index(string(src), "server.PlanInstructionsFor(")
	call := string(src)[i:]
	if j := strings.Index(call, ")"); j > 0 {
		call = call[:j]
	}
	if !strings.Contains(call, "*cwd") {
		t.Errorf("the CLI does not pass its cwd, so it gets no history: %s", call)
	}
}

var _ = server.PlanHistoryLimit
