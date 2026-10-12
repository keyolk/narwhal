// questions.go collects what a run's workers could not settle on their own,
// for the operator to answer in one sitting.
//
// A worker that meets an ambiguity — which of two readings the request
// meant, whether a file is in scope, which behaviour is the right one to
// keep — has had one move: pick something and carry on. The pick went into
// its outcome as a sentence, if at all, and the operator met it after the
// run, in a result built on it. Nothing asked first, so nothing could be
// answered first.
//
// A worker now asks (the ask script): the question, the answer it will
// assume, and whether it can go on without one. The question is recorded on
// the run, not posted as an alert: the monitor gathers a run's open
// questions in one place and the operator answers them together.
//
//   - assume (the default): the worker goes on with its assumption. An
//     answer that differs reopens the task with the answer (followup.go),
//     and what builds on it follows.
//   - blocking: the worker cannot go on without the answer. Its ask call
//     waits for one, inside the tool call, the way task-done waits for its
//     peers; the rest of the run carries on.
//
// The planner can ask too, before it plans (control.go): questions whose
// answers would change the decomposition, answered before any worker
// starts.
//
// Not every question is worth the operator's time as asked. Two workers
// ask the same thing; a third's findings answer a fourth's question. So in
// a run with a synthesis or a checkpoint — a task whose job is to see the
// peers' findings together — a worker's assume question starts as a
// candidate. That task reviews the candidates once it has the findings
// (questions/resolve scripts): it answers what the findings settle, merges
// duplicates, and raises the rest to the operator with the options the
// research found and its recommendation. What it leaves is raised when it
// finishes. Blocking questions and the planner's go to the operator at
// once: someone is waiting on them.
//
// An unanswered assumption is not forgotten when the run ends: the result
// view lists it as an assumption nobody confirmed.
package broker

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Question is one thing a worker or the planner asked the operator.
type Question struct {
	ID int `json:"id"`
	// TaskID is the task that asked, or "plan" for the planner.
	TaskID   string `json:"task_id"`
	Text     string `json:"text"`
	Assumed  string `json:"assumed,omitempty"`
	Blocking bool   `json:"blocking,omitempty"`
	// Options are the choices, when the question is one; Context is what
	// the asker found that makes it a question; Recommended is the option
	// it would pick, 1-based, or 0.
	Options     []string `json:"options,omitempty"`
	Context     string   `json:"context,omitempty"`
	Recommended int      `json:"recommended,omitempty"`
	// Candidate marks a question waiting for the run's synthesis or
	// checkpoint to review before it reaches the operator.
	Candidate bool `json:"candidate,omitempty"`
	// ResolvedBy is the task that answered or merged a candidate from the
	// findings, when it was not the operator; MergedInto is the question
	// it was folded into.
	ResolvedBy string `json:"resolved_by,omitempty"`
	MergedInto int    `json:"merged_into,omitempty"`
	// RaisedBy is the reviewer that sent a candidate on to the operator.
	RaisedBy string `json:"raised_by,omitempty"`
	// Answer is the operator's; empty while open. AcceptedAssumption
	// records that the operator answered "as assumed".
	Answer             string    `json:"answer,omitempty"`
	AcceptedAssumption bool      `json:"accepted_assumption,omitempty"`
	AskedAt            time.Time `json:"asked_at"`
	AnsweredAt         time.Time `json:"answered_at,omitempty"`
}

// PlanAsker is the TaskID of a question the planner asked.
const PlanAsker = "plan"

// Open reports whether the question still waits for an answer — from the
// operator, or for a candidate, from the reviewing task.
func (q Question) Open() bool { return q.AnsweredAt.IsZero() }

// ForOperator reports whether the question waits for the operator.
func (q Question) ForOperator() bool { return q.Open() && !q.Candidate }

// Differs reports whether the answer departs from what was assumed: an
// answer to a question with no assumption always does.
func (q Question) Differs() bool {
	return !q.Open() && !q.AcceptedAssumption && q.MergedInto == 0 &&
		strings.TrimSpace(q.Answer) != "" &&
		!strings.EqualFold(strings.TrimSpace(q.Answer), strings.TrimSpace(q.Assumed))
}

