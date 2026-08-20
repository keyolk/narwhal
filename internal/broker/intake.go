// intake.go applies the graph-mutating requests workers post on the radio.
//
// Workers are handed six wrapper scripts — split, dep-add, dep-remove,
// file-claim, file-release, escalate — and their instructions explain all
// of them at length. Each one posts a prefixed message and something has to
// read it back and act.
//
// That reader lived in the batch coordinator only. On the interactive path
// a worker could split a task, claim a file, or ask for a stronger model
// and the message would land on the radio and be ignored — no error, no
// log, just nothing. Since nearly every run is interactive, the documented
// half of the worker protocol did not work where it was actually used.
//
// So the logic lives here, on the Run, and both dispatchers call it. This
// is the fifth rule in this codebase that existed on one path and not the
// other; putting shared behaviour on the shared object is what stops the
// sixth.
package broker

import (
	"fmt"
	"log"
	"strings"
)

// IntakeCursors tracks how far a dispatcher has read the radio. Zero value
// means "nothing read yet", which is correct for a fresh run.
type IntakeCursors struct {
	Split int64
	Graph int64
}

// IntakeSplitRequests creates tasks requested by workers on the planning
// thread, advancing the cursor past what it read.
//
// Existing tasks are immutable, so a split whose id already exists is
// dropped rather than merged — two workers discovering the same missing
// work is a normal race, not an error.
func (r *Run) IntakeSplitRequests(cursor int64) int64 {
	msgs := r.MessagesSince(cursor)
	for _, m := range msgs {
		if m.ThreadID != PlanningThread {
			continue
		}
		taskID, name, assignment, deps, ok := ParseSplitRequest(m.Content)
		if !ok {
			continue
		}
		if r.GetTask(taskID) != nil {
			continue
		}
		r.AddTask(taskID, name, assignment, deps)
		log.Printf("[intake] split-request accepted: %s (%s) deps=%v from %s",
			taskID, name, deps, m.Sender)
	}
	if len(msgs) > 0 {
		return msgs[len(msgs)-1].Seq
	}
	return cursor
}

// IntakeGraphRequests applies dep-edge changes, file claims, and model
// escalations, advancing the cursor past what it read.
//
// Unlike split-request these can arrive on any thread: a worker discovers a
// relationship, is about to write a file, or finds its area too hard, and
// posts to worklog rather than planning.
func (r *Run) IntakeGraphRequests(cursor int64) int64 {
	// The run remembers how far it has been read, and that memory survives
	// a restart. The caller's cursor lives in the dispatcher's process, so
	// a new daemon passes 0 and would otherwise replay the whole channel:
	// file claims re-asserted for tasks that finished long ago, dep edges
	// appended a second time. Take whichever is further along.
	if own := r.IntakeCursor(); own > cursor {
		cursor = own
	}
	msgs := r.MessagesSince(cursor)
	for _, m := range msgs {
		if action, taskID, deps, ok := ParseDepEdgeRequest(m.Content); ok {
			r.applyDepEdge(action, taskID, deps, m.Sender)
			continue
		}
		if action, taskID, paths, ok := ParseFileClaimRequest(m.Content); ok {
			r.applyFileClaim(action, taskID, paths, m.Sender)
			continue
		}
		if taskID, model, reason, ok := ParseModelEscalateRequest(m.Content); ok {
			r.applyModelEscalation(taskID, model, reason, m.Sender)
		}
	}
	if len(msgs) > 0 {
		cursor = msgs[len(msgs)-1].Seq
	}
	r.SetIntakeCursor(cursor)
	return cursor
}

func (r *Run) applyDepEdge(action, taskID string, deps []string, sender string) {
	task := r.GetTask(taskID)
	if task == nil {
		log.Printf("[intake] dep-edge for unknown task %s, ignoring", taskID)
		return
	}
	switch action {
	case DepAddPrefix:
		task.AddDep(deps, r)
		log.Printf("[intake] dep-edge added: %s ← %v from %s", taskID, deps, sender)
	case DepRemovePrefix:
		task.RemoveDep(deps)
		log.Printf("[intake] dep-edge removed: %s ⊘ %v from %s", taskID, deps, sender)
	}
}

func (r *Run) applyFileClaim(action, taskID string, paths []string, sender string) {
	switch action {
	case FileClaimPrefix:
		conflicts := r.ClaimFiles(taskID, paths)
		if len(conflicts) == 0 {
			log.Printf("[intake] files claimed by %s: %v", taskID, paths)
			return
		}
		var b strings.Builder
		fmt.Fprintf(&b, "FILE_CONFLICT: these paths are already held by another task.\n")
		for p, owner := range conflicts {
			fmt.Fprintf(&b, "  %s → held by %s\n", p, owner)
		}
		fmt.Fprintf(&b, "Coordinate on the radio before writing; do not overwrite.")
		r.PostMessage(WorklogThread, "coordinator", []string{sender}, PriorityUrgent, b.String())
		log.Printf("[intake] file conflict for %s: %v", taskID, conflicts)
	case FileReleasePrefix:
		r.ReleaseFiles(taskID, paths)
		log.Printf("[intake] files released by %s: %v", taskID, paths)
	}
}

func (r *Run) applyModelEscalation(taskID, model, reason, sender string) {
	task := r.GetTask(taskID)
	if task == nil {
		log.Printf("[intake] escalation for unknown task %s, ignoring", taskID)
		return
	}

	current := task.CurrentModel()
	target := model
	if target == "" {
		next, ok := NextModelTier(current)
		if !ok {
			log.Printf("[intake] %s already at the strongest tier (%s); not escalating",
				taskID, current)
			return
		}
		target = next
	}
	if target == current {
		log.Printf("[intake] %s already on %s; not escalating", taskID, target)
		return
	}

	task.SetModel(target)
	log.Printf("[intake] escalating %s: %s → %s (%s, from %s)",
		taskID, current, target, reason, sender)

	// Only force a retry if the task is still in flight. A completed task
	// that asks to escalate has already produced its answer; re-running it
	// would discard work the synthesis task may already have drained.
	if task.CurrentState() == TaskDispatched {
		task.FailDispatch("escalated to "+target+": "+reason, r)
	}
}

// ReleaseTaskFiles gives up every path a task still holds. Called when a
// worker exits, so a forgotten FILE_RELEASE cannot strand a path for the
// rest of the run.
func (r *Run) ReleaseTaskFiles(taskID string) {
	var held []string
	for path, owner := range r.FileClaims() {
		if owner == taskID {
			held = append(held, path)
		}
	}
	if len(held) == 0 {
		return
	}
	r.ReleaseFiles(taskID, held)
	log.Printf("[intake] released %d file(s) held by exited %s", len(held), taskID)
}
