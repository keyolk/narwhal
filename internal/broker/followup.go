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
//
// A task does not stand alone, so neither does reopening it. What builds on
// it — the tasks that depend on it, and in the end the run's answer — was
// written from the outcome the instruction is about to change, so those
// are reopened behind it (ReopenWithDependents): they wait on it through
// the ordinary dep gate and revise their own outcomes once it is done. And
// a resumed worker that finds the instruction bigger than its task may
// split new work off; in a run whose answer is already written, that work
// is made a dependency of the answer task, which is reopened to fold it in
// (foldIntoAnswer). Without either, the run's answer would stay the one
// written before the instruction, however much work followed it.
package broker

import (
	"fmt"
	"sort"
	"strings"
)

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
	return t.reopen(instruction, TaskReady, true)
}

// reopen returns a finished task to state with a follow-up. A dependent
// reopened behind another task goes to pending, so the dep gate holds it
// until what it builds on is done; withFailed says whether a failed task
// is reopened too, which an operator's instruction does and a cascade does
// not — a downstream failure has its own cause, and the operator can see it.
func (t *Task) reopen(instruction string, state TaskState, withFailed bool) bool {
	instruction = strings.TrimSpace(instruction)
	if instruction == "" {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	switch t.State {
	case TaskCompleted, TaskFailed:
		if t.State == TaskFailed && !withFailed {
			return false
		}
		t.FollowUp = instruction
		t.CheckResult = ""
		// A failed task spent its retries; the follow-up gets its own.
		t.ReopenedAt = len(t.Dispatches)
		t.State = state
		return true
	case TaskReady, TaskPending:
		if t.FollowUp != "" {
			t.FollowUp += "\n\n" + instruction
			return true
		}
	}
	return false
}

// inFollowUp reports whether the task has been reopened and is working
// through its follow-up — not yet finished with it.
func (t *Task) inFollowUp() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.ReopenedAt > 0 && t.State != TaskCompleted && t.State != TaskFailed
}

// ReopenWithDependents reopens a finished task with an instruction, and
// every finished task that builds on it, transitively, with word of why.
// It returns the ids reopened, the task first; none when the task itself
// was not reopened.
//
// Dependents still running are left to the radio, where the reopened
// task's work will reach them; the walk goes on through them, since what
// builds on them was written from the same picture.
//
// Each finished dependent other than the run's answer task is first put to
// the judge: could this instruction change anything its outcome rests on?
// A confident no leaves it, and what builds only on it, finished — "add a
// comment explaining the retry" need not wake the runbook writer. The
// answer task is always reopened: whatever changed upstream, the answer is
// what has to say so. Undecided, as everywhere, means reopen.
func (r *Run) ReopenWithDependents(t *Task, instruction string) []string {
	if !t.Reopen(instruction) {
		return nil
	}
	children := map[string][]string{}
	for _, s := range r.SnapshotTasks() {
		for _, d := range s.Deps {
			children[d] = append(children[d], s.ID)
		}
	}
	for _, c := range children {
		sort.Strings(c)
	}
	ans := r.answerTask()
	upstream := t.assignment()
	out := []string{t.ID}
	seen := map[string]bool{t.ID: true}
	level := []string{t.ID}
	for len(level) > 0 {
		var next []*Task
		for _, id := range level {
			for _, c := range children[id] {
				if seen[c] {
					continue
				}
				seen[c] = true
				if ct := r.GetTask(c); ct != nil {
					next = append(next, ct)
				}
			}
		}
		affected := r.judgeAffected(next, ans, upstream, instruction)
		level = nil
		for i, ct := range next {
			if !affected[i] {
				continue
			}
			level = append(level, ct.ID)
			if ct.reopen(upstreamNote(t.ID, instruction), TaskPending, false) {
				out = append(out, ct.ID)
			}
		}
	}
	return out
}

// upstreamNote is the follow-up a dependent is reopened with.
func upstreamNote(root, instruction string) string {
	return fmt.Sprintf("Task %s, which this task builds on, was reopened by the operator with this "+
		"instruction:\n\n%s\n\nIts outcome is going to change. Your task-done will wait until it has "+
		"finished; then read its updated outcome and what it posted, revise your own work to match, "+
		"and call task-done with your full updated outcome. If nothing you concluded depends on the "+
		"change, say that in the outcome.", root, instruction)
}

// answerTask is the task whose outcome is the run's answer — the
// synthesis — or nil when the answer is composed from several.
func (r *Run) answerTask() *Task {
	var ids []string
	for _, s := range r.SnapshotTasks() {
		if isSynthesisName(s.Name, s.Assignment) {
			ids = append(ids, s.ID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	sort.Strings(ids)
	return r.GetTask(ids[0])
}

// foldIntoAnswer makes a task split off after the run's answer was written
// a dependency of the answer task, reopening it to fold the new work in.
// While the answer task is itself working through a follow-up, the dep
// is added so its task-done waits for the new work. A run still on its
// first pass is left as the planner and its workers arranged it.
func (r *Run) foldIntoAnswer(newID, name, sender string) {
	ans := r.answerTask()
	if ans == nil || ans.ID == newID {
		return
	}
	following := ans.inFollowUp()
	done := ans.CurrentState() == TaskCompleted
	if !done && !following {
		return
	}
	ans.AddDep([]string{newID}, r)
	if done {
		ans.reopen(fmt.Sprintf("A new task, %s (%s), was split off by %s after this run's answer was "+
			"written. Your task-done will wait until it has finished; then fold what it found into the "+
			"answer and call task-done with the full updated answer.", newID, name, sender), TaskPending, false)
	}
	r.PostMessage(WorklogThread, "coordinator", []string{"main"}, PriorityNormal,
		fmt.Sprintf("%s %s split off %s after the answer was written; %s will fold it into the answer.",
			RelayPrefix, sender, newID, ans.ID))
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
