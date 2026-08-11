package store

import (
	"fmt"
	"os"
	"strings"
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

func TestDiscoverIncludesDaemonRuns(t *testing.T) {
	// Regression: the monitor reported "no live runs" while the daemon was
	// hosting three workers, because only the batch CLI writes to the
	// registry file. Discover has to ask the daemon too.
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	lister := func() (string, []string, error) {
		return "http://127.0.0.1:9999", []string{"daemon-run-1", "daemon-run-2"}, nil
	}

	entries := Discover(lister)
	if len(entries) != 2 {
		t.Fatalf("Discover = %d entries, want 2 daemon runs", len(entries))
	}
	for _, e := range entries {
		if e.BrokerURL != "http://127.0.0.1:9999" {
			t.Fatalf("entry %q has broker %q", e.RunID, e.BrokerURL)
		}
		// A daemon run has no per-run process, so PID must stay zero
		// rather than borrowing the daemon's.
		if e.PID != 0 {
			t.Fatalf("daemon run %q should have PID 0, got %d", e.RunID, e.PID)
		}
	}
}

func TestDiscoverMergesBatchAndDaemonRuns(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	if err := RegisterLive(LiveRun{RunID: "batch-run", PID: os.Getpid(), BrokerURL: "http://127.0.0.1:1111"}); err != nil {
		t.Fatalf("register: %v", err)
	}
	lister := func() (string, []string, error) {
		return "http://127.0.0.1:2222", []string{"daemon-run"}, nil
	}

	entries := Discover(lister)
	if len(entries) != 2 {
		t.Fatalf("Discover = %d, want batch + daemon", len(entries))
	}
	byID := map[string]LiveRun{}
	for _, e := range entries {
		byID[e.RunID] = e
	}
	if byID["batch-run"].BrokerURL != "http://127.0.0.1:1111" {
		t.Fatalf("batch run broker = %q", byID["batch-run"].BrokerURL)
	}
	if byID["daemon-run"].BrokerURL != "http://127.0.0.1:2222" {
		t.Fatalf("daemon run broker = %q", byID["daemon-run"].BrokerURL)
	}
}

func TestDiscoverDoesNotDuplicate(t *testing.T) {
	// If a run somehow appears in both places, it must not be listed twice.
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	if err := RegisterLive(LiveRun{RunID: "shared", PID: os.Getpid(), BrokerURL: "http://127.0.0.1:1111"}); err != nil {
		t.Fatalf("register: %v", err)
	}
	lister := func() (string, []string, error) {
		return "http://127.0.0.1:2222", []string{"shared"}, nil
	}

	entries := Discover(lister)
	if len(entries) != 1 {
		t.Fatalf("Discover = %d, want 1 (deduped)", len(entries))
	}
	// The registry entry wins: it carries the owning pid.
	if entries[0].PID == 0 {
		t.Fatal("expected the batch entry to win, keeping its pid")
	}
}

func TestDiscoverToleratesMissingDaemon(t *testing.T) {
	// Monitoring a batch run must still work when no daemon was ever
	// started, so a lister error is "no daemon runs", not a failure.
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	if err := RegisterLive(LiveRun{RunID: "batch-only", PID: os.Getpid(), BrokerURL: "u"}); err != nil {
		t.Fatalf("register: %v", err)
	}
	lister := func() (string, []string, error) {
		return "", nil, errNoDaemon
	}

	entries := Discover(lister)
	if len(entries) != 1 || entries[0].RunID != "batch-only" {
		t.Fatalf("Discover = %v, want just the batch run", entries)
	}

	// A nil lister is also valid (callers without daemon support).
	if entries := Discover(nil); len(entries) != 1 {
		t.Fatalf("Discover(nil) = %d, want 1", len(entries))
	}
}

func TestFindLiveInSelectsNewestWhenIDEmpty(t *testing.T) {
	entries := []LiveRun{
		{RunID: "newest", StartedAt: 300},
		{RunID: "older", StartedAt: 100},
	}
	e, ok := FindLiveIn(entries, "")
	if !ok || e.RunID != "newest" {
		t.Fatalf("FindLiveIn(\"\") = %+v, %v", e, ok)
	}
	e2, ok2 := FindLiveIn(entries, "older")
	if !ok2 || e2.RunID != "older" {
		t.Fatalf("FindLiveIn(older) = %+v, %v", e2, ok2)
	}
	if _, ok3 := FindLiveIn(entries, "missing"); ok3 {
		t.Fatal("FindLiveIn(missing) should not match")
	}
	if _, ok4 := FindLiveIn(nil, ""); ok4 {
		t.Fatal("FindLiveIn on an empty set should not match")
	}
}

func TestSummarizeRunsLabelsDaemonRuns(t *testing.T) {
	out := SummarizeRuns([]LiveRun{
		{RunID: "batch", PID: 123, BrokerURL: "u1"},
		{RunID: "hosted", PID: 0, BrokerURL: "u2"},
	})
	if !strings.Contains(out, "pid=123") {
		t.Fatalf("batch run should show its pid: %q", out)
	}
	if !strings.Contains(out, "(daemon)") {
		t.Fatalf("daemon run should be labeled: %q", out)
	}
	if empty := SummarizeRuns(nil); !strings.Contains(empty, "no live runs") {
		t.Fatalf("empty summary = %q", empty)
	}
}

var errNoDaemon = fmt.Errorf("daemon not running")
