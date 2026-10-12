// answered.go asks, as a task completes, whether its outcome answers it.
//
// task-done records whatever the worker says. Most tasks carry no end
// condition — 0 of the last six spawned runs did — so nothing checked that
// the outcome was an answer at all, and the ones that were not went on to
// feed their dependents and the run's answer. On disk: "completed via
// radio activity" as a whole outcome, and progress notes ("started on
// this, details to follow") are what a worker writes when it means to come
// back and its turn ends first.
//
// The judge is asked once per dispatch, at task-done, whether the outcome
// answers the assignment. A confident no hands the call back with the
// reason, the way the check gate does; the worker is inside its tool call
// and can act. It is asked once: a worker that answers again is completed
// whatever the judge thinks, so a misjudgement costs one round, never a
// stuck task. No judge, no decision, or anything above the threshold
// completes as before.
package broker

import (
	"fmt"
	"strings"
	"sync"
)

// QuestionAnswered names the judgement made at task-done.
const QuestionAnswered = "answered"

// ActionReturned is a task-done handed back because the outcome did not
// answer the task.
const ActionReturned = "returned"

// AnsweredThreshold is the P(answers the task) below which a task-done is
// handed back once.
//
// Calibrated 2026-10-13 on 24 completed tasks from the runs on disk, each
// judged against its own outcome, another task's outcome, and a progress
// note. Real outcomes scored 0.15–0.91; the lowest, 0.15, was "completed
// via radio activity", which is not an answer. Progress notes scored
// 0.02–0.04 and other tasks' outcomes 0.02–0.46, 21 of 24 under 0.12. At
// 0.12 no real outcome was handed back. Long multi-part investigations
// scored lowest among real ones (0.31–0.48), so the margin is kept wide:
// a missed non-answer costs what it cost before this existed.
const AnsweredThreshold = 0.12

const answeredQuestion = "TASK is the assignment a worker was given, with any follow-up instruction it " +
	"was reopened with. OUTCOME is what it reported when it said it was done. Does OUTCOME actually " +
	"answer TASK — the thing TASK asked for is done or found and reported, not only begun, planned, " +
	"deferred to somewhere else, or about a different task?"

// JudgeAnswered asks whether outcome answers the task, once per dispatch.
// It returns the reason to hand the call back, or "" to complete.
func (r *Run) JudgeAnswered(t *Task, outcome string) string {
	if r.currentJudge() == nil || strings.TrimSpace(outcome) == "" {
		return ""
	}
	t.mu.Lock()
	n := len(t.Dispatches)
	asked := n > 0 && t.Dispatches[n-1].AnswerAsked
	if n > 0 {
		t.Dispatches[n-1].AnswerAsked = true
	}
	task := t.Assignment
	if t.FollowUp != "" {
		task += "\n\nFOLLOW-UP INSTRUCTION:\n" + t.FollowUp
	}
	t.mu.Unlock()
	if asked {
		return ""
	}
	state := "TASK:\n" + clip(task, 1500) + "\n\nOUTCOME:\n" + clip(outcome, 2500)
	v := r.ask(t.ID, QuestionAnswered, state, answeredQuestion)
	if v.Decided && v.P < AnsweredThreshold {
		v.Action = ActionReturned
		r.recordVerdict(v)
		return fmt.Sprintf("The outcome you submitted does not read as an answer to your task "+
			"(p=%.2f that it does). task-done records it as the task's result, and what builds on this "+
			"task reads only that. If the work is done, call task-done again with the outcome itself — "+
			"what you found or changed, not a pointer to the radio or a note that you started. If it is "+
			"not done, finish it first. The next call completes the task either way.", v.P)
	}
	if v.Decided {
		v.Action = ActionComplete
	}
	r.recordVerdict(v)
	return ""
}

// QuestionAffected names the judgement made before reopening a dependent.
const QuestionAffected = "affected"

// ActionSkipped is a dependent left finished because the judge said the
// upstream change could not reach its outcome.
const ActionSkipped = "skipped"

// AffectedThreshold is the P(the change reaches this outcome) below which
// a finished dependent is not reopened.
//
// Measured 2026-10-13 on ten upstream-change/dependent pairs: changes that
// do reach the dependent — a number it doubles, a field it validates, the
// database its migrations target — scored 0.63–0.94; ones that do not — a
// comment, a report's format, a rename in a test helper — scored 0.09–0.50.
// Waking a dependent needlessly costs a worker run; leaving one that
// should have revised costs a wrong answer, so only a confident no skips:
// at 0.3, four of the five unrelated pairs skip and none of the related.
const AffectedThreshold = 0.3

const affectedQuestion = "UPSTREAM is a task this task builds on, and INSTRUCTION is a change the " +
	"operator just asked of it. TASK is this task's assignment and OUTCOME what it concluded. Could " +
	"the change to UPSTREAM alter anything in OUTCOME — a fact it relies on, a file it touched, a " +
	"number it reports — so that this task should revise its work?"

// judgeAffected reports, for each finished dependent, whether it should be
// reopened. The answer task, unfinished ones and any the judge is not
// confident about are; the questions go out together.
func (r *Run) judgeAffected(tasks []*Task, ans *Task, upstream, instruction string) []bool {
	out := make([]bool, len(tasks))
	var wg sync.WaitGroup
	for i, t := range tasks {
		out[i] = true
		if t == ans || t.CurrentState() != TaskCompleted || r.currentJudge() == nil {
			continue
		}
		wg.Add(1)
		go func(i int, t *Task) {
			defer wg.Done()
			s := t.snapshot()
			state := "UPSTREAM:\n" + clip(upstream, 800) + "\n\nINSTRUCTION:\n" + clip(instruction, 800) +
				"\n\nTASK:\n" + clip(s.Assignment, 800) + "\n\nOUTCOME:\n" + clip(s.Outcome, 1500)
			v := r.ask(t.ID, QuestionAffected, state, affectedQuestion)
			if v.Decided && v.P < AffectedThreshold {
				v.Action = ActionSkipped
				out[i] = false
			} else if v.Decided {
				v.Action = ActionComplete
			}
			r.recordVerdict(v)
		}(i, t)
	}
	wg.Wait()
	return out
}
