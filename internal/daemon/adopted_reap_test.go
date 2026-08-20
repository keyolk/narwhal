package daemon

import (
	"os"
	"testing"

	"github.com/keyolk/narwhal/internal/broker"
)

// Two defects that mask each other, which is why they have to move
// together.
//
// AdoptRuns hands the dispatcher the workers it decided are still running,
// keyed runID+"/"+taskID. The dispatcher keys the same map with
// runID+"\x00"+taskID, and runOf/taskOf split on the NUL — so runOf("r1/a")
// is "r1/a", which matches no run, and taskOf is "". The adopted entry
// matches nothing and is never reaped: a worker adopted and then dying
// leaves its task dispatched for the life of the daemon.
//
// But the wrong key is load-bearing. reap decides liveness from
// Launcher.ActiveWorkers, which reads l.workers — populated only when the
// launcher itself spawns a process. A restarted daemon's launcher CANNOT
// contain a worker it did not spawn, so every adopted worker reads as
// exited. Fix the key alone and the first tick fails the dispatch of a
// worker that is alive and working, then launches a second one on the same
// task. A hang becomes a duplicate.
//
// So reap has to consult the same evidence adoption did: the pid on disk.

func TestAnAdoptedWorkerIsTrackedUnderTheKeyReapUses(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	orphanRun(t, "r1", broker.TaskSnapshot{ID: "a", State: broker.TaskDispatched})
	writePID(t, "r1", "a", os.Getpid())

	sess := NewSession()
	_, running := AdoptRuns(sess)

	d := NewDispatcher(sess)
	d.AdoptRunning(running)

	d.mu.Lock()
	tracked := d.running[runKey("r1", "a")]
	d.mu.Unlock()
	if tracked != "worker-a" {
		t.Fatalf("the dispatcher cannot find the adopted worker under its own "+
			"key format: %q", tracked)
	}
}

func TestAnAdoptedWorkerThatIsAliveIsNotReaped(t *testing.T) {
	// The trap. The launcher did not spawn this process and never will
	// list it, so liveness has to come from the same place adoption got
	// it: the recorded pid.
	t.Setenv("HOME", t.TempDir())
	stubWorker(t)
	orphanRun(t, "r1", broker.TaskSnapshot{ID: "a", State: broker.TaskDispatched})
	writePID(t, "r1", "a", os.Getpid()) // this test process: certainly alive

	sess := NewSession()
	_, running := AdoptRuns(sess)
	run := sess.Broker.GetRun("r1")
	sess.LauncherFor("r1", t.TempDir())

	d := NewDispatcher(sess)
	d.AdoptRunning(running)
	d.tick()

	if got := run.GetTask("a").CurrentState(); got != broker.TaskDispatched {
		t.Errorf("a live adopted worker's task moved to %s on the first tick", got)
	}
	if n := run.GetTask("a").DispatchCount(); n > 1 {
		t.Errorf("a second worker was launched on a task already being worked: "+
			"%d dispatches", n)
	}
}

func TestAnAdoptedWorkerThatDiedIsReaped(t *testing.T) {
	// And the defect the key mismatch was hiding: once the process is
	// gone, the task must not stay dispatched forever.
	t.Setenv("HOME", t.TempDir())
	stubWorker(t)
	orphanRun(t, "r1", broker.TaskSnapshot{ID: "a", State: broker.TaskDispatched})
	writePID(t, "r1", "a", 999999) // a pid that is not running

	sess := NewSession()
	_, running := AdoptRuns(sess)
	run := sess.Broker.GetRun("r1")
	sess.LauncherFor("r1", t.TempDir())

	d := NewDispatcher(sess)
	d.AdoptRunning(running)
	d.tick()

	if got := run.GetTask("a").CurrentState(); got == broker.TaskDispatched {
		t.Error("a task whose adopted worker is gone is still dispatched")
	}
}
