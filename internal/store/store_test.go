package store

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/keyolk/narwhal/internal/broker"
)

func TestSaveAndLoadRun(t *testing.T) {
	// Isolate the runs directory so the test does not touch the user's
	// real ~/.narwhal/runs.
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	b := broker.New()
	r := b.CreateRun("test-run-1", "analyze", "/tmp/repo", "main")
	r.AddTask("t1", "first", "do thing", nil)
	r.CreateThread("th1", "worklog", []string{"a", "b"})
	r.PostMessage("th1", "a", []string{"b"}, broker.PriorityUrgent, "hello")

	snap := r.Snapshot()
	if err := SaveRun(snap); err != nil {
		t.Fatalf("SaveRun: %v", err)
	}

	loaded, err := LoadRun("test-run-1")
	if err != nil {
		t.Fatalf("LoadRun: %v", err)
	}
	if loaded.RunID != "test-run-1" {
		t.Fatalf("run id = %q, want test-run-1", loaded.RunID)
	}
	if len(loaded.Tasks) != 1 {
		t.Fatalf("tasks = %d, want 1", len(loaded.Tasks))
	}
	if len(loaded.Messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(loaded.Messages))
	}
	if loaded.Messages[0].Content != "hello" {
		t.Fatalf("message content = %q, want hello", loaded.Messages[0].Content)
	}
}

func TestLoadRunMissing(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	_, err := LoadRun("nonexistent")
	if err == nil {
		t.Fatal("expected error for missing run, got nil")
	}
}

func TestListRuns(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	b := broker.New()
	for _, id := range []string{"run-a", "run-b", "run-c"} {
		r := b.CreateRun(id, "test", "/tmp", "main")
		r.AddTask("t", "t", "t", nil)
		if err := SaveRun(r.Snapshot()); err != nil {
			t.Fatalf("SaveRun %s: %v", id, err)
		}
	}

	ids, err := ListRuns()
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(ids) != 3 {
		t.Fatalf("list = %d, want 3", len(ids))
	}
}

func TestSaveRunPermissions(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	b := broker.New()
	r := b.CreateRun("perm-test", "test", "/tmp", "main")
	if err := SaveRun(r.Snapshot()); err != nil {
		t.Fatalf("SaveRun: %v", err)
	}

	path := filepath.Join(tmp, ".narwhal", "runs", "perm-test.json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("perm = %o, want 0600", info.Mode().Perm())
	}
}

// The outcome is why a completed task mattered, and disk is where a
// restarted daemon has to find it. It was absent from TaskSnapshot, so
// every one of the 36 run files already on disk records what each task was
// asked and nothing about what it answered.
func TestTheOutcomeSurvivesDisk(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	b := broker.New()
	r := b.CreateRun("r-outcome", "p", "/tmp", "main")
	task := r.AddTask("task-1", "n", "a", nil)
	task.SetModel("opus")
	task.StartDispatch("d1", "worker-task-1")
	task.CompleteDispatch("4 of 7 SANs are covered", r)

	if err := SaveRun(r.Snapshot()); err != nil {
		t.Fatalf("SaveRun: %v", err)
	}
	loaded, err := LoadRun("r-outcome")
	if err != nil {
		t.Fatalf("LoadRun: %v", err)
	}
	if got := loaded.Tasks[0].Outcome; got != "4 of 7 SANs are covered" {
		t.Errorf("disk lost the outcome: %q", got)
	}
	if got := loaded.Tasks[0].Model; got != "opus" {
		t.Errorf("disk lost the model: %q", got)
	}

	// And the restored run answers the same, so an adopted run's monitor
	// shows what its finished tasks concluded.
	again := broker.RestoreRun(loaded).Snapshot()
	if got := again.Tasks[0].Outcome; got != "4 of 7 SANs are covered" {
		t.Errorf("restore lost the outcome: %q", got)
	}
}
