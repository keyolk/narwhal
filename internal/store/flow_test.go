package store

import (
	"strings"
	"testing"

	"github.com/keyolk/narwhal/internal/broker"
)

func TestAFlowKeepsItsInputAndStepsInOrder(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	first := broker.New().CreateRun("r1", "plan the lock API", "/src/repo", "main")
	tk := first.AddTask("synthesis", "synthesis", "integrate", nil)
	tk.StartDispatch("d", "w")
	_ = tk.RecordArtifact(broker.ArtifactBranch, "lock-plan")
	tk.CompleteDispatch("Plan: middleware validates LockKey.", first)
	if err := SaveRun(first.Snapshot()); err != nil {
		t.Fatal(err)
	}

	f, err := StartFlow("r1", "/src/repo", "plan the lock API\nmore detail")
	if err != nil {
		t.Fatal(err)
	}
	if f.Title != "plan the lock API" || f.LastRun() != "r1" {
		t.Errorf("start: %+v", f)
	}
	if _, err := SetFlowNotes("r1", "  keep the v1 wire format  "); err != nil {
		t.Fatal(err)
	}
	f, err = AddFlowStep("r1", "r2", "build it")
	if err != nil {
		t.Fatal(err)
	}
	if f.Notes != "keep the v1 wire format" || f.StepOf("r2") != 1 || f.LastRun() != "r2" {
		t.Errorf("after step: %+v", f)
	}

	brief := FlowBrief(f, f.Output().Runs[:1], "/tmp/full.md")
	for _, w := range []string{"plan the lock API", "keep the v1 wire format", "Plan: middleware validates LockKey.", "lock-plan", "/tmp/full.md"} {
		if !strings.Contains(brief, w) {
			t.Errorf("brief lacks %q:\n%s", w, brief)
		}
	}
	if got := ListFlows(); len(got) != 1 || got[0].ID != "r1" {
		t.Errorf("list: %+v", got)
	}

	if _, err := SetFlowClosed("r1", true); err != nil {
		t.Fatal(err)
	}
	if _, err := AddFlowStep("r1", "r3", "more"); err == nil {
		t.Error("a closed flow took a step")
	}
	if _, err := AddFlowStep("nope", "r3", "x"); err == nil {
		t.Error("a step was added to a flow that does not exist")
	}
}
