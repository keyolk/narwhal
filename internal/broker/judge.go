// judge.go lets a cheap decision model weigh in on the mechanical calls a
// dispatcher makes, without ever making one on its own.
//
// Two calls in the dispatch loop were made on evidence too thin for them:
//
//   - A worker that exits without task-done is marked complete if it posted
//     *anything* to the radio. A worker whose only message was a FILE_CLAIM
//     or a "here is my plan" note was recorded as done, and its dependents
//     ran on output that did not exist.
//   - A failed dispatch is retried on the same model, up to the breaker.
//     A task that failed twice because the model could not do it fails a
//     third time the same way and spends a full worker run doing it.
//
// The Judge answers those two questions with a probability. Every judgement
// is a refinement of what the dispatcher already did: Judge nil, Judge
// undecided, or a probability in the uncertain middle all leave the old
// behaviour in place. A gateway outage can make narwhal less informed, never
// differently wrong.
//
// Verdicts are recorded on the run so the monitor can show them — a call
// that silently changed a task's fate would be worse than the call it
// replaced.
package broker

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// Judge answers typed yes/no questions about a piece of state.
//
// An interface rather than a jev.Client so the broker stays testable with
// no network, and because the gateway is one way to get a probability
// rather than the definition of one.
type Judge interface {
	Ask(ctx context.Context, state, name, instructions string) (p float64, ok bool)
}

// JudgeFunc adapts a function to Judge.
type JudgeFunc func(ctx context.Context, state, name, instructions string) (float64, bool)

func (f JudgeFunc) Ask(ctx context.Context, state, name, instructions string) (float64, bool) {
	return f(ctx, state, name, instructions)
}

// Verdict is one judgement the dispatcher asked for, and what it did with it.
type Verdict struct {
	At       time.Time `json:"at"`
	TaskID   string    `json:"task_id"`
	Question string    `json:"question"`
	// P is the judge's probability. Meaningless when Decided is false.
	P       float64 `json:"p"`
	Decided bool    `json:"decided"`
	// Action is what the dispatcher did: the outcome the verdict chose, or
	// the fallback it kept because the verdict was uncertain or missing.
	Action  string `json:"action"`
	Latency int64  `json:"latency_ms"`
	// Asker is the worker that asked, for a judgement a worker requested
	// through its judge script; empty for the dispatcher's own. Ask is the
	// question it put, since a worker's question is free text rather than
	// one of the named ones above.
	Asker string `json:"asker,omitempty"`
	Ask   string `json:"ask,omitempty"`
}

// Sharp reports whether the verdict was confident enough to act on.
func (v Verdict) Sharp() bool { return v.Decided && v.Action != ActionFallback }

const (
	// QuestionNoResult and QuestionSameCause name the two judgements, so
	// the monitor can label a row without parsing the instructions.
	QuestionNoResult  = "no-result"
	QuestionSameCause = "same-cause"

	// QuestionWorker names a judgement a worker asked for.
	QuestionWorker = "worker"

	// ActionAnswered is what a worker's question got: an answer, which the
	// worker acts on, not the dispatcher.
	ActionAnswered = "answered"

	ActionFallback = "fallback"
	ActionRetry    = "retry"
	ActionComplete = "complete"
	ActionEscalate = "escalate"
)

// NoResultThreshold is the P(no result) at or above which a worker that
// exited without task-done is retried instead of marked complete.
//
// Calibrated on the runs on disk (2026-10-03): 20 tasks that completed
// normally, judged on their last three radio messages, against the same
// 20 judged on their first message only. Completed tasks topped out at
// 0.30; at 0.45, 0 of 20 were misjudged as empty and 8 of 20 first
// messages were caught. Some of the other 12 already carried a result, so
// the true miss rate is lower. The threshold is set to lose no finished
// work, and accepts missing some empty ones — a missed one costs what it
// cost before this existed.
const NoResultThreshold = 0.45

// SameCauseThreshold is the P(same cause) at or above which a task that has
// failed twice is moved up a model tier before its last attempt.
//
// Measured on representative failure pairs (2026-10-03): repeated causes —
// the same compile error, no task-done twice, an empty radio twice, or an
// empty radio then no task-done — scored 0.69–0.94; unrelated ones — a
// timeout then a cancel, a launch error then no task-done, an escalation
// then no task-done — scored 0.19–0.29.
const SameCauseThreshold = 0.65

