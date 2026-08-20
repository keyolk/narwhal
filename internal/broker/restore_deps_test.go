package broker

import (
	"testing"
)

// #25 taught ParseSplitRequest to refuse a dep that cannot be a task id,
// because a SPLIT_REQUEST whose assignment contained pipes had its prose
// parsed as a dependency list. That fix is intake-only. RestoreRun applies
// no validation, so a snapshot written before it — or by any other path —
// rebuilds the bad deps verbatim on every daemon start.
//
// s1786800188852-1 is that run, still on disk. task-5 carries six deps,
// none of which names a task: " tar -x -C /tmp/head", " then symlink
// node_modules...". It has been re-adopted at every daemon start since
// 2026-08-16 and can never retire.
//
// The deadlock is silent because two functions answer the same question
// differently. PendingDeps skips a dep it cannot find — "waiting forever
// on a typo is worse than proceeding" — so the task reports nothing
// blocking it. recomputeReady returns early on the same dep, so it never
// becomes ready. Nothing is blocked and nothing runs.

func TestRestoreDropsADepThatCannotBeATaskID(t *testing.T) {
	snap := Snapshot{
		RunID: "r1",
		State: RunActive,
		Tasks: []TaskSnapshot{
			{ID: "task-1", State: TaskCompleted, Dispatches: 1},
			{ID: "task-5", State: TaskPending, Deps: []string{
				"task-1",
				" tar -x -C /tmp/head",
				" then symlink node_modules -- run it the same way",
			}},
		},
	}
	run := RestoreRun(snap)
	got := run.Tasks["task-5"].Deps
	if len(got) != 1 || got[0] != "task-1" {
		t.Errorf("restore rebuilt deps that cannot name a task: %q", got)
	}
}

func TestARestoredTaskWhoseDepsAreDoneBecomesReady(t *testing.T) {
	// The point of dropping them. With the junk gone the real dep is
	// satisfied, so the task can run — which is what should have happened
	// on 2026-08-16.
	snap := Snapshot{
		RunID: "r1",
		State: RunActive,
		Tasks: []TaskSnapshot{
			{ID: "task-1", State: TaskCompleted, Dispatches: 1},
			{ID: "task-5", State: TaskPending, Deps: []string{
				"task-1", " tar -x -C /tmp/head",
			}},
		},
	}
	run := RestoreRun(snap)
	ready := run.ReadyTasks()
	if len(ready) != 1 || ready[0].ID != "task-5" {
		var ids []string
		for _, t := range ready {
			ids = append(ids, t.ID)
		}
		t.Errorf("ready tasks after restore: %v, want [task-5]", ids)
	}
}

func TestRestoreKeepsADepOnATaskNotYetCreated(t *testing.T) {
	// Not everything unknown is junk. A split can name a dep before the
	// task exists; that is a real edge and must survive, unlike prose.
	snap := Snapshot{
		RunID: "r1",
		State: RunActive,
		Tasks: []TaskSnapshot{
			{ID: "task-1", State: TaskPending, Deps: []string{"task-9"}},
		},
	}
	run := RestoreRun(snap)
	if got := run.Tasks["task-1"].Deps; len(got) != 1 || got[0] != "task-9" {
		t.Errorf("restore dropped a dep on a task that could still be created: %q", got)
	}
	if len(run.ReadyTasks()) != 0 {
		t.Error("a task waiting on an uncreated dep was made ready")
	}
}
