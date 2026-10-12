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