// QuestionSpec is what an asker says.
type QuestionSpec struct {
	Text, Assumed, Context string
	Options                []string
	Recommended            int
	Blocking               bool
}

// Ask records a question and returns it. A worker's assume question in a
// run with a reviewer starts as a candidate.
func (r *Run) Ask(taskID, text, assumed string, blocking bool) Question {
	return r.AskSpec(taskID, QuestionSpec{Text: text, Assumed: assumed, Blocking: blocking})
}

// AskSpec is Ask with options and context.
func (r *Run) AskSpec(taskID string, sp QuestionSpec) Question {
	reviewer := r.reviewer()
	r.mu.Lock()
	q := Question{ID: len(r.questions) + 1, TaskID: taskID, Text: strings.TrimSpace(sp.Text),
		Assumed: strings.TrimSpace(sp.Assumed), Blocking: sp.Blocking, AskedAt: time.Now(),
		Context: strings.TrimSpace(sp.Context), Recommended: sp.Recommended}
	for _, o := range sp.Options {
		if o = strings.TrimSpace(o); o != "" {
			q.Options = append(q.Options, o)
		}
	}
	if q.Recommended < 0 || q.Recommended > len(q.Options) {
		q.Recommended = 0
	}
	q.Candidate = !sp.Blocking && taskID != PlanAsker && reviewer != nil && reviewer.ID != taskID &&
		reviewer.CurrentState() != TaskCompleted && reviewer.CurrentState() != TaskFailed
	r.questions = append(r.questions, q)
	r.mu.Unlock()
	return q
}

// reviewer is the task that reviews workers' candidate questions: a
// checkpoint if the run has one still to finish, else the synthesis.
func (r *Run) reviewer() *Task {
	var synth, check *Task
	for _, s := range r.SnapshotTasks() {
		t := r.GetTask(s.ID)
		switch {
		case isCheckpointName(s.Name, s.Assignment) && s.State != TaskCompleted && s.State != TaskFailed:
			if check == nil || s.ID < check.ID {
				check = t
			}
		case isSynthesisName(s.Name, s.Assignment):
			if synth == nil || s.ID < synth.ID {
				synth = t
			}
		}
	}
	if check != nil {
		return check
	}
	return synth
}

// isCheckpointName reports whether a task is a checkpoint: the point after
// research where the run stops for the operator's choices.
//
// By its name, or an assignment that declares itself one ("This is the
// checkpoint task"). Mentioning a checkpoint is not being one: a task that
// builds on the checkpoint says "read the checkpoint task's outcome", and
// matching that started it alongside the checkpoint it was meant to wait
// for.
func isCheckpointName(name, assignment string) bool {
	if strings.Contains(strings.ToLower(name), "checkpoint") {
		return true
	}
	a := strings.ToLower(assignment)
	return strings.Contains(a, "this is the checkpoint task") || strings.Contains(a, "you are the checkpoint")
}

// Candidates returns the questions waiting for the reviewer.
func (r *Run) Candidates() []Question {
	var out []Question
	for _, q := range r.Questions() {
		if q.Open() && q.Candidate {
			out = append(out, q)
		}
	}
	return out
}

// Resolution is what the reviewer does with a candidate.
type Resolution struct {
	ID int
	// Answer settles it from the findings; MergeInto folds it into another
	// question; Raise sends it to the operator, with the options and
	// recommendation the review found when given.
	Answer      string
	MergeInto   int
	Raise       bool
	Options     []string
	Context     string
	Recommended int
}

