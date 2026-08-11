package daemon

import (
	"os"
	"syscall"
	"testing"
)

func TestStatusWhenNotRunning(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	if _, err := Status(); err == nil {
		t.Fatal("expected error when no daemon is running")
	}
	if _, err := URL(); err == nil {
		t.Fatal("URL should fail when no daemon is running")
	}
}

func TestAcquireLockIsExclusive(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	first, err := AcquireLock()
	if err != nil {
		t.Fatalf("first AcquireLock: %v", err)
	}
	defer func() {
		syscall.Flock(int(first.Fd()), syscall.LOCK_UN)
		first.Close()
	}()

	// A second acquire must fail while the first holds the lock — this is
	// what stops two daemons binding different ports and splitting state.
	if _, err := AcquireLock(); err == nil {
		t.Fatal("second AcquireLock should have failed while the lock is held")
	}
}

func TestLockReleaseAllowsReacquire(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	first, err := AcquireLock()
	if err != nil {
		t.Fatalf("first AcquireLock: %v", err)
	}
	syscall.Flock(int(first.Fd()), syscall.LOCK_UN)
	first.Close()

	second, err := AcquireLock()
	if err != nil {
		t.Fatalf("reacquire after release: %v", err)
	}
	syscall.Flock(int(second.Fd()), syscall.LOCK_UN)
	second.Close()
}

func TestWriteStateAndStatusRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	lock, err := AcquireLock()
	if err != nil {
		t.Fatalf("AcquireLock: %v", err)
	}
	defer func() {
		syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		lock.Close()
	}()

	pid := os.Getpid()
	url := "http://127.0.0.1:45678"
	if err := WriteState(lock, pid, url); err != nil {
		t.Fatalf("WriteState: %v", err)
	}

	info, err := Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if info.PID != pid {
		t.Fatalf("pid = %d, want %d", info.PID, pid)
	}
	if info.URL != url {
		t.Fatalf("url = %q, want %q", info.URL, url)
	}

	got, err := URL()
	if err != nil {
		t.Fatalf("URL: %v", err)
	}
	if got != url {
		t.Fatalf("URL() = %q, want %q", got, url)
	}
}

func TestStatusRejectsStalePidFile(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	// Write a pidfile with nobody holding the lock — the shape kill -9
	// leaves behind. Status must not report a live daemon just because a
	// pid happens to be readable.
	lock, err := AcquireLock()
	if err != nil {
		t.Fatalf("AcquireLock: %v", err)
	}
	if err := WriteState(lock, os.Getpid(), "http://127.0.0.1:1"); err != nil {
		t.Fatalf("WriteState: %v", err)
	}
	syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	lock.Close()

	if _, err := Status(); err == nil {
		t.Fatal("Status should reject a pidfile whose lock nobody holds")
	}
}

func TestClearStateRemovesFiles(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	lock, err := AcquireLock()
	if err != nil {
		t.Fatalf("AcquireLock: %v", err)
	}
	if err := WriteState(lock, os.Getpid(), "http://127.0.0.1:2"); err != nil {
		t.Fatalf("WriteState: %v", err)
	}
	syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	lock.Close()

	ClearState()

	for _, p := range []string{pidFilePath(), portFilePath(), urlFilePath()} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("%s still exists after ClearState", p)
		}
	}
}

func TestSessionMintsUniqueRunIDs(t *testing.T) {
	s := NewSession()
	seen := map[string]bool{}
	// Two spawns in the same millisecond must not collide, which is why
	// the id carries a process-local counter as well as a timestamp.
	for i := 0; i < 100; i++ {
		id := s.NewRunID()
		if seen[id] {
			t.Fatalf("duplicate run id: %s", id)
		}
		seen[id] = true
	}
}

func TestSessionLauncherReuse(t *testing.T) {
	s := NewSession()
	s.URL = "http://127.0.0.1:9999"

	first := s.LauncherFor("run-1", "/tmp/a")
	second := s.LauncherFor("run-1", "/tmp/b")
	if first != second {
		t.Fatal("LauncherFor should reuse the launcher for a run so workers share a session dir")
	}
	if got := s.Launcher("run-1"); got != first {
		t.Fatal("Launcher should return the registered launcher")
	}
	if got := s.Launcher("missing"); got != nil {
		t.Fatal("Launcher should return nil for an unknown run")
	}

	if runs := s.ActiveRuns(); len(runs) != 1 || runs[0] != "run-1" {
		t.Fatalf("ActiveRuns = %v, want [run-1]", runs)
	}
	s.DropLauncher("run-1")
	if runs := s.ActiveRuns(); len(runs) != 0 {
		t.Fatalf("ActiveRuns after drop = %v, want empty", runs)
	}
}
