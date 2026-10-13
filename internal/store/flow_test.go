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

// A removed step is out of the flow — its steps, its brief and its output —
// while the run's record stays; removing the last step removes the flow.
func TestRemovingAStepTakesItOutOfTheFlow(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, id := range []string{"r1", "r2", "r3"} {
		run := broker.New().CreateRun(id, "ask "+id, "/src/repo", "main")
		tk := run.AddTask("synthesis", "synthesis", "x", nil)
		tk.StartDispatch("d", "w")
		tk.CompleteDispatch("concluded in "+id, run)
		if err := SaveRun(run.Snapshot()); err != nil {
			t.Fatal(err)
		}
	}
	StartFlow("r1", "/src/repo", "ask r1")
	AddFlowStep("r1", "r2", "ask r2")
	AddFlowStep("r1", "r3", "ask r3")

	f, removed, err := RemoveFlowStep("r1", "2")
	if err != nil || removed.RunID != "r2" {
		t.Fatalf("remove by number: %+v %v", removed, err)
	}
	if len(f.Steps) != 2 || f.Steps[1].RunID != "r3" || !f.IsRemoved("r2") {
		t.Fatalf("after remove: %+v", f)
	}
	out := f.Output()
	brief := FlowBrief(f, out.Runs, "")
	if strings.Contains(brief, "concluded in r2") || !strings.Contains(brief, "concluded in r3") {
		t.Errorf("the brief still reads the removed step:\n%s", brief)
	}
	if _, err := LoadRun("r2"); err != nil {
		t.Errorf("the removed run's record is gone: %v", err)
	}
	if _, _, err := RemoveFlowStep("r1", "r9"); err == nil {
		t.Error("removed a step that is not there")
	}

	RemoveFlowStep("r1", "r3")
	if _, _, err := RemoveFlowStep("r1", "r1"); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFlow("r1"); err == nil {
		t.Error("a flow with no steps left is still on disk")
	}
}
