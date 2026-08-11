package store

import (
	"os"
	"testing"
)

func TestRegisterAndListLive(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	// Use our own pid so the entry survives pruning.
	pid := os.Getpid()
	if err := RegisterLive(LiveRun{
		RunID:     "live-1",
		PID:       pid,
		BrokerURL: "http://127.0.0.1:1234",
		CWD:       "/tmp/repo",
		Prompt:    "test prompt",
	}); err != nil {
		t.Fatalf("RegisterLive: %v", err)
	}

	entries := ListLive()
	if len(entries) != 1 {
		t.Fatalf("live entries = %d, want 1", len(entries))
	}
	if entries[0].RunID != "live-1" || entries[0].BrokerURL != "http://127.0.0.1:1234" {
		t.Fatalf("entry = %+v", entries[0])
	}
	if entries[0].StartedAt == 0 {
		t.Fatal("StartedAt should be auto-populated")
	}
}

func TestPrunesDeadProcesses(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	// pid 1 is init/launchd and always alive; a very high pid almost
	// certainly is not. Register the live one via our own pid.
	if err := RegisterLive(LiveRun{RunID: "alive", PID: os.Getpid(), BrokerURL: "u1"}); err != nil {
		t.Fatalf("register alive: %v", err)
	}
	// Write a dead entry directly so RegisterLive's own pruning does not
	// remove it before we can observe the prune-on-read behavior.
	entries := loadRegistry()
	entries = append(entries, LiveRun{RunID: "dead", PID: 999999, BrokerURL: "u2", StartedAt: 1})
	if err := writeRegistry(entries); err != nil {
		t.Fatalf("write: %v", err)
	}

	live := ListLive()
	if len(live) != 1 {
		t.Fatalf("expected dead entry pruned, got %d entries: %+v", len(live), live)
	}
	if live[0].RunID != "alive" {
		t.Fatalf("surviving entry = %q, want alive", live[0].RunID)
	}
}

func TestFindLive(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	pid := os.Getpid()
	if err := RegisterLive(LiveRun{RunID: "run-x", PID: pid, BrokerURL: "u"}); err != nil {
		t.Fatalf("register: %v", err)
	}

	// By id.
	e, ok := FindLive("run-x")
	if !ok || e.RunID != "run-x" {
		t.Fatalf("FindLive(run-x) = %+v, %v", e, ok)
	}

	// Empty id returns the newest.
	e2, ok2 := FindLive("")
	if !ok2 || e2.RunID != "run-x" {
		t.Fatalf("FindLive(\"\") = %+v, %v", e2, ok2)
	}

	// Unknown id.
	_, ok3 := FindLive("nope")
	if ok3 {
		t.Fatal("FindLive(nope) should not match")
	}
}

func TestDeregisterLive(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	pid := os.Getpid()
	if err := RegisterLive(LiveRun{RunID: "run-y", PID: pid, BrokerURL: "u"}); err != nil {
		t.Fatalf("register: %v", err)
	}
	if len(ListLive()) != 1 {
		t.Fatal("expected 1 live entry before deregister")
	}
	if err := DeregisterLive(pid); err != nil {
		t.Fatalf("deregister: %v", err)
	}
	if got := len(ListLive()); got != 0 {
		t.Fatalf("live entries after deregister = %d, want 0", got)
	}
}
