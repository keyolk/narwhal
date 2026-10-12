package broker

import (
	"strings"
	"testing"
)

func finished(t *testing.T, failed bool) (*Run, *Task) {
	t.Helper()
	r := New().CreateRun("r", "p", t.TempDir(), "main")
	task := r.AddTask("a", "a", "do a", nil)
	for i := 0; i < MaxDispatchFailures; i++ {
		task.StartDispatch("d", "worker-a")
		if !failed {
			task.CompleteDispatch("the answer", r)
			return r, task
		}
		task.FailDispatch("died", r)
	}
	if task.CurrentState() != TaskFailed {
		t.Fatalf("setup: state %s", task.CurrentState())
	}
	return r, task
}

func TestReopenPutsAFinishedTaskBackWithItsInstruction(t *testing.T) {
	r, task := finished(t, false)
	task.RecordCheckResult("checked")
	if !task.Reopen("also cover the legacy payload") {
		t.Fatal("a completed task was not reopened")
	}
	if task.CurrentState() != TaskReady || len(r.DispatchableTasks()) != 1 {
		t.Fatalf("reopened task is %s and not dispatchable", task.CurrentState())
	}
	if task.CurrentFollowUp() != "also cover the legacy payload" || task.PreviousOutcome() != "the answer" {
		t.Errorf("follow-up %q, previous %q", task.CurrentFollowUp(), task.PreviousOutcome())
	}
	if task.snapshot().CheckResult != "" {
		t.Error("the check result for the old work survived the reopen")
	}
	// A second instruction before the worker starts joins the first.
	task.Reopen("and log it")
	if got := task.CurrentFollowUp(); got != "also cover the legacy payload\n\nand log it" {
		t.Errorf("follow-ups = %q", got)
	}
	task.StartDispatch("d2", "worker-a")
	task.CompleteDispatch("the new answer", r)
	if task.CurrentFollowUp() != "" {
		t.Error("the follow-up outlived the task completing again")
	}
}

func TestReopenLeavesUnfinishedTasksAlone(t *testing.T) {
	r := New().CreateRun("r", "p", t.TempDir(), "main")
	running := r.AddTask("a", "a", "x", nil)
	running.StartDispatch("d", "worker-a")
	ready := r.AddTask("b", "b", "x", nil)
	if running.Reopen("x") || ready.Reopen("x") {
		t.Error("an unfinished task was reopened")
	}
	if running.CurrentFollowUp() != "" || ready.CurrentFollowUp() != "" {
		t.Error("an unfinished task took a follow-up")
	}
}

func TestAReopenedFailedTaskGetsItsOwnRetries(t *testing.T) {
	r, task := finished(t, true)
	task.Reopen("try with the other endpoint")
	task.StartDispatch("d", "worker-a")
	task.FailDispatch("died again", r)
	if task.CurrentState() != TaskReady {
		t.Fatalf("one failure after a reopen failed the task: the old failures were counted")
	}
}

func TestAFollowUpSurvivesARestart(t *testing.T) {
	r, task := finished(t, false)
	task.Reopen("also cover the legacy payload")
	restored := RestoreRun(r.Snapshot())
	got := restored.GetTask("a")
	if got.CurrentState() != TaskReady || got.CurrentFollowUp() != "also cover the legacy payload" {
		t.Errorf("restored: %s, %q", got.CurrentState(), got.CurrentFollowUp())
	}
}

// done adds a task with deps and completes it.
func done(t *testing.T, r *Run, id, name string, deps ...string) *Task {
	t.Helper()
	task := r.AddTask(id, name, "do "+id, deps)
	task.StartDispatch("d1", "worker-"+id)
	task.CompleteDispatch(id+" done", r)
	return task
}

