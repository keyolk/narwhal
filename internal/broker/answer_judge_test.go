package broker

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keyolk/narwhal/internal/config"
)

// The pairs OverturnedThreshold was measured on, with what the judge
// scored them. Kept so a re-calibration starts from the same cases.
var overturnedCalibration = []struct{ kind, assumed, answer string }{
	{"same", "json", "JSON"},                                  // 0.05
	{"same", "yes, keep v1", "Yes — keep it as it is"},        // 0.04
	{"same", "Korean", "한국어로 써줘"},                             // 0.12
	{"detail", "json", "json, indented with 2 spaces"},        // 0.38
	{"detail", "yes, keep v1", "yes, but mark it deprecated"}, // 0.55
	{"diff", "json", "yaml"},                                  // 0.96
	{"diff", "main", "release/2.3"},                           // 0.94
}

// stateJudge answers by what the state says, as the real judge reads it.
type stateJudge struct {
	p     func(name, state string) float64
	asked []string
}

func (j *stateJudge) Ask(_ context.Context, state, name, _ string) (float64, bool) {
	j.asked = append(j.asked, name)
	return j.p(name, state), true
}

func judgedRun(t *testing.T, j Judge) (*Run, *Task) {
	t.Helper()
	b := New()
	b.SetJudge(j)
	r := b.CreateRun("r", "p", t.TempDir(), "main")
	r.CreateStandardThreads()
	task := r.AddTask("research", "research", "compare formats", nil)
	task.StartDispatch("d1", "worker-research")
	return r, task
}

// A rewording of the assumption confirms it: the task is not reopened,
// though the text differs.
func TestARewordedAnswerDoesNotReopen(t *testing.T) {
	j := &stateJudge{p: func(name, state string) float64 {
		if name == QuestionOverturned && strings.Contains(state, "ANSWER:\nuse JSON") {
			return 0.05
		}
		return 0.96
	}}
	r, task := judgedRun(t, j)
	q := r.Ask("research", "Which format?", "json", false)
	task.CompleteDispatch("wrote json", r)
	a, err := r.Answer(q.ID, "use JSON", false)
	if err != nil {
		t.Fatal(err)
	}
	if a.Differs() || a.Overturns == nil || *a.Overturns {
		t.Fatalf("reworded answer read as overturning: %+v", a)
	}
	if got := r.ApplyAnswer(a); len(got) != 0 {
		t.Errorf("reopened %v for a rewording", got)
	}

	q2 := r.Ask("research", "Which encoding?", "utf-8", false)
	a2, _ := r.Answer(q2.ID, "latin-1", false)
	if !a2.Differs() {
		t.Errorf("a different choice did not overturn: %+v", a2)
	}
}

// With the judge off, or undecided, the text decides as before.
func TestOverturnedFallsBackToTheText(t *testing.T) {
	r, _ := judgedRun(t, JudgeFunc(func(context.Context, string, string, string) (float64, bool) { return 0, false }))
	q := r.Ask("research", "Which format?", "json", false)
	a, _ := r.Answer(q.ID, "JSON, 2 spaces", false)
	if a.Overturns != nil || !a.Differs() {
		t.Errorf("undecided judge: %+v", a)
	}
}

// An outcome that still builds on the assumption is handed back once with
// the answer; the next call completes.
func TestAnOutcomeThatIgnoresTheAnswerIsHandedBackOnce(t *testing.T) {
	j := &stateJudge{p: func(name, state string) float64 {
		switch name {
		case QuestionOverturned:
			return 0.96
		case QuestionFollowed:
			if strings.Contains(state, "config.yaml") {
				return 0.97
			}
			return 0.06
		}
		return 0.9
	}}
	r, task := judgedRun(t, j)
	q := r.Ask("research", "Which format?", "json", false)
	if _, err := r.Answer(q.ID, "yaml", false); err != nil {
		t.Fatal(err)
	}
	hint := r.JudgeFollowed(task, "wrote config.json")
	if !strings.Contains(hint, "#1 Which format?") || !strings.Contains(hint, "yaml") {
		t.Fatalf("hint = %q", hint)
	}
	if again := r.JudgeFollowed(task, "wrote config.json"); again != "" {
		t.Errorf("handed back twice: %q", again)
	}

	r2, task2 := judgedRun(t, j)
	q2 := r2.Ask("research", "Which format?", "json", false)
	r2.Answer(q2.ID, "yaml", false)
	if hint := r2.JudgeFollowed(task2, "wrote config.yaml"); hint != "" {
		t.Errorf("an outcome that follows the answer was handed back: %q", hint)
	}
}

// A question the reviewer raised is the reviewer's to follow, not the
// asker's.
func TestTheReviewerFollowsWhatItRaised(t *testing.T) {
	j := &stateJudge{p: func(string, string) float64 { return 0.96 }}
	r, task := judgedRun(t, j)
	cp := r.AddTask("checkpoint", "checkpoint", "This is the checkpoint task.", []string{"research"})
	cp.StartDispatch("d1", "worker-checkpoint")
	q := r.Ask("research", "Which format?", "json", false)
	r.Resolve("checkpoint", Resolution{ID: q.ID, Raise: true})
	r.Answer(q.ID, "yaml", false)
	if got := r.answersFor("research"); len(got) != 0 {
		t.Errorf("asker must follow %v", got)
	}
	if got := r.answersFor("checkpoint"); len(got) != 1 {
		t.Errorf("reviewer must follow %v", got)
	}
	_ = task
}

// The config file switches a judgement off and moves its threshold, read
// again when it changes.
func TestTheConfigFileControlsTheJudge(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	config.UseFile(p)
	t.Cleanup(func() { config.UseFile("") })

	j := &stateJudge{p: func(string, string) float64 { return 0.2 }}
	r, task := judgedRun(t, j)
	if hint := r.JudgeAnswered(task, "an outcome"); hint != "" {
		t.Fatalf("0.2 is above the default answered threshold: %q", hint)
	}
	if err := config.Set("jev.answered.threshold", "0.5"); err != nil {
		t.Fatal(err)
	}
	task2 := r.AddTask("b", "b", "x", nil)
	task2.StartDispatch("d1", "worker-b")
	if hint := r.JudgeAnswered(task2, "an outcome"); hint == "" {
		t.Error("the raised threshold did not apply")
	}

	if err := config.Set("jev.enabled", "off"); err != nil {
		t.Fatal(err)
	}
	n := len(j.asked)
	task3 := r.AddTask("c", "c", "x", nil)
	task3.StartDispatch("d1", "worker-c")
	r.JudgeAnswered(task3, "an outcome")
	if len(j.asked) != n {
		t.Error("asked the judge with jev.enabled off")
	}
	if b, _ := os.ReadFile(p); !strings.Contains(string(b), `"jev.enabled": false`) {
		t.Errorf("file = %s", b)
	}
}
