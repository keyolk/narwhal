// answer_judge.go puts the operator's answers to the judge: whether an
// answer overturns what the asker assumed, and whether an outcome
// finished after the answer follows it.
//
// The question loop (questions.go) turned on a string comparison. An
// answer that differed from the assumption in letters — "json, indented
// two spaces" against "json", or "yes" against "keep the current format"
// — reopened the task and everything that built on it, a worker run each,
// to redo work the answer had confirmed. One that matched in letters but
// not in meaning was never carried at all.
//
// And nothing checked that an answer was taken. A running task was told
// to change course on the radio, a reopened one was given the answer as
// its instruction, a checkpoint was handed the answers at task-done — and
// whatever it reported next was recorded, whether it built on the answer
// or on what it had assumed before.
//
// So two judgements, both refinements like every other here:
//
//   - overturned, once per answer: whether the answer calls for different
//     work than the assumption. It is what Differs reports when the judge
//     decided; the string comparison stands when it did not.
//   - followed, once per dispatch at task-done: for each answer that did
//     overturn an assumption the task asked or raised, whether the outcome
//     builds on it. A confident no hands the call back once with the
//     answers; the next call completes either way.
package broker

import (
	"fmt"
	"strings"
	"sync"
)

const (
	// QuestionOverturned and QuestionFollowed name the two judgements.
	QuestionOverturned = "overturned"
	QuestionFollowed   = "followed"
)

// OverturnedThreshold is the P(the answer calls for different work) at or
// above which an answer overturns the assumption. Below it the assumption
// is taken as confirmed.
//
// Measured 2026-10-13 on 18 assumption/answer pairs (answer_judge_test.go
// keeps the cases): the same answer in other words scored 0.04–0.12, a
// different choice 0.94–0.97, and the assumption plus a detail — "json,
// indented two spaces", "sqlite, under ~/.dayx", "keep v1 but mark it
// deprecated" — 0.33–0.55. A detail is usually a requirement the work
// built on the assumption does not yet meet, so the threshold sits under
// all of them: what the judge saves is the reopen for a rewording, which
// is where the string comparison was wrong, and nothing else.
const OverturnedThreshold = 0.3

// FollowedThreshold is the P(the outcome builds on the answer) below
// which a task-done is handed back once with the answers.
//
// Measured 2026-10-13 on twelve outcome/answer pairs: outcomes built on
// the answer scored 0.90–0.97, ones still built on the assumption
// 0.04–0.12, and ones that acknowledge the answer and go on with the
// assumption 0.08–0.11 — but 0.78 for one that opened with "answer
// received" before doing the old thing. At 0.4 every outcome built on the
// assumption is handed back and none built on the answer.
const FollowedThreshold = 0.4

const overturnedQuestion = "A worker asked QUESTION and, while waiting, assumed ASSUMED and built its " +
	"work on it. ANSWER is the operator's answer. Does ANSWER call for different work than ASSUMED — a " +
	"different choice, or a requirement the work built on ASSUMED would not meet — rather than confirm " +
	"ASSUMED, say it in other words, or add something work built on ASSUMED already satisfies?"

const followedQuestion = "During this task the operator answered QUESTION with ANSWER, replacing what " +
	"the task had assumed (ASSUMED). OUTCOME is what the task now reports as done. Is OUTCOME built on " +
	"ANSWER — its work and conclusions follow ANSWER — rather than still on ASSUMED, or only mentioning " +
	"ANSWER without acting on it?"

