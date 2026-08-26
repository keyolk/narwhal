// check.go carries a task's end condition — the claim its work must
// satisfy — and what the worker reported when it tested that claim.
//
// The gap this fills is the one #41 documented and could not close. Run
// s1787538246213-1 asked how many exported functions in internal/store
// lack a doc comment, answered 8 where the answer is 0, and finished 3/3
// completed: stamped with a build id, no retry, no failed task, no stuck
// frontier. Every field the snapshot records said the run went well.
// "A wrong-but-completed answer is invisible in the snapshot fields."
//
// So the check is recorded, and its result with it.
//
// # What this is not
//
// It is not enforcement. The worker runs its own check and reports its
// own result; nothing here re-executes the work or verifies the report.
// A worker that wants to claim a check passed can, and no code in this
// package would notice.
//
// That is a deliberate limit rather than an unfinished one. The broker
// does not have the worker's working directory, its tools, or any safe
// way to run arbitrary commands on its behalf, and a gate that executed
// planner-authored shell would be a much larger security surface than
// the thing it verifies. What the gate can do is make the claim explicit
// before the work starts, ask for an answer at the end, and put both in
// the record where an audit can count them — which is precisely what was
// missing when #41 had to be found by hand.
//
// The check is written by the PLANNER, at decomposition time, before any
// worker has an answer to defend. A check the worker composes for itself
// at task-done is a justification, not a test.
package broker

import "strings"

// SetCheck records the end condition for this task. Called from the task
// creation path, so it is set before the task is ever dispatched.
func (t *Task) SetCheck(check string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.Check = strings.TrimSpace(check)
}

// CurrentCheck returns the task's end condition under a read lock.
func (t *Task) CurrentCheck() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.Check
}

// RecordCheckResult stores what the worker reported when it ran the
// check. Returns false when the task has no check to answer, which is how
// the caller tells "nothing was asked" from "nothing was answered".
func (t *Task) RecordCheckResult(result string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.Check == "" {
		return false
	}
	t.CheckResult = strings.TrimSpace(result)
	return true
}

// NeedsCheckResult reports whether this task was given a check and has
// not yet answered it.
//
// This is the question the completion gate asks. It is deliberately not
// "is the answer satisfactory" — judging the content would mean the
// broker deciding whether a worker's own report about its own work is
// convincing, which it has no basis to do.
func (t *Task) NeedsCheckResult() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.Check != "" && t.CheckResult == ""
}
