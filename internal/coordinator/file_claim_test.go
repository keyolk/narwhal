package coordinator

import (
	"strings"
	"testing"

	"github.com/keyolk/narwhal/internal/broker"
	"github.com/keyolk/narwhal/internal/launcher"
)

// claimCoord builds a coordinator over a run with two tasks, ready for
// intake tests that do not need the dispatch loop to run.
func claimCoord(t *testing.T) (*Coordinator, *broker.Run) {
	t.Helper()
	b := broker.New()
	reg := broker.NewAgentRegistry()
	run := b.CreateRun("run-claim", "test", "/tmp", "main")
	run.CreateThread("worklog", "worklog", []string{"main"})
	run.AddTask("task-1", "one", "do one", nil)
	run.AddTask("task-2", "two", "do two", nil)
	return New(run, reg, newFakeRunner(), testConfig()), run
}

func TestFileClaimIsRecorded(t *testing.T) {
	c, run := claimCoord(t)
	run.PostMessage("worklog", "worker-task-1", nil, broker.PriorityNormal,
		broker.FormatFileClaimRequest(broker.FileClaimPrefix, "task-1", []string{"a.go"}))

	c.intakeDepEdgeRequests()

	if owner := run.FileOwner("a.go"); owner != "task-1" {
		t.Fatalf("FileOwner = %q, want task-1", owner)
	}
}

func TestConflictingClaimIsAnsweredOnTheRadio(t *testing.T) {
	// A dropped conflict is worse than a rejected one: the second worker
	// would go ahead and overwrite. It has to be told who holds the file.
	c, run := claimCoord(t)
	run.PostMessage("worklog", "worker-task-1", nil, broker.PriorityNormal,
		broker.FormatFileClaimRequest(broker.FileClaimPrefix, "task-1", []string{"shared.go"}))
	run.PostMessage("worklog", "worker-task-2", nil, broker.PriorityNormal,
		broker.FormatFileClaimRequest(broker.FileClaimPrefix, "task-2", []string{"shared.go"}))

	c.intakeDepEdgeRequests()

	var reply *broker.Message
	for _, m := range run.MessagesSince(0) {
		if m.Sender == "coordinator" && strings.Contains(m.Content, "FILE_CONFLICT") {
			reply = m
		}
	}
	if reply == nil {
		t.Fatal("no FILE_CONFLICT reply posted to the radio")
	}
	if reply.Priority != broker.PriorityUrgent {
		t.Errorf("conflict reply priority = %q, want urgent", reply.Priority)
	}
	if len(reply.Mentions) != 1 || reply.Mentions[0] != "worker-task-2" {
		t.Errorf("conflict reply mentions = %v, want [worker-task-2]", reply.Mentions)
	}
	if !strings.Contains(reply.Content, "task-1") {
		t.Errorf("conflict reply does not name the holder:\n%s", reply.Content)
	}
	if owner := run.FileOwner("shared.go"); owner != "task-1" {
		t.Errorf("the conflicting claim took the path: owner=%q", owner)
	}
}

func TestFileReleaseFreesThePath(t *testing.T) {
	c, run := claimCoord(t)
	run.PostMessage("worklog", "worker-task-1", nil, broker.PriorityNormal,
		broker.FormatFileClaimRequest(broker.FileClaimPrefix, "task-1", []string{"a.go"}))
	run.PostMessage("worklog", "worker-task-1", nil, broker.PriorityNormal,
		broker.FormatFileClaimRequest(broker.FileReleasePrefix, "task-1", []string{"a.go"}))

	c.intakeDepEdgeRequests()

	if owner := run.FileOwner("a.go"); owner != "" {
		t.Fatalf("path still held after release: %q", owner)
	}
}

func TestExitedWorkerReleasesItsClaims(t *testing.T) {
	// A worker that forgets FILE_RELEASE would otherwise strand the path
	// for the rest of the run.
	c, run := claimCoord(t)
	run.ClaimFiles("task-1", []string{"held.go"})

	f := newFakeRunner()
	c.launcher = f
	c.mu.Lock()
	c.running["task-1"] = "worker-task-1"
	c.mu.Unlock()

	c.reapFinishedWorkers() // worker-task-1 is not in f.active, so it "exited"

	if owner := run.FileOwner("held.go"); owner != "" {
		t.Fatalf("exited worker's claim survived: owner=%q", owner)
	}
}

// unused reference keeps the launcher import honest if the file is trimmed.
var _ = launcher.WorkerConfig{}
