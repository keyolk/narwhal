package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keyolk/narwhal/internal/broker"
)

// A test that forgets to isolate HOME writes into the developer's real
// ~/.narwhal/runs, and nothing says so. It happened: a run called "r1",
// prompt "p", cwd /var/folders/.../T/TestARunWhoseTasksAllFailed.../001
// sat in the store for days, showing up in `narwhal show` and in the
// monitor's run picker beside real work.
//
// The store cannot tell a test from a run. It can tell that a test binary
// is writing to a home directory that is not a temporary one, and that
// combination is always the mistake.

func isolationSnapshot(id string) broker.Snapshot {
	return broker.Snapshot{
		RunID: id, Prompt: "p", State: broker.RunDone,
		Tasks: []broker.TaskSnapshot{{ID: "a", State: broker.TaskCompleted}},
	}
}

func TestAnUnisolatedSaveIsRefused(t *testing.T) {
	// Deliberately NOT isolated — this is the shape of the mistake, and
	// the point is that it now fails loudly instead of polluting.
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip(err)
	}
	if strings.HasPrefix(home, os.TempDir()) || strings.HasPrefix(home, "/tmp") {
		t.Skip("HOME is already temporary; nothing to protect")
	}

	err = SaveRun(isolationSnapshot("r-should-not-exist"))
	if err == nil {
		// Clean up before failing, so a regression does not leave residue.
		_ = os.Remove(filepath.Join(RunsDir(), "r-should-not-exist.json"))
		t.Fatal("SaveRun wrote into the developer's store from a test")
	}
	if !strings.Contains(err.Error(), "t.Setenv") {
		t.Errorf("the refusal does not say how to fix it: %v", err)
	}
}

func TestAnIsolatedSaveWritesNormally(t *testing.T) {
	// The guard must not break the tests that do isolate, which is nearly
	// all of them.
	home := t.TempDir()
	t.Setenv("HOME", home)

	if err := SaveRun(isolationSnapshot("r-ok")); err != nil {
		t.Fatalf("an isolated SaveRun failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".narwhal", "runs", "r-ok.json")); err != nil {
		t.Errorf("the snapshot was not written: %v", err)
	}
	// And it reads back, so the guard is not silently redirecting writes.
	if got, err := LoadRun("r-ok"); err != nil || got.RunID != "r-ok" {
		t.Errorf("LoadRun after an isolated save: %+v %v", got, err)
	}
}

func TestTheGuardOnlyFiresUnderTest(t *testing.T) {
	// A daemon writing to the real store is the normal case and must not
	// be refused. underTest is what separates them.
	if !underTest() {
		t.Fatal("underTest() is false inside a test binary; the guard can never fire")
	}
	if err := checkTestIsolation(filepath.Join(t.TempDir(), ".narwhal", "runs")); err != nil {
		t.Errorf("a temp-dir write was refused: %v", err)
	}
}
