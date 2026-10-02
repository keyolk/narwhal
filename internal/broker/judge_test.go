package broker

import (
	"context"
	"strings"
	"testing"
)

// fixedJudge answers every question with p, or undecided when ok is false,
// and remembers what it was asked.
type fixedJudge struct {
	p     float64
	ok    bool
	asked []string
	state []string
}

func (j *fixedJudge) Ask(_ context.Context, state, name, _ string) (float64, bool) {
	j.asked = append(j.asked, name)
	j.state = append(j.state, state)
	return j.p, j.ok
}

func exitedRun(t *testing.T, j Judge, posts ...string) (*Run, *Task) {
	t.Helper()
	b := New()
	b.SetJudge(j)
	r := b.CreateRun("r1", "p", "/tmp", "main")
	task := r.AddTask("t1", "t1", "find the leak", nil)
	task.StartDispatch("d1", "worker-t1")
	for _, p := range posts {
		r.PostMessage(WorklogThread, "worker-t1", nil, PriorityNormal, p)
	}
	return r, task
}

// A FILE_CLAIM is not findings. The old check completed this task, and its
// dependents ran on output that did not exist.
func TestOnlyProtocolMessagesIsRetriedWithoutAskingTheJudge(t *testing.T) {
	j := &fixedJudge{p: 0, ok: true}
	r, task := exitedRun(t, j, "FILE_CLAIM|t1|a.go")

	if got := r.ResolveExitWithoutDone(task, "worker-t1"); got != ActionRetry {
		t.Fatalf("action = %s, want retry", got)
	}
	if task.CurrentState() != TaskReady {
		t.Errorf("state = %s, want ready for a retry", task.CurrentState())
	}
	if len(j.asked) != 0 {
		t.Errorf("judge was asked %v; a protocol-only radio is decidable for free", j.asked)
	}
}

func TestJudgeSureOfNoResultRetries(t *testing.T) {
	j := &fixedJudge{p: 0.9, ok: true}
	r, task := exitedRun(t, j, "plan: I will read main.go first")

	if got := r.ResolveExitWithoutDone(task, "worker-t1"); got != ActionRetry {
		t.Fatalf("action = %s, want retry", got)
	}
	vs := r.Verdicts()
	if len(vs) != 1 || vs[0].Action != ActionRetry || !vs[0].Sharp() {
		t.Fatalf("verdicts = %+v, want one sharp retry", vs)
	}
	if !strings.Contains(j.state[0], "find the leak") {
		t.Errorf("judge was not shown the assignment: %q", j.state[0])
	}
}

// Below the threshold the dispatcher keeps what it did before the judge
// existed. A finished task must never be retried on an uncertain verdict.
func TestUncertainVerdictKeepsTheResult(t *testing.T) {
	j := &fixedJudge{p: NoResultThreshold - 0.01, ok: true}
	r, task := exitedRun(t, j, "found it: the cache never evicts")

	if got := r.ResolveExitWithoutDone(task, "worker-t1"); got != ActionComplete {
		t.Fatalf("action = %s, want complete", got)
	}
	if task.CurrentState() != TaskCompleted {
		t.Errorf("state = %s, want completed", task.CurrentState())
	}
}

// An outage must not change what narwhal does — only what it records.
func TestUndecidedJudgeFallsBackToComplete(t *testing.T) {
	j := &fixedJudge{ok: false}
	r, task := exitedRun(t, j, "found it")

	if got := r.ResolveExitWithoutDone(task, "worker-t1"); got != ActionComplete {
		t.Fatalf("action = %s, want complete", got)
	}
	vs := r.Verdicts()
	if len(vs) != 1 || vs[0].Decided || vs[0].Action != ActionFallback {
		t.Fatalf("verdicts = %+v, want one undecided fallback", vs)
	}
}

func TestNoJudgeRecordsNothing(t *testing.T) {
	r, task := exitedRun(t, nil, "found it")
	if got := r.ResolveExitWithoutDone(task, "worker-t1"); got != ActionComplete {
		t.Fatalf("action = %s, want complete", got)
	}
	if vs := r.Verdicts(); len(vs) != 0 {
		t.Errorf("verdicts = %+v, want none without a judge", vs)
	}
}

func failTwice(r *Run, task *Task, reason string) {
	for i := 0; i < MaxDispatchFailures-1; i++ {
		task.StartDispatch("d", "worker-t1")
		r.FailAndMaybeEscalate(task, reason)
	}
}

