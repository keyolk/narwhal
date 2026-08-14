package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// `narwhal daemon status` always exited 0, whether or not a daemon was
// running, which made it useless as a shell condition. make daemon-restart
// branches on exactly that: it read a dead daemon as a live one, tried to
// stop it, and aborted the restart on the failure — so an install left no
// daemon running at all, and the next spawn had nothing to talk to.

func TestStatusFailsWhenNoDaemonIsRunning(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	out, code := daemonStatus()
	if code == 0 {
		t.Errorf("status exited 0 with no daemon running: %s", out)
	}

	var payload struct {
		Running bool `json:"running"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("status is not JSON: %v", err)
	}
	if payload.Running {
		t.Errorf("status reported a daemon that does not exist: %s", out)
	}
}

func TestStatusFailsOnAStalePidfile(t *testing.T) {
	// A daemon killed with SIGKILL leaves its pidfile behind. The file
	// existing is not the daemon existing.
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".narwhal")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// A pid that cannot be in use: the max is 99999 on macOS and
	// 4194304 on Linux, and either way this one is free.
	if err := os.WriteFile(filepath.Join(dir, "daemon.pid"),
		[]byte(strconv.Itoa(4194305)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, code := daemonStatus(); code == 0 {
		t.Error("a stale pidfile was reported as a running daemon")
	}
}
