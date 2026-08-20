package broker

import (
	"testing"
)

// The intake cursor says how far the radio has been read, so a worker's
// request is applied once rather than on every tick. It lives in the
// dispatcher's memory (daemon/dispatch.go), the snapshot has no field for
// it, and RestoreRun does not rebuild it — so every daemon start reads the
// whole channel again from zero and re-applies everything on it.
//
// Measured across the stored runs: 60 replayable prefixed messages
// (FILE_CLAIM 48, FILE_RELEASE 9, SPLIT_REQUEST 2, MODEL_ESCALATE 1), and
// of 158 distinct (task, path) claims, 110 were never released AND their
// task is already terminal. Those 110 are what a restart re-asserts on
// behalf of tasks that will never answer.
//
// SPLIT_REQUEST survives replay because a split whose id exists is
// dropped. FILE_CLAIM and DEP_ADD do not.

func TestReplayDoesNotReclaimFilesForAFinishedTask(t *testing.T) {
	b := New()
	r := b.CreateRun("r1", "p", "/tmp", "main")
	r.CreateStandardThreads()
	done := r.AddTask("task-1", "n", "a", nil)
	r.AddTask("task-2", "n", "a", nil)

	r.PostMessage(WorklogThread, "worker-task-1", nil, PriorityNormal,
		FormatFileClaimRequest(FileClaimPrefix, "task-1", []string{"main.go"}))
	r.IntakeGraphRequests(0)

	// task-1 finishes. CompleteDispatch releases its claims — in memory,
	// which is the asymmetry: the claim is on the durable radio and the
	// release is not.
	done.StartDispatch("d1", "worker-task-1")
	done.CompleteDispatch("done", r)

	// A restart: the run is rebuilt from disk and the cursor is gone.
	restored := RestoreRun(r.Snapshot())
	restored.IntakeGraphRequests(0)

	held := restored.FileOwner("main.go")
	if held == "task-1" {
		t.Errorf("replay re-claimed main.go for task-1, which has finished; "+
			"a live peer asking for it is told to negotiate with a task that "+
			"will never answer (holder=%q)", held)
	}
}

func TestReplayDoesNotDuplicateDepEdges(t *testing.T) {
	// AddDep is a bare append with no dedup, so a DEP_ADD applied twice
	// leaves the edge twice.
	b := New()
	r := b.CreateRun("r1", "p", "/tmp", "main")
	r.CreateStandardThreads()
	r.AddTask("task-1", "n", "a", nil)
	r.AddTask("task-2", "n", "a", nil)

	r.PostMessage(WorklogThread, "worker-task-2", nil, PriorityNormal,
		FormatDepEdgeRequest(DepAddPrefix, "task-2", []string{"task-1"}))
	r.IntakeGraphRequests(0)

	restored := RestoreRun(r.Snapshot())
	restored.IntakeGraphRequests(0)

	deps := restored.Tasks["task-2"].Deps
	seen := map[string]int{}
	for _, d := range deps {
		seen[d]++
	}
	if seen["task-1"] > 1 {
		t.Errorf("replay duplicated a dep edge: %q", deps)
	}
}

func TestTheIntakeCursorSurvivesASnapshot(t *testing.T) {
	// The root cause, stated directly: whatever the cursor was must come
	// back, or every restart replays the channel.
	b := New()
	r := b.CreateRun("r1", "p", "/tmp", "main")
	r.CreateStandardThreads()
	r.AddTask("task-1", "n", "a", nil)
	r.PostMessage(WorklogThread, "worker-task-1", nil, PriorityNormal,
		FormatFileClaimRequest(FileClaimPrefix, "task-1", []string{"main.go"}))

	after := r.IntakeGraphRequests(0)
	if after == 0 {
		t.Fatal("setup: intake did not advance the cursor")
	}
	if got := RestoreRun(r.Snapshot()).IntakeCursor(); got != after {
		t.Errorf("the cursor came back as %d, want %d", got, after)
	}
}

func TestADepAddedTwiceLeavesOneEdge(t *testing.T) {
	// Defence in depth. The durable cursor stops the replay that made this
	// visible, but AddDep is a bare append: two workers both noticing the
	// same missing edge is an ordinary race, and it would leave the edge
	// twice. A duplicate edge is not merely untidy — PendingDeps and the
	// graph layout both count it.
	b := New()
	r := b.CreateRun("r1", "p", "/tmp", "main")
	r.AddTask("task-1", "n", "a", nil)
	t2 := r.AddTask("task-2", "n", "a", nil)

	t2.AddDep([]string{"task-1"}, r)
	t2.AddDep([]string{"task-1"}, r)

	if got := t2.Deps; len(got) != 1 {
		t.Errorf("adding the same dep twice left %q", got)
	}
}

func TestASnapshotWithNoCursorDoesNotReplay(t *testing.T) {
	// Every run already on disk was written before the cursor existed, so
	// it restores as 0 — and 0 means "read the whole channel", which is
	// exactly the replay. Measured on this machine: 110 claims held by
	// tasks that have already finished, re-asserted on every daemon start.
	//
	// A missing cursor is not the same as a cursor at the start. A run
	// whose messages are already reflected in its task states has been
	// read; treat the absence as "already applied" rather than replaying
	// history against a graph that has moved on.
	b := New()
	r := b.CreateRun("r1", "p", "/tmp", "main")
	r.CreateStandardThreads()
	done := r.AddTask("task-1", "n", "a", nil)
	r.PostMessage(WorklogThread, "worker-task-1", nil, PriorityNormal,
		FormatFileClaimRequest(FileClaimPrefix, "task-1", []string{"main.go"}))
	done.StartDispatch("d1", "worker-task-1")
	done.CompleteDispatch("done", r)

	// An old snapshot: messages, but no cursor field.
	snap := r.Snapshot()
	snap.IntakeCursor = 0

	restored := RestoreRun(snap)
	restored.IntakeGraphRequests(0)
	if got := restored.FileOwner("main.go"); got == "task-1" {
		t.Errorf("a pre-cursor snapshot replayed its channel: main.go held by %q", got)
	}
}
