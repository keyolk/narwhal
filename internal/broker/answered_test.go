package broker

import (
	"context"
	"strings"
	"testing"
)

// answerJudge answers every question with p, or undecided.
type answerJudge struct {
	p       float64
	decided bool
	asked   []string
}

func (j *answerJudge) Ask(_ context.Context, state, name, _ string) (float64, bool) {
	j.asked = append(j.asked, name+"|"+state)
	return j.p, j.decided
}

func answeredRun(t *testing.T, j Judge) (*Run, *Task) {
	t.Helper()
	b := New()
	b.SetJudge(j)
	r := b.CreateRun("r", "p", t.TempDir(), "main")
	task := r.AddTask("a", "a", "find where the lock token is logged", nil)
	task.StartDispatch("d1", "worker-a")
	return r, task
}

// A confident no hands the call back once; the next call completes.
func TestANonAnswerIsHandedBackOnce(t *testing.T) {
	j := &answerJudge{p: 0.03, decided: true}
	r, task := answeredRun(t, j)
	if hint := r.JudgeAnswered(task, "started on this, details to follow"); !strings.Contains(hint, "does not read as an answer") {
		t.Fatalf("a non-answer was not handed back: %q", hint)
	}
	if hint := r.JudgeAnswered(task, "started on this, details to follow"); hint != "" {
		t.Errorf("the second call was handed back too: %q", hint)
	}
	if len(j.asked) != 1 {
		t.Errorf("the judge was asked %d times, want once per dispatch", len(j.asked))
	}
	vs := r.Verdicts()
	if len(vs) != 1 || vs[0].Question != QuestionAnswered || vs[0].Action != ActionReturned {
		t.Errorf("verdicts = %+v", vs)
	}
	// A new dispatch is asked again.
	task.StartDispatch("d2", "worker-a")
	if hint := r.JudgeAnswered(task, "still nothing"); hint == "" {
		t.Error("a new dispatch was not judged")
	}
}

// Above the threshold, undecided, or no judge: complete as before.
func TestAnAnswerOrNoDecisionCompletes(t *testing.T) {
	for name, j := range map[string]Judge{
		"answers":   &answerJudge{p: 0.15, decided: true},
		"undecided": &answerJudge{p: 0, decided: false},
		"no judge":  nil,
	} {
		r, task := answeredRun(t, j)
		if hint := r.JudgeAnswered(task, "the token is logged in middleware.go:40"); hint != "" {
			t.Errorf("%s: handed back: %q", name, hint)
		}
	}
}

// A reopened task is judged against its follow-up too: the outcome answers
// the instruction, which the assignment alone does not mention.
func TestAReopenedTaskIsJudgedWithItsFollowUp(t *testing.T) {
	j := &answerJudge{p: 0.9, decided: true}
	r, task := answeredRun(t, j)
	task.CompleteDispatch("found it", r)
	task.Reopen("also check the request logger")
	task.StartDispatch("d2", "worker-a")
	r.JudgeAnswered(task, "the request logger redacts it")
	if len(j.asked) != 1 || !strings.Contains(j.asked[0], "FOLLOW-UP INSTRUCTION:\nalso check the request logger") {
		t.Errorf("the judge did not see the follow-up: %q", j.asked)
	}
}

// A dependent the judge is confident the change cannot reach stays
// finished, and so does what builds only on it; the answer task reopens
// regardless.
func TestADependentTheChangeCannotReachIsLeftFinished(t *testing.T) {
	b := New()
	b.SetJudge(JudgeFunc(func(_ context.Context, state, name, _ string) (float64, bool) {
		if name != QuestionAffected {
			return 0, false
		}
		if strings.Contains(state, "TASK:\ndo docs") {
			return 0.1, true
		}
		return 0.8, true
	}))
	r := b.CreateRun("r", "p", t.TempDir(), "main")
	a := done(t, r, "a", "a")
	done(t, r, "docs", "docs", "a")
	done(t, r, "docs-index", "docs-index", "docs")
	done(t, r, "impl", "impl", "a")
	done(t, r, "synthesis", "synthesis", "docs", "impl")

	got := r.ReopenWithDependents(a, "add an Expiry field")
	if strings.Join(got, ",") != "a,impl,synthesis" {
		t.Fatalf("reopened %v, want a, impl and the synthesis", got)
	}
	if r.GetTask("docs").CurrentState() != TaskCompleted || r.GetTask("docs-index").CurrentState() != TaskCompleted {
		t.Error("a dependent the change cannot reach was reopened")
	}
	var skipped int
	for _, v := range r.Verdicts() {
		if v.Question == QuestionAffected && v.Action == ActionSkipped {
			skipped++
		}
	}
	if skipped != 1 {
		t.Errorf("%d skips recorded, want 1 (docs)", skipped)
	}
}

// A synthesis whose answer leaves a peer out is handed back once, naming it.
func TestASynthesisThatLeavesAPeerOutIsHandedBack(t *testing.T) {
	b := New()
	b.SetJudge(JudgeFunc(func(_ context.Context, state, name, _ string) (float64, bool) {
		if name == QuestionFolded && strings.Contains(state, "PEER b:") {
			return 0.05, true
		}
		return 0.9, true
	}))
	r := b.CreateRun("r", "p", t.TempDir(), "main")
	done(t, r, "a", "a")
	done(t, r, "b", "b")
	syn := r.AddTask("synthesis", "synthesis", "combine", []string{"a", "b"})
	syn.StartDispatch("d1", "worker-synthesis")

	hint := r.JudgeFolded(syn, "a done; that is all")
	if !strings.Contains(hint, "what b concluded") || strings.Contains(hint, "a,") {
		t.Fatalf("hint = %q, want it to name b alone", hint)
	}
	if hint := r.JudgeFolded(syn, "a done; that is all"); hint != "" {
		t.Errorf("handed back twice: %q", hint)
	}
	// Not a synthesis: never asked.
	other := r.AddTask("c", "c", "x", []string{"a"})
	other.StartDispatch("d1", "worker-c")
	if hint := r.JudgeFolded(other, "x"); hint != "" {
		t.Errorf("a non-synthesis was judged: %q", hint)
	}
}
