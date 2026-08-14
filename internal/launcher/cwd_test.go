package launcher

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keyolk/narwhal/internal/broker"
)

// When cwd is missing or is not a directory, fork/exec fails with ENOTDIR
// and Go renders it against the *binary* path:
//
//	fork/exec /Users/x/.local/bin/ccproxy: not a directory
//
// That sends you to inspect a file that is perfectly fine — the failing
// path does not appear in the message at all. Found the hard way: a
// benchmark had left /tmp/narwhal-final as a file, a later run pointed at
// it, and every task failed blaming ccproxy.

func launcherAt(t *testing.T, cwd string) (*Launcher, string, WorkerConfig) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)

	l := New("http://127.0.0.1:1", "run-cwd", cwd)
	l.sessionDir = filepath.Join(home, ".narwhal", "sessions", "run-cwd")

	reg := broker.NewAgentRegistry()
	a := reg.Register("worker-1", "run-cwd", false)
	cfg := WorkerConfig{AgentID: a.ID, TaskID: "task-1", Assignment: "investigate"}
	dir, err := l.SetupAgent(a, cfg)
	if err != nil {
		t.Fatalf("SetupAgent: %v", err)
	}
	return l, dir, cfg
}

func TestLaunchNamesTheBadWorkingDirectory(t *testing.T) {
	// A file where a directory belongs — the exact shape that produced the
	// misleading message.
	notADir := filepath.Join(t.TempDir(), "actually-a-file")
	if err := os.WriteFile(notADir, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	l, dir, cfg := launcherAt(t, notADir)

	err := l.Launch(dir, cfg)
	if err == nil {
		t.Fatal("launching into a file succeeded")
	}
	if !strings.Contains(err.Error(), notADir) {
		t.Errorf("the error does not name the bad path: %v", err)
	}
	if strings.Contains(err.Error(), "ccproxy") {
		t.Errorf("the error still blames the binary: %v", err)
	}
}

func TestLaunchNamesAMissingWorkingDirectory(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "gone")
	l, dir, cfg := launcherAt(t, missing)

	err := l.Launch(dir, cfg)
	if err == nil {
		t.Fatal("launching into a missing directory succeeded")
	}
	if !strings.Contains(err.Error(), missing) {
		t.Errorf("the error does not name the missing path: %v", err)
	}
}