// a ← b ← synthesis, and c beside a: reopening a reopens b and the
// synthesis behind it, pending until a is done; c is left alone.
func TestReopeningATaskReopensWhatBuildsOnIt(t *testing.T) {
	r := New().CreateRun("r", "p", t.TempDir(), "main")
	a := done(t, r, "a", "a")
	done(t, r, "c", "c")
	b := done(t, r, "b", "b", "a")
	syn := done(t, r, "synthesis", "synthesis", "b", "c")

	got := r.ReopenWithDependents(a, "cover the legacy payload too")
	if strings.Join(got, ",") != "a,b,synthesis" {
		t.Fatalf("reopened %v, want a, b, synthesis", got)
	}
	if a.CurrentState() != TaskReady || b.CurrentState() != TaskPending || syn.CurrentState() != TaskPending {
		t.Fatalf("states a=%s b=%s synthesis=%s", a.CurrentState(), b.CurrentState(), syn.CurrentState())
	}
	if r.GetTask("c").CurrentState() != TaskCompleted {
		t.Error("c, which a does not feed, was reopened")
	}
	if fu := b.CurrentFollowUp(); !strings.Contains(fu, "Task a") || !strings.Contains(fu, "legacy payload") {
		t.Errorf("b's follow-up does not say why: %q", fu)
	}
	// The synthesis starts listening at once; b waits for a.
	var dispatchable []string
	for _, d := range r.DispatchableTasks() {
		dispatchable = append(dispatchable, d.ID)
	}
	if strings.Join(dispatchable, ",") != "a,synthesis" {
		t.Errorf("dispatchable %v, want a and the synthesis", dispatchable)
	}
	a.StartDispatch("d2", "worker-a")
	a.CompleteDispatch("a redone", r)
	if b.CurrentState() != TaskReady {
		t.Errorf("b did not become ready when a finished: %s", b.CurrentState())
	}
	if p := r.PendingDeps("synthesis"); len(p) != 1 || p[0] != "b" {
		t.Errorf("the synthesis waits on %v, want b", p)
	}
}

// A failed task downstream has its own cause; a cascade leaves it.
func TestACascadeLeavesFailedTasksAlone(t *testing.T) {
	r := New().CreateRun("r", "p", t.TempDir(), "main")
	a := done(t, r, "a", "a")
	b := r.AddTask("b", "b", "x", []string{"a"})
	for i := 0; i < MaxDispatchFailures; i++ {
		b.StartDispatch("d", "worker-b")
		b.FailDispatch("died", r)
	}
	if got := r.ReopenWithDependents(a, "again"); strings.Join(got, ",") != "a" {
		t.Errorf("reopened %v, want only a", got)
	}
}

func splitMsg(id, name string) string {
	return SplitRequestPrefix + "|" + id + "|" + name + "|investigate " + name + "|"
}

// Work split off after the answer was written becomes a dependency of the
// answer task, which reopens to fold it in.
func TestASplitAfterTheAnswerIsFoldedIntoIt(t *testing.T) {
	r := New().CreateRun("r", "p", t.TempDir(), "main")
	r.CreateStandardThreads()
	a := done(t, r, "a", "a")
	syn := done(t, r, "synthesis", "synthesis", "a")
	r.ReopenWithDependents(a, "also check the legacy payload")
	a.StartDispatch("d2", "worker-a")
	cur := r.IntakeCursor()

	r.PostMessage(PlanningThread, "worker-a", nil, PriorityNormal, splitMsg("legacy", "legacy-payload"))
	r.IntakeSplitRequests(cur)

	if r.GetTask("legacy") == nil {
		t.Fatal("the split task was not added")
	}
	if p := r.PendingDeps("synthesis"); !strings.Contains(strings.Join(p, ","), "legacy") {
		t.Errorf("the synthesis does not wait on the split task: %v", p)
	}
	if syn.CurrentState() != TaskPending {
		t.Errorf("synthesis is %s", syn.CurrentState())
	}
}

func TestASplitAfterACompletedAnswerReopensIt(t *testing.T) {
	r := New().CreateRun("r", "p", t.TempDir(), "main")
	r.CreateStandardThreads()
	done(t, r, "a", "a")
	syn := done(t, r, "synthesis", "synthesis", "a")
	cur := r.IntakeCursor()
	r.PostMessage(PlanningThread, "worker-a", nil, PriorityNormal, splitMsg("extra", "extra"))
	r.IntakeSplitRequests(cur)
	if syn.CurrentState() != TaskPending || !strings.Contains(syn.CurrentFollowUp(), "extra") {
		t.Errorf("the answer task was not reopened for the new work: %s %q", syn.CurrentState(), syn.CurrentFollowUp())
	}
}

// On a run's first pass the planner's graph is left as it is: a split does
// not quietly become a dependency of the synthesis.
func TestASplitOnTheFirstPassIsLeftAlone(t *testing.T) {
	r := New().CreateRun("r", "p", t.TempDir(), "main")
	r.CreateStandardThreads()
	r.AddTask("a", "a", "x", nil)
	r.AddTask("synthesis", "synthesis", "x", []string{"a"})
	cur := r.IntakeCursor()
	r.PostMessage(PlanningThread, "worker-a", nil, PriorityNormal, splitMsg("extra", "extra"))
	r.IntakeSplitRequests(cur)
	if p := r.PendingDeps("synthesis"); strings.Contains(strings.Join(p, ","), "extra") {
		t.Errorf("a first-pass split was made a dependency of the synthesis: %v", p)
	}
}
