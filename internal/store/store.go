// Package store persists run state to disk so `narwhal show` can read it
// after the process that created the run has exited.
//
// The store is append-only and JSON-based: each run is a single file under
// ~/.narwhal/runs/<run-id>.json containing a snapshot written at the end of
// the run (or periodically during long runs). This is deliberately simple —
// the first goal is making run state inspectable, not building a full
// event-sourced database.
package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/keyolk/narwhal/internal/broker"
)

// RunsDir returns the directory where run snapshots are stored.
func RunsDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".narwhal", "runs")
}

// SaveRun writes a run snapshot to disk atomically. The file is created
// with 0600 permissions so agent tokens and message contents stay private.
func SaveRun(s broker.Snapshot) error {
	dir := RunsDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create runs dir: %w", err)
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal snapshot: %w", err)
	}
	path := filepath.Join(dir, s.RunID+".json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

// LoadRun reads a run snapshot from disk. Returns an error if the file
// does not exist or is unreadable.
func LoadRun(runID string) (broker.Snapshot, error) {
	path := filepath.Join(RunsDir(), runID+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return broker.Snapshot{}, fmt.Errorf("read run %s: %w", runID, err)
	}
	var s broker.Snapshot
	if err := json.Unmarshal(data, &s); err != nil {
		return broker.Snapshot{}, fmt.Errorf("parse run %s: %w", runID, err)
	}
	return s, nil
}

// ListRuns returns the run IDs of all persisted snapshots, newest first
// (by file modification time).
func ListRuns() ([]string, error) {
	dir := RunsDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	type entry struct {
		id  string
		mod int64
	}
	var items []entry
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		id := e.Name()[:len(e.Name())-len(".json")]
		info, err := e.Info()
		if err != nil {
			continue
		}
		items = append(items, entry{id: id, mod: info.ModTime().Unix()})
	}
	// Sort by modification time, newest first.
	for i := 0; i < len(items); i++ {
		for j := i + 1; j < len(items); j++ {
			if items[j].mod > items[i].mod {
				items[i], items[j] = items[j], items[i]
			}
		}
	}
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.id
	}
	return out, nil
}
