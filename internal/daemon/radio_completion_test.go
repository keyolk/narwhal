package daemon

import (
	"testing"

	"github.com/keyolk/narwhal/internal/broker"
)

// A worker that posts its findings and then exits without calling
// task-done has done its job. Retrying it redoes work already on the
// radio, and on the third retry the circuit breaker fails a task whose
// output exists — which then releases the completion gate on anything
// depending on it, so one forgetful worker can cascade.
//
// The batch coordinator has always checked this. The interactive path did
// not, which is the same split DispatchableTasks had: a rule only one
// dispatcher knows does not apply to the path most runs take.

func TestRadioActivityCountsAsCompletionOnTheDaemonPath(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	stubWorker(t)

	sess := NewSession()
	sess.URL = "http://127.0.0.1:1"
	run := sess.Broker.CreateRun("r-radio", "test", t.TempDir(), "main")
	run.CreateStandardThreads()
	sess.LauncherFor("r-radio", run.CWD)
	run.AddTask("a", "a", "do a", nil)

	// The stub worker exits immediately without calling task-done, but the
	// findings are on the radio under its agent id.
	run.PostMessage(broker.WorklogThread, "worker-a", nil, broker.PriorityNormal,
		"here is what I found")

	d := NewDispatcher(sess)
	d.Start()
	defer d.Stop()

	waitFor(t, "task a to reach a terminal state", func() bool {
		s := run.GetTask("a").CurrentState()
		return s == broker.TaskCompleted || s == broker.TaskFailed
	})

	if got := run.GetTask("a").CurrentState(); got != broker.TaskCompleted {
		t.Fatalf("task a = %s, want completed — its findings are on the radio", got)
	}
	if got := run.GetTask("a").DispatchCount(); got != 1 {
		t.Errorf("task a was dispatched %d times; a worker that posted should not be retried", got)
	}
}

func TestSilentWorkerStillFails(t *testing.T) {
	// The check must not swallow a genuinely dead worker: one that posted
	// nothing produced nothing, and retrying is the right answer.
	t.Setenv("HOME", t.TempDir())
	stubWorker(t)

	sess := NewSession()
	sess.URL = "http://127.0.0.1:1"
	run := sess.Broker.CreateRun("r-silent", "test", t.TempDir(), "main")
	run.CreateStandardThreads()
	sess.LauncherFor("r-silent", run.CWD)
	run.AddTask("a", "a", "do a", nil)

	d := NewDispatcher(sess)
	d.Start()
	defer d.Stop()

	waitFor(t, "task a to be retried", func() bool {
		return run.GetTask("a").DispatchCount() > 1
	})

	if got := run.GetTask("a").CurrentState(); got == broker.TaskCompleted {
		t.Fatal("a worker that posted nothing was marked complete")
	}
}
