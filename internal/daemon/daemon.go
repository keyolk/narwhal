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
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
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

// ClearState removes the pid/port/url/version files on shutdown.
//
// The version goes with the rest: a file left behind by a dead daemon
// would answer for the next one, and the staleness check would compare
// the installed binary against a daemon that is not there.
func ClearState() {
	os.Remove(pidFilePath())
	os.Remove(portFilePath())
	os.Remove(urlFilePath())
	os.Remove(versionFilePath())
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
//
// It refuses while workers are still running unless force is set. Stopping
// the broker does not stop the workers — they are detached processes that
// keep going — so a stop mid-run leaves them alive with nowhere to report:
// their task-done calls hit a closed port and the work vanishes even though
// the files were written. That happened to a four-worker run, triggered by
// a routine `make daemon-restart` in another terminal, which is exactly how
// this will happen again.
func Stop(force bool) error {
	info, err := Status()
	if err != nil {
		return err
	}
	if !force {
		if busy, err := activeWorkers(info.URL); err != nil {
			// Could not ask. Say so rather than guessing either way: a
			// silent stop is the failure this exists to prevent, and a
			// silent refusal would strand a wedged daemon.
			return fmt.Errorf("cannot tell whether workers are running (%w); "+
				"use --force to stop anyway", err)
		} else if busy > 0 {
			return fmt.Errorf("%d worker(s) still running; they would keep going "+
				"with no broker to report to. Wait, or use --force", busy)
		}
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

// activeWorkers asks the running daemon how many workers are in flight.
//
// This has to go over HTTP: `narwhal daemon stop` is a separate process
// from the daemon and shares no memory with it.
func activeWorkers(baseURL string) (int, error) {
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(baseURL + "/api/v1/control/status")
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("broker returned %d", resp.StatusCode)
	}
	var payload struct {
		Runs []struct {
			ActiveWorkers int `json:"active_workers"`
		} `json:"runs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return 0, err
	}
	n := 0
	for _, r := range payload.Runs {
		n += r.ActiveWorkers
	}
	return n, nil
}

// StatusJSONFor is StatusJSON plus the staleness verdict against the
// binary asking. The caller passes its own version because only it knows
// what build it is; the daemon package must not import main.
func StatusJSONFor(current string) ([]byte, error) {
	info, err := Status()
	if err != nil {
		return json.Marshal(map[string]any{"running": false, "error": err.Error()})
	}
	out := map[string]any{"running": true, "pid": info.PID, "url": info.URL}
	stale, running := Stale(current)
	if running != "" {
		out["version"] = running
	}
	if stale {
		out["stale"] = true
		// Named rather than left to be inferred from two version
		// strings: the reader has to know what to do, and what to do is
		// restart the daemon.
		out["hint"] = "the running daemon is build " + orUnknown(running) +
			" but " + current + " is installed; run `make daemon-restart`" +
			" or runs will keep being served by the old code"
	}
	return json.Marshal(out)
}

func orUnknown(v string) string {
	if v == "" {
		return "(unstamped, predating this field)"
	}
	return v
}

// StatusJSON renders the daemon status for machine consumption.
func StatusJSON() ([]byte, error) {
	info, err := Status()
	if err != nil {
		return json.Marshal(map[string]any{"running": false, "error": err.Error()})
	}
	out := map[string]any{"running": true, "pid": info.PID, "url": info.URL}
	if v := RunningVersion(); v != "" {
		out["version"] = v
	}
	return json.Marshal(out)
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