// Resolve applies the reviewer's resolution of a candidate.
func (r *Run) Resolve(by string, res Resolution) (Question, error) {
	r.mu.Lock()
	if res.ID < 1 || res.ID > len(r.questions) {
		r.mu.Unlock()
		return Question{}, fmt.Errorf("no question %d", res.ID)
	}
	q := &r.questions[res.ID-1]
	if !q.Open() || !q.Candidate {
		r.mu.Unlock()
		return *q, fmt.Errorf("question %d is not a candidate", res.ID)
	}
	switch {
	case strings.TrimSpace(res.Answer) != "":
		q.Answer, q.ResolvedBy, q.AnsweredAt, q.Candidate = strings.TrimSpace(res.Answer), by, time.Now(), false
	case res.MergeInto > 0:
		if res.MergeInto == res.ID || res.MergeInto > len(r.questions) {
			r.mu.Unlock()
			return *q, fmt.Errorf("question %d cannot be merged into %d", res.ID, res.MergeInto)
		}
		q.MergedInto, q.ResolvedBy, q.AnsweredAt, q.Candidate = res.MergeInto, by, time.Now(), false
		q.Answer = fmt.Sprintf("(merged into #%d)", res.MergeInto)
	case res.Raise:
		q.Candidate, q.RaisedBy = false, by
		if len(res.Options) > 0 {
			q.Options = append([]string(nil), res.Options...)
		}
		if c := strings.TrimSpace(res.Context); c != "" {
			q.Context = c
		}
		if res.Recommended > 0 && res.Recommended <= len(q.Options) {
			q.Recommended = res.Recommended
		}
	default:
		r.mu.Unlock()
		return *q, fmt.Errorf("question %d: answer, merge_into or raise is required", res.ID)
	}
	out := *q
	r.mu.Unlock()
	return out, nil
}

// RaiseCandidates sends every candidate still waiting to the operator: the
// reviewer finished without settling them, and an unreviewed question is
// still better asked than lost.
func (r *Run) RaiseCandidates() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for i := range r.questions {
		if r.questions[i].Open() && r.questions[i].Candidate {
			r.questions[i].Candidate = false
			n++
		}
	}
	return n
}

// Questions returns every question asked on the run, oldest first.
func (r *Run) Questions() []Question {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]Question(nil), r.questions...)
}

// OpenQuestions returns the questions still waiting for the operator.
func (r *Run) OpenQuestions() []Question {
	var out []Question
	for _, q := range r.Questions() {
		if q.ForOperator() {
			out = append(out, q)
		}
	}
	return out
}

// Answer records the operator's answer to question id: text, or with
// asAssumed, the worker's assumption confirmed. It wakes a worker waiting
// on the question. It returns the question as answered.
func (r *Run) Answer(id int, text string, asAssumed bool) (Question, error) {
	r.mu.Lock()
	if id < 1 || id > len(r.questions) {
		r.mu.Unlock()
		return Question{}, fmt.Errorf("no question %d", id)
	}
	q := &r.questions[id-1]
	if !q.Open() {
		r.mu.Unlock()
		return *q, fmt.Errorf("question %d was already answered", id)
	}
	q.Answer, q.AcceptedAssumption = strings.TrimSpace(text), asAssumed
	if asAssumed && q.Answer == "" {
		q.Answer = q.Assumed
	}
	if q.Answer == "" {
		r.mu.Unlock()
		return *q, fmt.Errorf("question %d: an answer is required", id)
	}
	q.AnsweredAt = time.Now()
	out := *q
	waiters := r.answerWaiters[id]
	delete(r.answerWaiters, id)
	r.mu.Unlock()
	for _, ch := range waiters {
		close(ch)
	}
	return out, nil
}

// AwaitAnswer returns a channel closed when question id is answered, and
// whether it already is.
func (r *Run) AwaitAnswer(id int) (<-chan struct{}, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if id >= 1 && id <= len(r.questions) && !r.questions[id-1].Open() {
		return nil, true
	}
	ch := make(chan struct{})
	if r.answerWaiters == nil {
		r.answerWaiters = map[int][]chan struct{}{}
	}
	r.answerWaiters[id] = append(r.answerWaiters[id], ch)
	return ch, false
}

// Question returns question id.
func (r *Run) Question(id int) (Question, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if id < 1 || id > len(r.questions) {
		return Question{}, false
	}
	return r.questions[id-1], true
}

