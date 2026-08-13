package daemon

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/keyolk/narwhal/internal/broker"
	"github.com/keyolk/narwhal/internal/store"
)

// The batch CLI saves a run when its coordinator returns. The daemon had no
// equivalent, so an interactive run — which is how nearly every run happens
// — left nothing on disk. A four-worker run finished real work, its files
// landed in the target repo, and `narwhal show` could not see that the run
// had ever existed.

func TestDaemonPersistsRunsAsTheyProgress(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	stubWorker(t)

	sess := NewSession()
	sess.URL = "http://127.0.0.1:1"
	run := sess.Broker.CreateRun("r-persist", "audit the thing", t.TempDir(), "main")
	run.CreateStandardThreads()
	sess.LauncherFor("r-persist", run.CWD)
	run.AddTask("a", "a", "do a", nil)

	d := NewDispatcher(sess)
	d.Start()
	defer d.Stop()

	waitFor(t, "the run to be written to disk", func() bool {
		_, err := store.LoadRun("r-persist")
		return err == nil
	})

	snap, err := store.LoadRun("r-persist")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if snap.RunID != "r-persist" {
		t.Errorf("run id = %q", snap.RunID)
	}
	if snap.Prompt != "audit the thing" {
		t.Errorf("prompt = %q, want the run's prompt", snap.Prompt)
	}
	if len(snap.Tasks) != 1 {
		t.Errorf("tasks = %d, want 1", len(snap.Tasks))
	}
}

func TestPersistSkipsUnchangedRuns(t *testing.T) {
	// The dispatch loop runs twice a second. Writing every tick would be
	// pointless disk traffic for a graph that only changes when a worker
	// finishes or a message lands.
	t.Setenv("HOME", t.TempDir())

	sess := NewSession()
	sess.URL = "http://127.0.0.1:1"
	run := sess.Broker.CreateRun("r-idle", "test", t.TempDir(), "main")
	run.AddTask("a", "a", "do a", nil)

	d := NewDispatcher(sess)
	if !d.persistRun("r-idle", run) {
		t.Fatal("first persist did not write")
	}
	if d.persistRun("r-idle", run) {
		t.Error("an unchanged run was written again")
	}

	// A state change must get through.
	task := run.GetTask("a")
	task.StartDispatch("d1", "worker-a")
	if !d.persistRun("r-idle", run) {
		t.Error("a state change was not written")
	}

	// So must a new message: the radio is most of a run's value, and a
	// snapshot without it is not a record of what happened.
	run.PostMessage(broker.WorklogThread, "worker-a", nil, broker.PriorityNormal, "found it")
	if !d.persistRun("r-idle", run) {
		t.Error("a new radio message was not written")
	}
}

func TestPersistAllWritesEveryRun(t *testing.T) {
	// Shutdown is exactly when the record matters most, so it ignores the
	// change fingerprint and writes whatever is live.
	t.Setenv("HOME", t.TempDir())

	sess := NewSession()
	sess.URL = "http://127.0.0.1:1"
	for _, id := range []string{"r1", "r2"} {
		run := sess.Broker.CreateRun(id, "test", t.TempDir(), "main")
		run.AddTask("a", "a", "do a", nil)
		sess.LauncherFor(id, run.CWD)
	}

	d := NewDispatcher(sess)
	if n := d.PersistAll(); n != 2 {
		t.Fatalf("PersistAll wrote %d runs, want 2", n)
	}
	for _, id := range []string{"r1", "r2"} {
		if _, err := store.LoadRun(id); err != nil {
			t.Errorf("run %s was not written: %v", id, err)
		}
	}
}

func TestPersistedRunIsReadableByShow(t *testing.T) {
	// The point of persisting is that `narwhal show` can read it back, so
	// assert against the file layout the store defines rather than only
	// against the round trip.
	home := t.TempDir()
	t.Setenv("HOME", home)

	sess := NewSession()
	sess.URL = "http://127.0.0.1:1"
	run := sess.Broker.CreateRun("r-show", "test", t.TempDir(), "main")
	run.AddTask("a", "a", "do a", nil)

	d := NewDispatcher(sess)
	d.persistRun("r-show", run)

	path := filepath.Join(home, ".narwhal", "runs", "r-show.json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("no run file where show looks (%s): %v", path, err)
	}

	ids, err := store.ListRuns()
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	found := false
	for _, id := range ids {
		if id == "r-show" {
			found = true
		}
	}
	if !found {
		t.Errorf("ListRuns did not include the run: %v", ids)
	}
}
