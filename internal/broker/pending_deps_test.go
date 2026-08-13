package broker

import (
	"testing"
)

func TestPendingDepsListsUnfinishedPeers(t *testing.T) {
	b := New()
	run := b.CreateRun("run-gate", "test", "/tmp", "main")
	run.AddTask("task-1", "investigate", "a", nil)
	run.AddTask("task-2", "investigate", "b", nil)
	run.AddTask("synth", "synthesis", "integrate", []string{"task-1", "task-2"})

	if got := run.PendingDeps("synth"); len(got) != 2 {
		t.Fatalf("PendingDeps = %v, want both peers", got)
	}

	run.GetTask("task-1").StartDispatch("d1", "worker-task-1")
	run.GetTask("task-1").CompleteDispatch("done", run)

	got := run.PendingDeps("synth")
	if len(got) != 1 || got[0] != "task-2" {
		t.Fatalf("PendingDeps = %v, want [task-2]", got)
	}

	run.GetTask("task-2").StartDispatch("d2", "worker-task-2")
	run.GetTask("task-2").CompleteDispatch("done", run)

	if got := run.PendingDeps("synth"); len(got) != 0 {
		t.Fatalf("PendingDeps = %v, want empty once every peer finished", got)
	}
}

func TestPendingDepsTreatsAFailedPeerAsFinished(t *testing.T) {
	// A failed peer is never going to post again. Blocking on it would
	// strand the synthesis worker until the run times out, losing the
	// findings the other peers did produce.
	b := New()
	run := b.CreateRun("run-gate-fail", "test", "/tmp", "main")
	run.AddTask("task-1", "investigate", "a", nil)
	run.AddTask("synth", "synthesis", "integrate", []string{"task-1"})

	task := run.GetTask("task-1")
	for i := 0; i < 3; i++ {
		task.StartDispatch("d", "worker-task-1")
		task.FailDispatch("boom", run)
	}
	if task.CurrentState() != TaskFailed {
		t.Fatalf("setup: task-1 state = %s, want failed", task.CurrentState())
	}

	if got := run.PendingDeps("synth"); len(got) != 0 {
		t.Fatalf("PendingDeps = %v, want empty — a failed peer cannot finish", got)
	}
}

func TestPendingDepsIgnoresDepsOnTasksThatDoNotExist(t *testing.T) {
	// The planner can name a task it never created. Waiting forever on a
	// typo is worse than proceeding; the graph already draws these as
	// unreachable rather than dropping them.
	b := New()
	run := b.CreateRun("run-gate-ghost", "test", "/tmp", "main")
	run.AddTask("synth", "synthesis", "integrate", []string{"never-created"})

	if got := run.PendingDeps("synth"); len(got) != 0 {
		t.Fatalf("PendingDeps = %v, want empty for a nonexistent dep", got)
	}
}

func TestPendingDepsOfAnUnknownTaskIsEmpty(t *testing.T) {
	b := New()
	run := b.CreateRun("run-gate-unknown", "test", "/tmp", "main")
	if got := run.PendingDeps("nope"); got != nil {
		t.Fatalf("PendingDeps = %v, want nil", got)
	}
}

func TestPendingDepsIsStablyOrdered(t *testing.T) {
	// The list is shown to a worker and posted on the radio; map iteration
	// order would make the same state read differently each poll.
	b := New()
	run := b.CreateRun("run-gate-order", "test", "/tmp", "main")
	for _, id := range []string{"zeta", "alpha", "mid"} {
		run.AddTask(id, "investigate", "x", nil)
	}
	run.AddTask("synth", "synthesis", "integrate", []string{"zeta", "alpha", "mid"})

	want := []string{"alpha", "mid", "zeta"}
	for i := 0; i < 5; i++ {
		got := run.PendingDeps("synth")
		for j := range want {
			if got[j] != want[j] {
				t.Fatalf("PendingDeps = %v, want %v", got, want)
			}
		}
	}
}