// judgeTimeout bounds one judgement. The reap path runs on the dispatch
// tick, and a slow gateway must not hold the tick; p90 was ~620ms.
//
// Judging is synchronous on purpose. It happens only when a worker exits
// without task-done — not on the normal path — and at worst costs one tick
// two timeouts. An asynchronous verdict would leave a task neither reaped
// nor running across ticks and across a daemon restart, which is a state
// both dispatchers would have to learn; a few seconds against worker runs
// measured in minutes is the cheaper price.
const judgeTimeout = 3 * time.Second

const noResultQuestion = "TASK is the assignment a worker was given. MESSAGES are everything that worker " +
	"posted to the team radio before it exited. Is it true that MESSAGES contain no result at all for " +
	"TASK — only a plan, an acknowledgement, a question, or coordination — so the work would have to be " +
	"redone from scratch?"

const sameCauseQuestion = "TASK is a worker's assignment. FAILURES are the reasons its last attempts " +
	"ended, oldest first. Did the attempts fail for the same underlying reason — one a retry on the same " +
	"model would most likely hit again — rather than for unrelated or transient reasons such as a timeout, " +
	"a crash, or an operator cancel?"

// protocolPrefixes are radio bodies that coordinate rather than report. A
// worker whose every message is one of these produced nothing, and that
// is decidable for free — no judge needed.
var protocolPrefixes = []string{
	SplitRequestPrefix, DepAddPrefix, DepRemovePrefix,
	FileClaimPrefix, FileReleasePrefix, ModelEscalatePrefix,
}

func isProtocolMessage(content string) bool {
	for _, p := range protocolPrefixes {
		if strings.HasPrefix(content, p) {
			return true
		}
	}
	return false
}

// SetJudge installs the judge this run consults. Nil turns judgement off.
func (r *Run) SetJudge(j Judge) {
	r.mu.Lock()
	r.judge = j
	r.mu.Unlock()
}

func (r *Run) currentJudge() Judge {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.judge
}

func (r *Run) recordVerdict(v Verdict) {
	r.mu.Lock()
	r.verdicts = append(r.verdicts, v)
	r.mu.Unlock()
}

// Verdicts returns every judgement made on this run, oldest first.
func (r *Run) Verdicts() []Verdict {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]Verdict(nil), r.verdicts...)
}

func (r *Run) ask(taskID, question, state, instructions string) Verdict {
	v := Verdict{At: time.Now(), TaskID: taskID, Question: question, Action: ActionFallback}
	j := r.currentJudge()
	if j == nil {
		return v
	}
	ctx, cancel := context.WithTimeout(context.Background(), judgeTimeout)
	defer cancel()
	start := time.Now()
	v.P, v.Decided = j.Ask(ctx, state, question, instructions)
	v.Latency = time.Since(start).Milliseconds()
	return v
}

// clip keeps a judged state within what is worth sending: the judge reads
// the shape of an answer, not all of it, and a 40KB report costs latency
// without changing the verdict.
//
// It cuts on a rune boundary: radio traffic is mostly Korean, and a byte
// cut splits a three-byte rune into invalid UTF-8.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}

// ResolveExitWithoutDone decides what to do with a task whose worker exited
// without calling task-done, and does it. It returns the action taken.
//
// This is the one place both dispatchers resolve that case. It replaced an
// "any message at all counts as findings" check that each of them carried.
func (r *Run) ResolveExitWithoutDone(task *Task, agentID string) string {
	var results []string
	for _, m := range r.MessagesSince(0) {
		if m.Sender == agentID && !isProtocolMessage(m.Content) {
			results = append(results, m.Content)
		}
	}
	if len(results) == 0 {
		// Nothing but coordination, or nothing at all. Before this, any
		// FILE_CLAIM counted as having posted findings. This holds with
		// the judge off too: it is a prefix match, not a judgement, and a
		// claim on a path was never a result.
		r.FailAndMaybeEscalate(task, "worker exited without calling task-done")
		return ActionRetry
	}

	tail := results
	if len(tail) > 3 {
		tail = tail[len(tail)-3:]
	}
	parts := make([]string, len(tail))
	for i, c := range tail {
		parts[i] = clip(c, 700)
	}
	state := "TASK:\n" + clip(task.assignment(), 800) + "\n\nMESSAGES:\n" + strings.Join(parts, "\n---\n")

	v := r.ask(task.ID, QuestionNoResult, state, noResultQuestion)
	if v.Decided && v.P >= NoResultThreshold {
		v.Action = ActionRetry
		r.recordVerdict(v)
		r.FailAndMaybeEscalate(task, fmt.Sprintf("worker exited without task-done; radio held no result (p=%.2f)", v.P))
		return ActionRetry
	}
	if v.Decided {
		v.Action = ActionComplete
	}
	if r.currentJudge() != nil {
		r.recordVerdict(v)
	}
	task.CompleteDispatch("completed via radio activity", r)
	return ActionComplete
}