// judgeOverturned asks whether question id's answer overturns its
// assumption and records the verdict on the question. Only an answer the
// string comparison cannot settle is put: no assumption, the assumption
// accepted, or the same text, each decides itself.
func (r *Run) judgeOverturned(id int) {
	q, ok := r.Question(id)
	if !ok || q.Open() || q.AcceptedAssumption || q.MergedInto != 0 || strings.TrimSpace(q.Assumed) == "" ||
		strings.EqualFold(strings.TrimSpace(q.Answer), strings.TrimSpace(q.Assumed)) ||
		!r.judging(QuestionOverturned) {
		return
	}
	state := "QUESTION:\n" + clip(q.Text, 800) + "\n\nASSUMED:\n" + clip(q.Assumed, 800) +
		"\n\nANSWER:\n" + clip(q.Answer, 800)
	v := r.ask(q.TaskID, QuestionOverturned, state, overturnedQuestion)
	v.Ask = fmt.Sprintf("#%d", q.ID)
	if !v.Decided {
		r.recordVerdict(v)
		return
	}
	overturns := v.P >= threshold(QuestionOverturned)
	if overturns {
		v.Action = ActionOverturned
	} else {
		v.Action = ActionKept
	}
	r.recordVerdict(v)
	r.mu.Lock()
	if id >= 1 && id <= len(r.questions) {
		r.questions[id-1].Overturns = &overturns
	}
	r.mu.Unlock()
}

const (
	// ActionOverturned is an answer that overturned the assumption;
	// ActionKept one the judge read as confirming it.
	ActionOverturned = "overturned"
	ActionKept       = "kept"
)

// answersFor are the answered questions whose answer the task must build
// on: ones it asked, or a reviewer raised, that overturned the assumption.
func (r *Run) answersFor(taskID string) []Question {
	var out []Question
	for _, q := range r.Questions() {
		if q.Open() || !q.Differs() || q.TaskID == PlanAsker {
			continue
		}
		carrier := q.TaskID
		if q.RaisedBy != "" {
			carrier = q.RaisedBy
		}
		if carrier == taskID {
			out = append(out, q)
		}
	}
	return out
}

// JudgeFollowed asks, once per dispatch, whether the outcome builds on
// each answer the task was given. It returns the reason to hand the call
// back, naming the answers it does not follow, or "" to complete.
func (r *Run) JudgeFollowed(t *Task, outcome string) string {
	if !r.judging(QuestionFollowed) || strings.TrimSpace(outcome) == "" {
		return ""
	}
	qs := r.answersFor(t.ID)
	if len(qs) == 0 {
		return ""
	}
	t.mu.Lock()
	n := len(t.Dispatches)
	asked := n > 0 && t.Dispatches[n-1].FollowAsked
	if n > 0 {
		t.Dispatches[n-1].FollowAsked = true
	}
	t.mu.Unlock()
	if asked {
		return ""
	}
	missed := make([]bool, len(qs))
	var wg sync.WaitGroup
	for i, q := range qs {
		wg.Add(1)
		go func(i int, q Question) {
			defer wg.Done()
			state := "QUESTION:\n" + clip(q.Text, 600) + "\n\nASSUMED:\n" + clip(orNone(q.Assumed), 600) +
				"\n\nANSWER:\n" + clip(q.Answer, 600) + "\n\nOUTCOME:\n" + clip(outcome, 3000)
			v := r.ask(t.ID, QuestionFollowed, state, followedQuestion)
			v.Ask = fmt.Sprintf("#%d", q.ID)
			if v.Decided && v.P < threshold(QuestionFollowed) {
				v.Action = ActionReturned
				missed[i] = true
			} else if v.Decided {
				v.Action = ActionComplete
			}
			r.recordVerdict(v)
		}(i, q)
	}
	wg.Wait()
	var lines []string
	for i, q := range qs {
		if missed[i] {
			lines = append(lines, fmt.Sprintf("  #%d %s\n     answer: %s (instead of %s)", q.ID, q.Text, q.Answer, orNone(q.Assumed)))
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return "Your outcome does not appear to build on the operator's answer:\n\n" + strings.Join(lines, "\n") +
		"\n\nThe operator chose this over what was assumed, and the tasks after you read your outcome. " +
		"Revise the work so it follows the answer — or, if it cannot, say why in the outcome — and call " +
		"task-done again. The next call completes the task either way."
}