func TestSameCauseTwiceEscalatesBeforeTheLastAttempt(t *testing.T) {
	j := &fixedJudge{p: 0.9, ok: true}
	r, task := exitedRun(t, j)
	task.SetModel("haiku")
	task.Dispatches = nil

	failTwice(r, task, "go test ./... failed: undefined: Foo")

	if got := task.CurrentModel(); got != "sonnet" {
		t.Errorf("model = %q, want sonnet before the last attempt", got)
	}
	if len(j.asked) != 1 || j.asked[0] != QuestionSameCause {
		t.Errorf("judge asked %v, want once, after the second failure", j.asked)
	}
	if task.CurrentState() != TaskReady {
		t.Errorf("state = %s; escalating must not spend the last attempt", task.CurrentState())
	}
	var announced bool
	for _, m := range r.MessagesSince(0) {
		if m.Sender == "coordinator" && strings.Contains(m.Content, "escalating t1 to sonnet") {
			announced = true
		}
	}
	if !announced {
		t.Error("escalation was not announced on the radio")
	}
}

func TestDifferentCausesKeepTheModel(t *testing.T) {
	j := &fixedJudge{p: 0.2, ok: true}
	r, task := exitedRun(t, j)
	task.SetModel("haiku")
	task.Dispatches = nil

	failTwice(r, task, "timeout")

	if got := task.CurrentModel(); got != "haiku" {
		t.Errorf("model = %q, want haiku kept", got)
	}
	if vs := r.Verdicts(); len(vs) != 1 || vs[0].Action != ActionRetry {
		t.Errorf("verdicts = %+v, want one retry", vs)
	}
}

func TestStrongestTierIsNotJudged(t *testing.T) {
	j := &fixedJudge{p: 0.9, ok: true}
	r, task := exitedRun(t, j)
	task.SetModel("opus")
	task.Dispatches = nil

	failTwice(r, task, "same")

	if len(j.asked) != 0 {
		t.Errorf("judge asked %v; there is nothing to escalate to", j.asked)
	}
}

// Verdicts explain a task's fate, so they must survive a daemon restart.
func TestVerdictsSurviveRestore(t *testing.T) {
	j := &fixedJudge{p: 0.9, ok: true}
	r, task := exitedRun(t, j, "plan only")
	r.ResolveExitWithoutDone(task, "worker-t1")

	restored := RestoreRun(r.Snapshot())
	if vs := restored.Verdicts(); len(vs) != 1 || vs[0].TaskID != "t1" {
		t.Fatalf("restored verdicts = %+v", vs)
	}
}

func TestAdoptedRunGetsTheBrokersJudge(t *testing.T) {
	b := New()
	j := &fixedJudge{}
	b.SetJudge(j)
	r := RestoreRun(Snapshot{RunID: "r2"})
	b.AdoptRun(r)
	if r.currentJudge() != Judge(j) {
		t.Error("an adopted run did not inherit the broker's judge")
	}
}

func TestClipCutsOnARuneBoundary(t *testing.T) {
	got := clip("가나다", 4) // 가 is 3 bytes; byte 4 is mid-rune
	if got != "가…" {
		t.Errorf("clip = %q, want 가…", got)
	}
}

// The judge compares causes, and a probability baked into a reason makes
// two identical causes read as different ones.
func TestSameCauseStateDropsVerdictNumbers(t *testing.T) {
	j := &fixedJudge{p: 0.9, ok: true}
	r, task := exitedRun(t, j)
	task.SetModel("haiku")
	task.Dispatches = nil

	failTwice(r, task, "worker exited without task-done; radio held no result (p=0.71)")

	if len(j.state) != 1 {
		t.Fatalf("judge asked %d times", len(j.state))
	}
	if strings.Contains(j.state[0], "p=0.71") {
		t.Errorf("judge state still carries the number: %q", j.state[0])
	}
}

// An unnamed tier is the launcher's default, which may already be opus.
// Escalating it to sonnet would be a downgrade.
func TestDefaultTierIsNotEscalated(t *testing.T) {
	j := &fixedJudge{p: 0.9, ok: true}
	r, task := exitedRun(t, j)
	task.Dispatches = nil

	failTwice(r, task, "same")

	if got := task.CurrentModel(); got != "" {
		t.Errorf("model = %q; a task on the run default must keep it", got)
	}
	if len(j.asked) != 0 {
		t.Errorf("judge asked %v about a task it cannot escalate", j.asked)
	}
}