// ApplyAnswer carries an answer to the task that asked: a running task is
// told on the radio, a finished one whose assumption was wrong is reopened
// with the answer, and the run's answer follows (ReopenWithDependents). It
// returns the tasks reopened.
//
// A question a reviewer raised is the reviewer's to carry: it saw the
// findings together, and what follows builds on its outcome, not on the
// asker's. Reopening the asker instead re-ran research whose finding stood
// either way ("both are viable"), and held the checkpoint's dependents until
// it finished again. The asker is still told while it works.
func (r *Run) ApplyAnswer(q Question) []string {
	if q.TaskID == PlanAsker || q.Open() {
		return nil
	}
	who := "The operator"
	if q.ResolvedBy != "" {
		who = q.ResolvedBy + ", from the run's findings,"
	}
	if q.RaisedBy == "" || q.RaisedBy == q.TaskID {
		note := fmt.Sprintf("%s answered your question #%d.\n\nQ: %s\nYou assumed: %s\nAnswer: %s",
			who, q.ID, q.Text, orNone(q.Assumed), q.Answer)
		return r.carryAnswer(q, q.TaskID, note)
	}
	if t := r.GetTask(q.TaskID); t != nil && !q.Blocking && !taskFinished(t) {
		r.tellAnswer(q, q.TaskID, fmt.Sprintf("%s answered your question #%d, which %s raised.\n\nQ: %s\nYou assumed: %s\nAnswer: %s",
			who, q.ID, q.RaisedBy, q.Text, orNone(q.Assumed), q.Answer))
	}
	note := fmt.Sprintf("%s answered question #%d, which %s asked and you raised.\n\nQ: %s\n%s assumed: %s\nAnswer: %s",
		who, q.ID, q.TaskID, q.Text, q.TaskID, orNone(q.Assumed), q.Answer)
	return r.carryAnswer(Question{ID: q.ID, Text: q.Text, Assumed: q.Assumed, Answer: q.Answer,
		AcceptedAssumption: q.AcceptedAssumption, AnsweredAt: q.AnsweredAt}, q.RaisedBy, note)
}

// carryAnswer brings an answer to one task: reopened when it finished on
// an assumption the answer overturns, told when it is still working.
func (r *Run) carryAnswer(q Question, taskID, note string) []string {
	t := r.GetTask(taskID)
	if t == nil {
		return nil
	}
	if taskFinished(t) {
		if !q.Differs() {
			return nil
		}
		return r.ReopenWithDependents(t, note+"\n\nYour outcome was built on the assumption. Revise the work "+
			"to follow the answer.")
	}
	if q.Blocking {
		// The worker is waiting inside its ask call, which returns the
		// answer itself.
		return nil
	}
	r.tellAnswer(q, taskID, note)
	return nil
}

func (r *Run) tellAnswer(q Question, taskID, note string) {
	if q.Differs() {
		note += "\n\nThis differs from what was assumed: change course to follow it."
	} else {
		note += "\n\nThe assumption stands."
	}
	r.PostMessage(WorklogThread, "main", []string{"worker-" + taskID}, PriorityUrgent, note)
}

func orNone(s string) string {
	if strings.TrimSpace(s) == "" {
		return "(nothing)"
	}
	return s
}

// QuestionsByTask groups a run's questions by the task that asked, tasks
// in id order.
func QuestionsByTask(qs []Question) map[string][]Question {
	out := map[string][]Question{}
	for _, q := range qs {
		out[q.TaskID] = append(out[q.TaskID], q)
	}
	for k := range out {
		sort.SliceStable(out[k], func(i, j int) bool { return out[k][i].ID < out[k][j].ID })
	}
	return out
}

// IsReviewer reports whether the task reviews the workers' candidate
// questions: a synthesis or a checkpoint.
func (t *Task) IsReviewer() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return isSynthesisName(t.Name, t.Assignment) || isCheckpointName(t.Name, t.Assignment)
}

// RaisedOpen returns the questions a reviewer raised that the operator has
// not answered: a checkpoint does not complete past them.
func (r *Run) RaisedOpen(reviewer string) []Question {
	var out []Question
	for _, q := range r.Questions() {
		if !q.ForOperator() {
			continue
		}
		// Raised by the reviewer (it resolves candidates into the
		// operator's queue) or asked by it directly.
		if q.TaskID == reviewer || q.RaisedBy == reviewer {
			out = append(out, q)
		}
	}
	return out
}

// IsCheckpoint reports whether the task is a checkpoint.
func (t *Task) IsCheckpoint() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return isCheckpointName(t.Name, t.Assignment)
}

func taskFinished(t *Task) bool {
	s := t.CurrentState()
	return s == TaskCompleted || s == TaskFailed
}
