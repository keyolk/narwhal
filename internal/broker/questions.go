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
	// Answer is the operator's; empty while open. AcceptedAssumption
	// records that the operator answered "as assumed".
	Answer             string    `json:"answer,omitempty"`
	AcceptedAssumption bool      `json:"accepted_assumption,omitempty"`
	AskedAt            time.Time `json:"asked_at"`
	AnsweredAt         time.Time `json:"answered_at,omitempty"`
}

// PlanAsker is the TaskID of a question the planner asked.
const PlanAsker = "plan"

// Open reports whether the question still waits for the operator.
func (q Question) Open() bool { return q.AnsweredAt.IsZero() }

// Differs reports whether the answer departs from what was assumed: an
// answer to a question with no assumption always does.
func (q Question) Differs() bool {
	return !q.Open() && !q.AcceptedAssumption &&
		strings.TrimSpace(q.Answer) != "" &&
		!strings.EqualFold(strings.TrimSpace(q.Answer), strings.TrimSpace(q.Assumed))
}

// Ask records a question and returns it.
func (r *Run) Ask(taskID, text, assumed string, blocking bool) Question {
	r.mu.Lock()
	q := Question{ID: len(r.questions) + 1, TaskID: taskID, Text: strings.TrimSpace(text),
		Assumed: strings.TrimSpace(assumed), Blocking: blocking, AskedAt: time.Now()}
	r.questions = append(r.questions, q)
	r.mu.Unlock()
	return q
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
		if q.Open() {
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
func (r *Run) ApplyAnswer(q Question) []string {
	if q.TaskID == PlanAsker || q.Open() {
		return nil
	}
	t := r.GetTask(q.TaskID)
	if t == nil {
		return nil
	}
	note := fmt.Sprintf("The operator answered your question #%d.\n\nQ: %s\nYou assumed: %s\nAnswer: %s",
		q.ID, q.Text, orNone(q.Assumed), q.Answer)
	switch t.CurrentState() {
	case TaskCompleted, TaskFailed:
		if !q.Differs() {
			return nil
		}
		return r.ReopenWithDependents(t, note+"\n\nYour outcome was built on the assumption. Revise the work "+
			"to follow the answer.")
	default:
		if q.Blocking {
			// The worker is waiting inside its ask call, which returns the
			// answer itself.
			return nil
		}
		msg := note
		if q.Differs() {
			msg += "\n\nThis differs from what you assumed: change course to follow it."
		} else {
			msg += "\n\nYour assumption stands."
		}
		r.PostMessage(WorklogThread, "main", []string{"worker-" + q.TaskID}, PriorityUrgent, msg)
		return nil
	}
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
