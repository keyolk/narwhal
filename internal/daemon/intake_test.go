package daemon

import (
	"strings"
	"testing"

	"github.com/keyolk/narwhal/internal/broker"
)

// Workers get six wrapper scripts that mutate the run — split, dep-add,
// dep-remove, file-claim, file-release, escalate — and their instructions
// explain all of them. Every one was inert on the daemon path: the tick
// only reaped and dispatched, so the message landed on the radio and
// nothing read it. Nearly every run is interactive, so the documented half
// of the worker protocol did not work where it was actually used.

func intakeSession(t *testing.T) (*Session, *broker.Run, *Dispatcher) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	stubWorker(t)

	sess := NewSession()
	sess.URL = "http://127.0.0.1:1"
	run := sess.Broker.CreateRun("r-intake", "test", t.TempDir(), "main")
	run.CreateStandardThreads()
	sess.LauncherFor("r-intake", run.CWD)
	return sess, run, NewDispatcher(sess)
}

func TestDaemonAcceptsSplitRequests(t *testing.T) {
	sess, run, d := intakeSession(t)
	_ = sess
	run.AddTask("first", "first", "do first", nil)

	run.PostMessage(broker.PlanningThread, "worker-first", nil, broker.PriorityNormal,
		broker.FormatSplitRequest("discovered", "discovered", "investigate the new area", nil))

	d.intake("r-intake", run)

	if run.GetTask("discovered") == nil {
		t.Fatal("a split request on the daemon path created no task")
	}
}

func TestDaemonAppliesDepEdges(t *testing.T) {
	_, run, d := intakeSession(t)
	run.AddTask("a", "a", "do a", nil)
	run.AddTask("b", "b", "do b", nil)

	run.PostMessage(broker.WorklogThread, "worker-b", nil, broker.PriorityNormal,
		broker.FormatDepEdgeRequest(broker.DepAddPrefix, "b", []string{"a"}))
	d.intake("r-intake", run)

	if pending := run.PendingDeps("b"); len(pending) != 1 || pending[0] != "a" {
		t.Fatalf("DEP_ADD had no effect: PendingDeps(b) = %v", pending)
	}

	run.PostMessage(broker.WorklogThread, "worker-b", nil, broker.PriorityNormal,
		broker.FormatDepEdgeRequest(broker.DepRemovePrefix, "b", []string{"a"}))
	d.intake("r-intake", run)

	if pending := run.PendingDeps("b"); len(pending) != 0 {
		t.Fatalf("DEP_REMOVE had no effect: PendingDeps(b) = %v", pending)
	}
}

func TestDaemonAppliesFileClaims(t *testing.T) {
	// Cursor's swarm experiment cut merge conflicts by giving agents clear
	// ownership. Without intake, every claim on an interactive run was a
	// no-op and two workers could write the same file believing they had
	// asked.
	_, run, d := intakeSession(t)
	run.AddTask("a", "a", "do a", nil)
	run.AddTask("b", "b", "do b", nil)

	run.PostMessage(broker.WorklogThread, "worker-a", nil, broker.PriorityNormal,
		broker.FormatFileClaimRequest(broker.FileClaimPrefix, "a", []string{"internal/api/router.go"}))
	d.intake("r-intake", run)

	if owner := run.FileOwner("internal/api/router.go"); owner != "a" {
		t.Fatalf("file owner = %q, want a", owner)
	}

	// A conflicting claim must be answered on the radio, or the second
	// worker has no way to learn it lost.
	run.PostMessage(broker.WorklogThread, "worker-b", nil, broker.PriorityNormal,
		broker.FormatFileClaimRequest(broker.FileClaimPrefix, "b", []string{"internal/api/router.go"}))
	d.intake("r-intake", run)

	var conflict *broker.Message
	for _, m := range run.MessagesSince(0) {
		if strings.Contains(m.Content, "FILE_CONFLICT") {
			conflict = m
		}
	}
	if conflict == nil {
		t.Fatal("a conflicting claim was not answered on the radio")
	}
	if conflict.Priority != broker.PriorityUrgent {
		t.Errorf("conflict priority = %s, want urgent", conflict.Priority)
	}
	if owner := run.FileOwner("internal/api/router.go"); owner != "a" {
		t.Errorf("the conflicting claim took the file: owner = %q", owner)
	}
}

