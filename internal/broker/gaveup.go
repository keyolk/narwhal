// gaveup.go asks, as a task completes, whether its worker gave up without
// trying another way.
//
// A worker told "if it is impossible on this host, report the errors" can
// meet that clause in a few turns and complete. Nothing downstream doubts
// it: the answered judge (answered.go) passes it, since a reported
// impossibility does answer such an assignment, and the synthesis repeats
// it. In the SWE-Atlas measure-2 trial a worker declared a headless kitty
// launch impossible after `which Xvfb` and `file` — 26 seconds, 5 turns —
// while the single-agent baseline ran it in the task's docker image; the
// run lost the six rubrics that needed the launch. Across the kitty runtime
// tasks on disk, 3 of 7 assignments with such a clause ended "impossible",
// and no peer or synthesis overturned one.
//
// The judge is asked three factual questions about the outcome and the
// worker's own radio reports, not whether to accept them: asked "should the
// coordinator accept this", Jev accepted that same false impossibility at
// 0.89. The decision is made here: a negative conclusion, together with
// either no alternative shown or an environment limit blamed with no
// workaround tried, hands the call back once, the way the other task-done
// gates do. The next call completes whatever the judge thinks.
package broker

import (
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"
)

// QuestionGaveUp names the judgement made at task-done.
const QuestionGaveUp = "gave-up"

// GaveUpThreshold is the P(gave up untried) — the larger of the two
// untried questions — above which a negative outcome is handed back.
//
// GaveUpNegative is the P(conclusion is negative) that must also be
// exceeded; it is a constant because it gates what the question is about
// rather than how sure the judge must be.
//
// Calibrated 2026-10-22 through this code path — one question per call,
// the TASK/OUTCOME/REPORTS state below — on two sets. The format matters:
// the same reports asked as one batched request scored the measure-2
// worker 0.57 on env-unworked, and 0.45 here.
//
//   - 93 worker outcomes from the runs on disk, labelled blind (prod
//     incident runs left out, since the judge is a remote gateway). The
//     one premature negative scored 0.45. One other outcome cleared
//     negative 0.7, at 0.41: an audit whose findings were absences ("no
//     CLI exists"), which is a result, not a give-up.
//   - 40 synthetic reports. Legitimate negatives — two or more
//     alternatives shown — scored at most 0.19; premature ones 0.33–0.89,
//     13 of 16 above 0.35. Two premature ones never cleared negative 0.7,
//     and all three misses are give-ups without an environment excuse
//     ("no callers" after one narrow grep), where nothing in the text
//     says what was not tried.
//
// 0.35 hands back the measure-2 worker and, of the 93, that one audit
// too. The costs are not symmetric: a miss loses a result the run needed,
// a wrong handback costs one more round, and the hint asks for what was
// tried rather than for more work when nothing else exists.
const (
	GaveUpThreshold = 0.35
	GaveUpNegative  = 0.7
)

const (
	gaveUpNegativeQuestion = "TASK is a worker's assignment, OUTCOME what it reported at task-done, REPORTS " +
		"what it posted to the radio. Is the worker's main conclusion negative: the assigned thing could not " +
		"be done, does not exist, is unsupported, failed, or it reports a blocker instead of the result?"
	gaveUpUntriedQuestion = "TASK is a worker's assignment, OUTCOME what it reported at task-done, REPORTS " +
		"what it posted to the radio. Does the worker conclude negatively (it could not do it, the thing does " +
		"not exist or is unsupported, it cannot be reproduced) WITHOUT showing that it actually tried at " +
		"least one reasonable alternative approach or searched exhaustively?"
	gaveUpEnvQuestion = "TASK is a worker's assignment, OUTCOME what it reported at task-done, REPORTS " +
		"what it posted to the radio. Does the worker blame a limitation of the environment (missing tool, " +
		"wrong platform or architecture, permissions, network, missing build) without showing an attempt to " +
		"work around it?"
)

// JudgeGaveUp asks whether a negative outcome was reached without trying
// another way, once per dispatch. It returns the reason to hand the call
// back, or "" to complete.
func (r *Run) JudgeGaveUp(t *Task, outcome string) string {
	if !r.judging(QuestionGaveUp) || strings.TrimSpace(outcome) == "" {
		return ""
	}
	t.mu.Lock()
	n := len(t.Dispatches)
	asked := n > 0 && t.Dispatches[n-1].GaveUpAsked
	agent := ""
	if n > 0 {
		t.Dispatches[n-1].GaveUpAsked = true
		agent = t.Dispatches[n-1].AgentID
	}
	task := t.Assignment
	if t.FollowUp != "" {
		task += "\n\nFOLLOW-UP INSTRUCTION:\n" + t.FollowUp
	}
	t.mu.Unlock()
	if asked {
		return ""
	}
	state := "TASK:\n" + clip(task, 1500) + "\n\nOUTCOME:\n" + clip(outcome, 2000) +
		"\n\nREPORTS:\n" + clip(r.reportsBy(agent), 4000)

	questions := []string{gaveUpNegativeQuestion, gaveUpUntriedQuestion, gaveUpEnvQuestion}
	vs := make([]Verdict, len(questions))
	var wg sync.WaitGroup
	for i, q := range questions {
		wg.Add(1)
		go func(i int, q string) {
			defer wg.Done()
			vs[i] = r.ask(t.ID, QuestionGaveUp, state, q)
		}(i, q)
	}
	wg.Wait()
	neg, untried, env := vs[0], vs[1], vs[2]

	// One verdict for the three, so the monitor shows one call per
	// task-done. P is what the threshold was compared against.
	v := neg
	v.P = max(untried.P, env.P)
	v.Decided = neg.Decided && untried.Decided && env.Decided
	v.Latency = max(neg.Latency, untried.Latency, env.Latency)
	v.Ask = fmt.Sprintf("negative=%.2f untried=%.2f env-unworked=%.2f", neg.P, untried.P, env.P)
	if !v.Decided {
		r.recordVerdict(v)
		return ""
	}
	if neg.P > GaveUpNegative && v.P > threshold(QuestionGaveUp) {
		v.Action = ActionReturned
		r.recordVerdict(v)
		return "Your outcome is a negative conclusion — it could not be done, or does not exist — and it does " +
			"not show another way being tried. A peer or the synthesis will take it as settled. Before " +
			"concluding, try at least one concrete alternative: a different environment (a container or " +
			"image, if one is available), installing or building what is missing, another tool, or a wider " +
			"search. If you did try alternatives, or none exists, say in the outcome what you tried and what " +
			"each one returned, then call task-done again. The next call completes the task either way."
	}
	v.Action = ActionComplete
	r.recordVerdict(v)
	return ""
}

// reportsBy is what agent posted to the radio, oldest first, newest kept
// when clipped.
func (r *Run) reportsBy(agent string) string {
	if agent == "" {
		return ""
	}
	var parts []string
	for _, m := range r.MessagesSince(0) {
		if m.Sender == agent {
			parts = append(parts, m.Content)
		}
	}
	s := strings.Join(parts, "\n---\n")
	// Keep the tail: a worker's last message is usually its conclusion.
	if len(s) > 4000 {
		i := len(s) - 4000
		for i < len(s) && !utf8.RuneStart(s[i]) {
			i++
		}
		s = "…" + s[i:]
	}
	return s
}
