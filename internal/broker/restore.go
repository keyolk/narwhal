// restore.go rebuilds a Run from a persisted snapshot.
//
// The broker holds everything in memory, so a daemon restart used to lose
// every run it was hosting. The snapshots were already on disk — written
// every tick — and nothing read them back, so a restart turned a run in
// flight into an orphan: its tasks frozen at "dispatched" forever, its
// workers still running with the old broker's URL baked into their
// scripts, reporting to a port that no longer answers.
package broker

import (
	"strings"
	"time"
)

// RestoreRun rebuilds a Run from a snapshot, preserving task states.
//
// This is deliberately not CreateRun + AddTask. AddTask starts a task at
// pending and recomputes readiness, which would erase the very thing worth
// restoring — that three of these tasks completed and one was mid-flight.
//
// Dispatch history does not survive. A Dispatch names an agent whose token
// died with the process that minted it, so replaying them would rebuild
// identities nothing can authenticate. What the snapshot preserves is the
// count, so the retry budget a task has already spent is not refunded by a
// restart.
// validDeps drops dependencies that cannot name a task.
//
// #25 taught ParseSplitRequest to refuse these, because a SPLIT_REQUEST
// whose assignment contained pipes had its prose parsed as a dependency
// list. That fix is intake-only, and a snapshot written before it rebuilds
// the bad deps verbatim on every daemon start: s1786800188852-1 task-5
// carries six deps that are shell-pipeline fragments, and has been
// re-adopted at every start since 2026-08-16.
//
// The deadlock is silent because two functions answer the same question
// differently. PendingDeps skips a dep it cannot find, so the task reports
// nothing blocking it; recomputeReady returns early on the same dep, so it
// never becomes ready. Nothing is blocked and nothing runs.
//
// A dep on a task that does not exist YET is a different thing and is
// kept — a split can name its dependency before creating it. Only names
// that could never be a task id are dropped.
func validDeps(deps []string) []string {
	if len(deps) == 0 {
		return nil
	}
	out := make([]string, 0, len(deps))
	for _, d := range deps {
		if validTaskID(strings.TrimSpace(d)) {
			out = append(out, strings.TrimSpace(d))
		}
	}
	return out
}

func RestoreRun(s Snapshot) *Run {
	r := &Run{
		ID:        s.RunID,
		Prompt:    s.Prompt,
		CWD:       s.CWD,
		State:     s.State,
		CreatedAt: time.Unix(s.StartedAt, 0),
		Tasks:     make(map[string]*Task, len(s.Tasks)),
		Threads:   make(map[string]*Thread),
	}
	if s.StartedAt == 0 {
		r.CreatedAt = time.Now()
	}
	if r.State == "" {
		r.State = RunActive
	}

	for _, ts := range s.Tasks {
		t := &Task{
			ID:         ts.ID,
			RunID:      s.RunID,
			Name:       ts.Name,
			Assignment: ts.Assignment,
			Deps:       validDeps(ts.Deps),
			State:      ts.State,
			Model:      ts.Model,
			CreatedAt:  r.CreatedAt,
		}
		// Dispatch history is rebuilt from the count, which is all the
		// snapshot keeps. The statuses follow from the task's own state:
		// to have reached attempt N you must have failed N-1 times, and
		// the last one is however the task currently stands.
		//
		// Marking every attempt failed would be wrong in a way that bites
		// immediately — a task on its first dispatch would be one failure
		// from the circuit breaker the moment it was restored. Refunding
		// them all would be wrong the other way.
		for i := 0; i < ts.Dispatches; i++ {
			d := &Dispatch{ID: "restored", TaskID: ts.ID, Status: DispatchFailed}
			if i == ts.Dispatches-1 {
				switch ts.State {
				case TaskCompleted:
					d.Status = DispatchDone
				case TaskDispatched:
					d.Status = DispatchRunning
				}
				// The snapshot records one outcome: the last dispatch's.
				// Earlier attempts' reasons were never stored, so they stay
				// empty rather than being copied onto every attempt.
				d.Output = ts.Outcome
			}
			t.Dispatches = append(t.Dispatches, d)
		}
		// A dispatched task always had a dispatch; a snapshot that says
		// otherwise predates the count being recorded. Without one there
		// is nothing to attach a harvested outcome to, and the result is
		// silently dropped.
		if len(t.Dispatches) == 0 && ts.State == TaskDispatched {
			t.Dispatches = append(t.Dispatches, &Dispatch{
				ID: "restored", TaskID: ts.ID, Status: DispatchRunning,
			})
		}
		r.Tasks[ts.ID] = t
	}

	// Bring pending tasks whose deps are all satisfied up to ready.
	//
	// recomputeReady is otherwise only reached from AddTask and
	// CompleteDispatch, so a task whose last dep completed before the
	// crash had no future event left to promote it: the dispatcher saw a
	// pending task, PendingDeps reported nothing blocking it, and it sat
	// there for the life of the daemon. The frontier the snapshot records
	// has to be resumed, not merely reloaded.
	for _, t := range r.Tasks {
		t.recomputeReady(r)
	}

	// The radio is the run's memory of what its workers told each other. A
	// restored run that lost it would have peers referring to findings no
	// reader can see.
	for _, m := range s.Messages {
		if m == nil {
			continue
		}
		r.messages = append(r.messages, m)
		if m.Seq > r.seqCounter {
			r.seqCounter = m.Seq
		}
	}
	for _, th := range s.Threads {
		r.Threads[th.ID] = &Thread{
			ID: th.ID, RunID: s.RunID, Name: th.Name,
			Participants: append([]string(nil), th.Participants...),
			CreatedAt:    r.CreatedAt,
		}
	}

	return r
}

// AdoptRun installs a restored run under the broker's lock.
func (b *Broker) AdoptRun(r *Run) {
	b.mu.Lock()
	b.runs[r.ID] = r
	b.mu.Unlock()
}
