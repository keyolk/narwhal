package broker

import (
	"context"
	"strings"
	"sync"
	"testing"
)

// gaveUpJudge answers each of the three gave-up questions with its own
// probability, keyed by a phrase only that question contains.
type gaveUpJudge struct {
	mu                     sync.Mutex
	negative, untried, env float64
	decided                bool
	states                 []string
}

func (j *gaveUpJudge) Ask(_ context.Context, state, _, instructions string) (float64, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.states = append(j.states, state)
	switch {
	case strings.Contains(instructions, "WITHOUT showing"):
		return j.untried, j.decided
	case strings.Contains(instructions, "limitation of the environment"):
		return j.env, j.decided
	default:
		return j.negative, j.decided
	}
}

func gaveUpRun(t *testing.T, j Judge) (*Run, *Task) {
	t.Helper()
	b := New()
	b.SetJudge(j)
	r := b.CreateRun("r", "p", t.TempDir(), "main")
	task := r.AddTask("a", "a", "launch the app headless and report what it logs", nil)
	task.StartDispatch("d1", "worker-a")
	return r, task
}

// The measure-2 shape: negative, environment blamed, no workaround. Handed
// back once; the next call completes; a new dispatch is asked again.
func TestAGaveUpOutcomeIsHandedBackOnce(t *testing.T) {
	j := &gaveUpJudge{negative: 0.98, untried: 0.32, env: 0.57, decided: true}
	r, task := gaveUpRun(t, j)
	if hint := r.JudgeGaveUp(task, "Headless launch impossible: no Xvfb on this host"); !strings.Contains(hint, "try at least one concrete alternative") {
		t.Fatalf("a gave-up outcome was not handed back: %q", hint)
	}
	if hint := r.JudgeGaveUp(task, "Headless launch impossible: no Xvfb on this host"); hint != "" {
		t.Errorf("the second call was handed back too: %q", hint)
	}
	if len(j.states) != 3 {
		t.Errorf("the judge was asked %d questions, want three, once per dispatch", len(j.states))
	}
	vs := r.Verdicts()
	if len(vs) != 1 || vs[0].Question != QuestionGaveUp || vs[0].Action != ActionReturned {
		t.Fatalf("verdicts = %+v, want one returned gave-up verdict", vs)
	}
	if !strings.Contains(vs[0].Ask, "negative=0.98") {
		t.Errorf("the verdict does not record the three answers: %q", vs[0].Ask)
	}
	task.StartDispatch("d2", "worker-a")
	if hint := r.JudgeGaveUp(task, "still impossible"); hint == "" {
		t.Error("a new dispatch was not judged")
	}
}

// Each half of the rule alone is not enough to hand back.
func TestGaveUpNeedsANegativeAndSomethingUntried(t *testing.T) {
	for name, j := range map[string]*gaveUpJudge{
		// A positive result that mentions an obstacle it worked around.
		"positive": {negative: 0.04, untried: 0.9, env: 0.9, decided: true},
		// A negative that shows alternatives tried: the synthetic legitimate
		// negatives scored untried 0.05–0.14.
		"alternatives shown": {negative: 0.99, untried: 0.1, env: 0.16, decided: true},
		"undecided":          {negative: 0.99, untried: 0.9, env: 0.9, decided: false},
	} {
		r, task := gaveUpRun(t, j)
		if hint := r.JudgeGaveUp(task, "outcome"); hint != "" {
			t.Errorf("%s: handed back: %q", name, hint)
		}
	}
	r, task := gaveUpRun(t, nil)
	if hint := r.JudgeGaveUp(task, "outcome"); hint != "" {
		t.Errorf("no judge: handed back: %q", hint)
	}
}

// The judge reads what the worker itself posted, not its peers: the
// diagnostics behind an impossibility live in the radio report, and a
// peer's success must not vouch for this worker.
func TestGaveUpReadsOnlyTheWorkersOwnReports(t *testing.T) {
	j := &gaveUpJudge{negative: 0.1, decided: true}
	r, task := gaveUpRun(t, j)
	r.PostMessage("results", "worker-a", nil, PriorityNormal, "which Xvfb -> not found")
	r.PostMessage("results", "worker-b", nil, PriorityNormal, "ran it in docker, it works")
	r.JudgeGaveUp(task, "impossible")
	if len(j.states) == 0 {
		t.Fatal("the judge was not asked")
	}
	s := j.states[0]
	if !strings.Contains(s, "which Xvfb -> not found") {
		t.Errorf("the worker's own report is missing from the state: %q", s)
	}
	if strings.Contains(s, "ran it in docker") {
		t.Errorf("a peer's report leaked into the state: %q", s)
	}
}
