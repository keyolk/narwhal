package broker

import (
	"strings"
	"testing"
)

func reviewRun(t *testing.T) (*Run, *Task) {
	t.Helper()
	r := New().CreateRun("r", "p", t.TempDir(), "main")
	r.CreateStandardThreads()
	for _, id := range []string{"a", "b"} {
		task := r.AddTask(id, id, "research "+id, nil)
		task.StartDispatch("d1", "worker-"+id)
	}
	syn := r.AddTask("synthesis", "synthesis", "combine", []string{"a", "b"})
	syn.StartDispatch("d1", "worker-synthesis")
	return r, syn
}

// In a run with a reviewer, a worker's assume question waits for review;
// blocking ones and the planner's go straight to the operator.
func TestWorkersQuestionsWaitForReview(t *testing.T) {
	r, _ := reviewRun(t)
	q := r.Ask("a", "Keep v1 format?", "yes", false)
	if !q.Candidate || len(r.OpenQuestions()) != 0 || len(r.Candidates()) != 1 {
		t.Fatalf("assume question: %+v; open %d", q, len(r.OpenQuestions()))
	}
	if b := r.Ask("b", "Which branch?", "main", true); b.Candidate {
		t.Error("a blocking question waited for review")
	}
	if p := r.Ask(PlanAsker, "scope?", "x", false); p.Candidate {
		t.Error("the planner's question waited for review")
	}
	// Without a reviewer, a question goes to the operator.
	r2 := New().CreateRun("r2", "p", t.TempDir(), "main")
	r2.AddTask("a", "a", "x", nil)
	if q := r2.Ask("a", "q", "x", false); q.Candidate {
		t.Error("a run with no reviewer held a question for review")
	}
}

// The reviewer settles what the findings answer, merges duplicates and
// raises the rest; what it leaves is raised when it finishes.
func TestTheReviewerSettlesMergesAndRaises(t *testing.T) {
	r, syn := reviewRun(t)
	q1 := r.Ask("a", "Keep v1 format?", "yes", false)
	q2 := r.Ask("b", "Is v1 still supported?", "yes", false)
	q3 := r.Ask("a", "Store in sqlite or postgres?", "sqlite", false)
	q4 := r.Ask("b", "Name it dayx?", "yes", false)

	if _, err := r.Resolve("synthesis", Resolution{ID: q1.ID, Answer: "no — b found v1 was dropped in 2.0"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Resolve("synthesis", Resolution{ID: q2.ID, MergeInto: q1.ID}); err != nil {
		t.Fatal(err)
	}
	raised, err := r.Resolve("synthesis", Resolution{ID: q3.ID, Raise: true,
		Options: []string{"sqlite", "postgres"}, Recommended: 1, Context: "single user, 5k rows/day"})
	if err != nil || raised.Candidate || raised.Recommended != 1 || raised.RaisedBy != "synthesis" {
		t.Fatalf("raise: %+v %v", raised, err)
	}
	open := r.OpenQuestions()
	if len(open) != 1 || open[0].ID != q3.ID {
		t.Fatalf("open for the operator: %+v", open)
	}
	a1, _ := r.Question(q1.ID)
	if !a1.Differs() || a1.ResolvedBy != "synthesis" {
		t.Errorf("settled: %+v", a1)
	}
	// q4 is left; finishing the synthesis raises it.
	syn.CompleteDispatch("answer", r)
	if open := r.OpenQuestions(); len(open) != 2 || open[1].ID != q4.ID {
		t.Errorf("after the reviewer finished, open = %+v", open)
	}
}

// A checkpoint reviews ahead of the synthesis, and starts early.
func TestACheckpointIsTheReviewerWhileItRuns(t *testing.T) {
	r := New().CreateRun("r", "p", t.TempDir(), "main")
	r.AddTask("a", "a", "research", nil)
	cp := r.AddTask("checkpoint", "checkpoint", "This is the checkpoint task.", []string{"a"})
	r.AddTask("impl", "impl", "build", []string{"checkpoint"})
	r.AddTask("synthesis", "synthesis", "combine", []string{"impl"})
	var ids []string
	for _, d := range r.DispatchableTasks() {
		ids = append(ids, d.ID)
	}
	if strings.Join(ids, ",") != "a,checkpoint,synthesis" {
		t.Errorf("dispatchable %v: the checkpoint should start with the research", ids)
	}
	if rv := r.reviewer(); rv == nil || rv.ID != cp.ID {
		t.Errorf("reviewer = %v, want the checkpoint", rv)
	}
}

// A task that builds on the checkpoint and says so is not a checkpoint: it
// must wait for one, not start with it.
func TestMentioningTheCheckpointIsNotBeingOne(t *testing.T) {
	r := New().CreateRun("r", "p", t.TempDir(), "main")
	r.AddTask("a", "a", "research", nil)
	r.AddTask("checkpoint", "checkpoint", "This is the checkpoint task.", []string{"a"})
	r.AddTask("write", "write", "Read the checkpoint task's outcome and write it.", []string{"checkpoint"})
	for _, d := range r.DispatchableTasks() {
		if d.ID == "write" {
			t.Fatal("a task that only mentions the checkpoint started before it")
		}
	}
}
