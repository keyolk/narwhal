// criterion.go judges a task's outcome against the property the planner
// said a correct one has.
//
// A check (check.go) is a test the worker runs and reports on. Nothing
// verifies the report — the broker has no way to re-run the work, so a
// worker that claims its check passed is believed. A criterion closes
// part of that gap: it is a property of the outcome *text* — "every
// finding names its file and line", "the count matches the names listed"
// — which the judge can read against the outcome itself, with no tools
// and no repository.
//
// The planner writes it at decomposition time, before any answer exists to
// defend. At task-done the judge is asked whether the outcome satisfies it;
// a confident no hands the call back once with the criterion, and the next
// call completes either way, like every gate here.
package broker

import (
	"fmt"
	"strings"
)

// QuestionCriterion names the criterion judgement.
const QuestionCriterion = "criterion"

// CriterionThreshold is the P(satisfies) below which a task-done is handed
// back once with the criterion.
//
// Measured 2026-10-13 on four planner-style criteria, each against an
// outcome that meets it and one that does not: met 0.96–0.97, not met
// 0.01–0.03. A criterion is a specific claim, so the judge is far more
// certain than on the open "does this answer the task" question
// (answered.go), and the threshold can sit well clear of both ends.
const CriterionThreshold = 0.3

// SetCriterion records what a correct outcome must satisfy.
func (t *Task) SetCriterion(c string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.Criterion = strings.TrimSpace(c)
}

// JudgeCriterion asks whether the outcome satisfies the task's criterion,
// once per dispatch. It returns the reason to hand the call back, or "".
func (r *Run) JudgeCriterion(t *Task, outcome string) string {
	t.mu.Lock()
	crit := t.Criterion
	n := len(t.Dispatches)
	asked := n > 0 && t.Dispatches[n-1].CriterionAsked
	if n > 0 && crit != "" {
		t.Dispatches[n-1].CriterionAsked = true
	}
	t.mu.Unlock()
	if crit == "" || asked || !r.judging(QuestionCriterion) || strings.TrimSpace(outcome) == "" {
		return ""
	}
	v := r.ask(t.ID, QuestionCriterion, "OUTCOME:\n"+clip(outcome, 4000),
		"CRITERION is what a correct outcome for this task must satisfy. Does OUTCOME satisfy it?\n\nCRITERION: "+crit)
	v.Ask = crit
	if v.Decided && v.P < threshold(QuestionCriterion) {
		v.Action = ActionReturned
		r.recordVerdict(v)
		return fmt.Sprintf("Your outcome does not appear to satisfy this task's criterion (p=%.2f):\n\n  %s\n\n"+
			"The planner set it before the work began, as what a correct outcome must show. Revise the "+
			"work or the outcome so it does — or, if it cannot be met, say why in the outcome — and call "+
			"task-done again. The next call completes the task either way.", v.P, crit)
	}
	if v.Decided {
		v.Action = ActionComplete
	}
	r.recordVerdict(v)
	return ""
}
