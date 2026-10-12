// followup.go reopens a finished task with an instruction from the operator.
//
// A task ended at task-done and its worker exited, so an instruction
// addressed to it afterwards — "@schema also cover the legacy payload" —
// went onto the radio, was reported as sent, and reached no one. The worker
// that knew the task, its files and its reasoning was gone, and the only
// way to act on the instruction was a whole new request that started from
// nothing.
//
// The worker is not really gone: its Claude session is on disk under the id
// the launcher pinned. Reopening puts the task back in the dispatch queue
// with the instruction attached, and the dispatcher resumes that session
// with it instead of starting a fresh worker (see server.DispatchTask). The
// task completes again through the same task-done, with the same gates.
package broker

import "strings"

// Reopen returns a finished task to ready with a follow-up instruction, and
// reports whether it did. A task that is not finished is left alone: one
// still running takes instructions on the radio, and one not yet started
// will read them when it starts.
//
// A second instruction before the resumed worker starts joins the first,
// so neither is lost to the other.
//
// The check result is cleared: it answered for the work as it was, and the
// follow-up changes the work.
func (t *Task) Reopen(instruction string) bool {
	instruction = strings.TrimSpace(instruction)
	if instruction == "" {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	switch t.State {
	case TaskCompleted, TaskFailed:
		t.FollowUp = instruction
		t.CheckResult = ""
		// A failed task spent its retries; the follow-up gets its own.
		t.ReopenedAt = len(t.Dispatches)
		t.State = TaskReady
		return true
	case TaskReady:
		if t.FollowUp != "" {
			t.FollowUp += "\n\n" + instruction
			return true
		}
	}
	return false
}

// CurrentFollowUp is the instruction a reopened task is waiting to be
// resumed with, or "".
func (t *Task) CurrentFollowUp() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.FollowUp
}

// PreviousOutcome is what the task concluded before it was reopened: the
// output of its last dispatch that finished.
func (t *Task) PreviousOutcome() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	for i := len(t.Dispatches) - 1; i >= 0; i-- {
		if d := t.Dispatches[i]; d.Status == DispatchDone && strings.TrimSpace(d.Output) != "" {
			return d.Output
		}
	}
	return ""
}

// clearFollowUp is called when the task reaches a terminal state again: the
// instruction has been acted on, or the task gave up on it.
func (t *Task) clearFollowUp() { t.FollowUp = "" }
