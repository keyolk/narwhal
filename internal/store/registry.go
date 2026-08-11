// registry.go tracks live Narwhal runs so a monitor in a neighbouring
// terminal can discover the broker of a run that is still in flight.
//
// The run snapshot store (store.go) only sees a run after it finishes.
// This registry closes that gap: each `narwhal run` / `narwhal plan`
// advertises its broker URL and pid on startup, and removes the entry on
// exit. Entries whose process is gone are pruned on every read, so a crash
// or kill -9 leaves no lasting garbage.
package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"syscall"
	"time"
)

// LiveRun describes a run whose broker is currently serving.
type LiveRun struct {
	RunID     string `json:"run_id"`
	PID       int    `json:"pid"`
	BrokerURL string `json:"broker_url"`
	CWD       string `json:"cwd,omitempty"`
	Prompt    string `json:"prompt,omitempty"`
	StartedAt int64  `json:"started_at"`
}

func registryPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".narwhal", "live.json")
}

// pidAlive reports whether a process exists. Signal 0 performs error
// checking without delivering a signal — the standard liveness probe.
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

func loadRegistry() []LiveRun {
	data, err := os.ReadFile(registryPath())
	if err != nil {
		return nil
	}
	var entries []LiveRun
	if json.Unmarshal(data, &entries) != nil {
		return nil
	}
	return entries
}

// pruneDead drops entries whose process has exited, newest first.
func pruneDead(entries []LiveRun) []LiveRun {
	live := make([]LiveRun, 0, len(entries))
	seen := make(map[int]bool, len(entries))
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].StartedAt > entries[j].StartedAt
	})
	for _, e := range entries {
		if !pidAlive(e.PID) || seen[e.PID] {
			continue
		}
		seen[e.PID] = true
		live = append(live, e)
	}
	return live
}

func writeRegistry(entries []LiveRun) error {
	path := registryPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// RegisterLive advertises a running broker. Best-effort: a failure only
// costs discoverability, never correctness.
func RegisterLive(e LiveRun) error {
	if e.StartedAt == 0 {
		e.StartedAt = time.Now().Unix()
	}
	entries := pruneDead(loadRegistry())
	filtered := entries[:0]
	for _, existing := range entries {
		if existing.PID != e.PID {
			filtered = append(filtered, existing)
		}
	}
	filtered = append(filtered, e)
	return writeRegistry(filtered)
}

// DeregisterLive removes the entry for pid. Called on shutdown.
func DeregisterLive(pid int) error {
	entries := pruneDead(loadRegistry())
	filtered := entries[:0]
	for _, existing := range entries {
		if existing.PID != pid {
			filtered = append(filtered, existing)
		}
	}
	return writeRegistry(filtered)
}

// ListLive returns the live runs, newest first, self-healing the file when
// pruning removed anything.
func ListLive() []LiveRun {
	raw := loadRegistry()
	live := pruneDead(raw)
	if len(live) != len(raw) {
		_ = writeRegistry(live)
	}
	return live
}

// FindLive resolves a run by id, or returns the newest live run when id is
// empty. The second value reports whether a match was found.
func FindLive(runID string) (LiveRun, bool) {
	entries := ListLive()
	if runID == "" {
		if len(entries) > 0 {
			return entries[0], true
		}
		return LiveRun{}, false
	}
	for _, e := range entries {
		if e.RunID == runID {
			return e, true
		}
	}
	return LiveRun{}, false
}

// LiveRunsSummary renders a short human-readable list for error messages.
func LiveRunsSummary() string {
	entries := ListLive()
	if len(entries) == 0 {
		return "(no live runs)"
	}
	out := ""
	for _, e := range entries {
		out += fmt.Sprintf("  %s  pid=%d  %s\n", e.RunID, e.PID, e.BrokerURL)
	}
	return out
}
