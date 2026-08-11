// Package daemon runs the Narwhal broker as a long-lived process so an
// interactive Claude Code session can spawn workers, collect their findings,
// and spawn more — across many user turns — instead of one shot per CLI
// invocation.
//
// The `narwhal run` / `narwhal plan` commands own their broker and tear it
// down when the run finishes. That is right for batch use and wrong for
// interactive use: the user's session outlives any single dispatch, so the
// broker has to as well.
//
// Single-instance enforcement uses flock on the pidfile rather than a bare
// pid check. A stale pidfile left by kill -9 would otherwise let a second
// daemon start and bind a different port, silently splitting state across
// two brokers. The lock is held for the process lifetime and released by
// the OS on exit, so it cannot go stale.
package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// StateDir is the root for daemon state files.
func StateDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".narwhal")
}

func pidFilePath() string  { return filepath.Join(StateDir(), "daemon.pid") }
func portFilePath() string { return filepath.Join(StateDir(), "daemon.port") }
func urlFilePath() string  { return filepath.Join(StateDir(), "daemon.url") }

// Info describes a running daemon.
type Info struct {
	PID int    `json:"pid"`
	URL string `json:"url"`
}

// AcquireLock takes an exclusive flock on the pidfile. The returned file
// must stay open for the daemon's lifetime — closing it releases the lock.
// Returns an error if another daemon already holds it.
func AcquireLock() (*os.File, error) {
	path := pidFilePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if existing, readErr := readPID(); readErr == nil && existing > 0 {
			return nil, fmt.Errorf("daemon already running (pid %d)", existing)
		}
		return nil, fmt.Errorf("daemon already running")
	}
	return f, nil
}

// WriteState records the daemon's pid and broker URL. The pid is written
// in place through the locked file handle so the inode — and therefore the
// flock — survives.
func WriteState(lockFile *os.File, pid int, url string) error {
	if err := lockFile.Truncate(0); err != nil {
		return err
	}
	if _, err := lockFile.Seek(0, 0); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(lockFile, "%d", pid); err != nil {
		return err
	}
	if err := lockFile.Sync(); err != nil {
		return err
	}

	port := ""
	if idx := strings.LastIndex(url, ":"); idx >= 0 {
		port = url[idx+1:]
	}
	if err := writeFileAtomic(portFilePath(), []byte(port), 0o600); err != nil {
		return err
	}
	return writeFileAtomic(urlFilePath(), []byte(url), 0o600)
}

// ClearState removes the pid/port/url files on shutdown.
func ClearState() {
	os.Remove(pidFilePath())
	os.Remove(portFilePath())
	os.Remove(urlFilePath())
}

func readPID() (int, error) {
	data, err := os.ReadFile(pidFilePath())
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(data)))
}

// URL returns the running daemon's broker URL, or an error when no daemon
// is running.
func URL() (string, error) {
	info, err := Status()
	if err != nil {
		return "", err
	}
	return info.URL, nil
}

// Status reports the running daemon, or an error when none is alive.
//
// Liveness is decided by trying to take the flock, not by signalling the
// pid: a recycled pid would make a signal check report a dead daemon as
// alive. If we can take the lock, whoever wrote the pidfile is gone.
func Status() (Info, error) {
	pid, err := readPID()
	if err != nil {
		return Info{}, fmt.Errorf("daemon not running")
	}
	if !lockHeld() {
		return Info{}, fmt.Errorf("daemon not running (stale pidfile for pid %d)", pid)
	}
	data, err := os.ReadFile(urlFilePath())
	if err != nil {
		return Info{}, fmt.Errorf("daemon running (pid %d) but url file unreadable: %w", pid, err)
	}
	return Info{PID: pid, URL: strings.TrimSpace(string(data))}, nil
}

// lockHeld reports whether some process holds the pidfile lock. EWOULDBLOCK
// means a live daemon owns it; acquiring it ourselves means it does not.
func lockHeld() bool {
	f, err := os.OpenFile(pidFilePath(), os.O_RDONLY, 0o600)
	if err != nil {
		return false
	}
	defer f.Close()
	err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err == syscall.EWOULDBLOCK {
		return true
	}
	if err == nil {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	}
	return false
}

// Stop signals the running daemon to shut down.
func Stop() error {
	info, err := Status()
	if err != nil {
		return err
	}
	proc, err := os.FindProcess(info.PID)
	if err != nil {
		return fmt.Errorf("find process %d: %w", info.PID, err)
	}
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		return fmt.Errorf("signal process %d: %w", info.PID, err)
	}
	return nil
}

// StatusJSON renders the daemon status for machine consumption.
func StatusJSON() ([]byte, error) {
	info, err := Status()
	if err != nil {
		return json.Marshal(map[string]any{"running": false, "error": err.Error()})
	}
	return json.Marshal(map[string]any{"running": true, "pid": info.PID, "url": info.URL})
}

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