func TestDaemonAppliesModelEscalation(t *testing.T) {
	_, run, d := intakeSession(t)
	task := run.AddTask("a", "a", "do a", nil)
	task.SetModel("haiku")

	run.PostMessage(broker.WorklogThread, "worker-a", nil, broker.PriorityNormal,
		broker.FormatModelEscalateRequest("a", "", "this area needs deeper reading"))
	d.intake("r-intake", run)

	if got := task.CurrentModel(); got != "sonnet" {
		t.Fatalf("model = %q, want the next tier up", got)
	}
}

func TestIntakeAppliesEachRequestOnce(t *testing.T) {
	// The tick runs twice a second. Without a cursor a split request would
	// be reprocessed forever — harmless for splits (the id already exists)
	// but not for escalation, which would walk the model up a tier per
	// tick until it ran out.
	_, run, d := intakeSession(t)
	task := run.AddTask("a", "a", "do a", nil)
	task.SetModel("haiku")

	run.PostMessage(broker.WorklogThread, "worker-a", nil, broker.PriorityNormal,
		broker.FormatModelEscalateRequest("a", "", "harder than expected"))

	for i := 0; i < 5; i++ {
		d.intake("r-intake", run)
	}

	if got := task.CurrentModel(); got != "sonnet" {
		t.Fatalf("model = %q after 5 ticks; the request was applied more than once", got)
	}
}

func TestSplitRequestIsPickedUpByTheRunningLoop(t *testing.T) {
	// The other tests call intake directly, which proves the logic but not
	// that anything runs it. This one goes through the live dispatch loop:
	// the defect was precisely that the loop never looked at the radio.
	t.Setenv("HOME", t.TempDir())
	stubWorker(t)

	sess := NewSession()
	sess.URL = "http://127.0.0.1:1"
	run := sess.Broker.CreateRun("r-loop", "test", t.TempDir(), "main")
	run.CreateStandardThreads()
	sess.LauncherFor("r-loop", run.CWD)
	run.AddTask("first", "first", "do first", nil)

	d := NewDispatcher(sess)
	d.Start()
	defer d.Stop()

	run.PostMessage(broker.PlanningThread, "worker-first", nil, broker.PriorityNormal,
		broker.FormatSplitRequest("discovered", "discovered", "investigate", nil))

	waitFor(t, "the split-requested task to be created", func() bool {
		return run.GetTask("discovered") != nil
	})

	// And it must actually be dispatched, not just created — a task the
	// loop creates but never launches is the same silence in a new place.
	waitFor(t, "the new task to be dispatched", func() bool {
		return run.GetTask("discovered").DispatchCount() > 0
	})
}

func TestExitedWorkerReleasesItsFiles(t *testing.T) {
	// A claim outlives the process that made it. Without this, a worker
	// that exits before its FILE_RELEASE strands the path for the rest of
	// the run and every peer is told to negotiate with a dead task.
	t.Setenv("HOME", t.TempDir())
	stubWorker(t)

	sess := NewSession()
	sess.URL = "http://127.0.0.1:1"
	run := sess.Broker.CreateRun("r-release", "test", t.TempDir(), "main")
	run.CreateStandardThreads()
	sess.LauncherFor("r-release", run.CWD)
	run.AddTask("a", "a", "do a", nil)
	run.ClaimFiles("a", []string{"internal/api/router.go"})

	d := NewDispatcher(sess)
	d.Start()
	defer d.Stop()

	waitFor(t, "task a to reach a terminal state", func() bool {
		s := run.GetTask("a").CurrentState()
		return s == broker.TaskCompleted || s == broker.TaskFailed
	})

	if owner := run.FileOwner("internal/api/router.go"); owner != "" {
		t.Fatalf("an exited worker still holds the file (owner=%q)", owner)
	}
}
