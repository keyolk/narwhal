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

// QuestionFolded names the judgement made when a synthesis says it is done.
const QuestionFolded = "folded"

// FoldedThreshold is the P(the answer takes a peer's findings into
// account) below which a synthesis is handed back to fold that peer in.
//
// Calibrated 2026-10-13 on the nine runs on disk with a synthesis and at
// least two peers: each synthesis judged against its own peers (25) and
// against two peers of another run (16). Own peers scored 0.25–0.97,
// foreign ones 0.06–0.49, 13 of 16 under 0.2. The two lowest own peers were
// real signals — one answer contradicted its peer's count without saying
// why, and one carried only part of a peer's report — but at 0.2 neither
// is handed back: the cost of a wrong handback is one more round, and the
// threshold is kept where it costs none on the runs that went well.
const FoldedThreshold = 0.2

const foldedQuestion = "ANSWER is a run's final answer, written by its synthesis task. PEER is one " +
	"task the synthesis depended on, with what it concluded. Does ANSWER take PEER's findings into " +
	"account — use them, reflect them, or say why they do not matter — rather than leave them out?"

// JudgeFolded asks, for a synthesis completing, whether its answer takes
// each finished dependency's findings into account, once per dispatch. It
// returns the reason to hand the call back, naming the peers left out, or
// "" to complete.
func (r *Run) JudgeFolded(t *Task, answer string) string {
	if r.currentJudge() == nil || !t.IsSynthesis() || strings.TrimSpace(answer) == "" {
		return ""
	}
	t.mu.Lock()
	n := len(t.Dispatches)
	asked := n > 0 && t.Dispatches[n-1].FoldAsked
	if n > 0 {
		t.Dispatches[n-1].FoldAsked = true
	}
	deps := append([]string(nil), t.Deps...)
	t.mu.Unlock()
	if asked {
		return ""
	}
	var peers []TaskSnapshot
	for _, d := range deps {
		if p := r.GetTask(d); p != nil {
			if s := p.snapshot(); s.State == TaskCompleted && strings.TrimSpace(s.Outcome) != "" {
				peers = append(peers, s)
			}
		}
	}
	missing := make([]bool, len(peers))
	var wg sync.WaitGroup
	for i, p := range peers {
		wg.Add(1)
		go func(i int, p TaskSnapshot) {
			defer wg.Done()
			state := "ANSWER:\n" + clip(answer, 3000) + "\n\nPEER " + p.ID + ":\nTASK: " + clip(p.Assignment, 500) +
				"\nCONCLUDED: " + clip(p.Outcome, 1500)
			v := r.ask(t.ID, QuestionFolded, state, foldedQuestion)
			v.Ask = p.ID
			if v.Decided && v.P < FoldedThreshold {
				v.Action = ActionReturned
				missing[i] = true
			} else if v.Decided {
				v.Action = ActionComplete
			}
			r.recordVerdict(v)
		}(i, p)
	}
	wg.Wait()
	var left []string
	for i, p := range peers {
		if missing[i] {
			left = append(left, p.ID)
		}
	}
	if len(left) == 0 {
		return ""
	}
	return fmt.Sprintf("Your answer does not appear to take into account what %s concluded. Read %s "+
		"outcome (and what %s posted to the results thread), then fold it into the answer — or say in "+
		"the answer why it does not matter — and call task-done again with the full answer. The next "+
		"call completes the task either way.", strings.Join(left, ", "),
		map[bool]string{true: "its", false: "their"}[len(left) == 1], map[bool]string{true: "it", false: "they"}[len(left) == 1])
}

// QuestionStuck names the judgement made on a worker that has run long.
const QuestionStuck = "stuck"

// ActionFlagged is a stuck worker reported to the operator. Nothing else
// is done to it: whether to steer, wait or cancel is the operator's call.
const ActionFlagged = "flagged"

// StuckThreshold is the P(stuck) at or above which a running worker is
// reported to the operator.
//
// Calibrated 2026-10-13 on the last twenty actions of every worker
// transcript on disk (10) and two built to be stuck — one failing command
// twenty times, one search-and-read loop. The stuck ones scored 0.97 and
// 0.98; every real worker scored 0.06–0.23, including a synthesis that
// drained the radio 26 times while its peers worked. A report the operator
// did not need is noise on the radio, so only a confident yes is raised.
const StuckThreshold = 0.8

const stuckQuestion = "TASK is a worker's assignment. RECENT is its last actions, oldest first, each " +
	"a tool call and the start of what came back. Is the worker stuck — repeating the same action " +
	"with the same result, or going in circles, without getting closer to finishing TASK? Waiting on " +
	"peers by draining or watching the radio while they work is not stuck."

// JudgeStuck asks whether a running worker is stuck on its recent actions,
// and if it is, says so to the operator on the radio. It reports whether
// the worker was flagged.
func (r *Run) JudgeStuck(t *Task, recent []string) bool {
	if r.currentJudge() == nil || len(recent) == 0 || t.CurrentState() != TaskDispatched {
		return false
	}
	state := "TASK:\n" + clip(t.assignment(), 800) + "\n\nRECENT:\n" + clip(strings.Join(recent, "\n"), 4000)
	v := r.ask(t.ID, QuestionStuck, state, stuckQuestion)
	flagged := v.Decided && v.P >= StuckThreshold
	if flagged {
		v.Action = ActionFlagged
	} else if v.Decided {
		v.Action = ActionComplete
	}
	r.recordVerdict(v)
	if flagged {
		r.PostMessage(WorklogThread, "coordinator", []string{"main"}, PriorityUrgent,
			fmt.Sprintf("%s worker-%s looks stuck (p=%.2f): its last actions repeat without getting closer "+
				"to the task. Its latest: %s. Steer it with @%s, or cancel the run.",
				RelayPrefix, t.ID, v.P, clip(recent[len(recent)-1], 200), t.ID))
	}
	return flagged
}