// FailAndMaybeEscalate records a failed dispatch and, when the breaker has
// one attempt left and the failures look like the same cause, moves the
// task up a model tier before that attempt runs.
//
// Retrying a deterministic failure on the same model is the most expensive
// way to reach the breaker. The worker can ask for MODEL_ESCALATE itself,
// but a worker that is failing is the one least placed to notice why.
func (r *Run) FailAndMaybeEscalate(task *Task, reason string) {
	task.FailDispatch(reason, r)
	if task.CurrentState() != TaskReady {
		return
	}
	failures := task.failureReasons()
	if len(failures) != MaxDispatchFailures-1 {
		return
	}
	// Only a task that names its tier can be moved up one. An empty model
	// means the launcher's default, which the broker cannot see; on a run
	// whose default is opus, NextModelTier("") = sonnet would be a
	// downgrade presented as an escalation.
	current := task.CurrentModel()
	if current == "" {
		return
	}
	next, ok := NextModelTier(current)
	if !ok {
		return
	}
	var b strings.Builder
	b.WriteString("TASK:\n" + clip(task.assignment(), 800) + "\n\nFAILURES:\n")
	for i, f := range failures {
		fmt.Fprintf(&b, "%d. %s\n", i+1, clip(verdictTail.ReplaceAllString(f, ""), 500))
	}
	v := r.ask(task.ID, QuestionSameCause, b.String(), sameCauseQuestion)
	if r.currentJudge() == nil {
		return
	}
	if v.Decided && v.P >= SameCauseThreshold {
		v.Action = ActionEscalate
		r.recordVerdict(v)
		task.SetModel(next)
		r.PostMessage(WorklogThread, "coordinator", nil, PriorityFYI,
			fmt.Sprintf("escalating %s to %s before its last attempt: %d failures look like the same cause (p=%.2f)",
				task.ID, next, len(failures), v.P))
		return
	}
	if v.Decided {
		v.Action = ActionRetry
	}
	r.recordVerdict(v)
}

// verdictTail is the "(p=0.71)" a judged failure carries in its reason.
// It is for the operator; shown to the judge, two failures with the same
// cause and different numbers read as less alike than they are — an empty
// radio twice scored 0.68 with the numbers and 0.75 without.
var verdictTail = regexp.MustCompile(`\s*\(p=[0-9.]+\)`)

func (t *Task) assignment() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.Assignment
}

// failureReasons returns why each failed dispatch ended, oldest first.
func (t *Task) failureReasons() []string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	var out []string
	for _, d := range t.Dispatches {
		if d.Status == DispatchFailed {
			out = append(out, d.Output)
		}
	}
	return out
}

// workerJudgeTimeout bounds a worker's question. Longer than the
// dispatcher's: the worker is waiting on the answer anyway, and a gateway
// hiccup is cheaper to ride out than to report as undecided.
const workerJudgeTimeout = 8 * time.Second

// AskForWorker puts a worker's yes/no question to the judge and records it
// on the run, so the monitor shows what the worker asked and was told.
//
// A worker reaches this through its judge script when a call it would
// otherwise make by guessing — is this flaky or real, is this file mine to
// touch, does this output answer the assignment — can be put as a question
// about some state. The answer is advice; the worker decides.
func (r *Run) AskForWorker(asker, question, state string) Verdict {
	v := Verdict{At: time.Now(), TaskID: strings.TrimPrefix(asker, "worker-"),
		Question: QuestionWorker, Action: ActionFallback, Asker: asker, Ask: question}
	j := r.currentJudge()
	if j != nil {
		ctx, cancel := context.WithTimeout(context.Background(), workerJudgeTimeout)
		start := time.Now()
		v.P, v.Decided = j.Ask(ctx, clip(state, 4000), QuestionWorker, question)
		v.Latency = time.Since(start).Milliseconds()
		cancel()
		if v.Decided {
			v.Action = ActionAnswered
		}
	}
	r.recordVerdict(v)
	return v
}
