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

	"github.com/keyolk/narwhal/internal/broker"
)

// LiveRun describes a run whose broker is currently serving.
type LiveRun struct {
	RunID     string `json:"run_id"`
	PID       int    `json:"pid"`
	BrokerURL string `json:"broker_url"`
	CWD       string `json:"cwd,omitempty"`
	Prompt    string `json:"prompt,omitempty"`
	StartedAt int64  `json:"started_at"`

	// Outcome, for the picker. A list of runs that says only when and where
	// makes you open each one to learn whether it worked — which is the
	// question you had before opening anything.
	State    string `json:"state,omitempty"`
	Tasks    int    `json:"tasks,omitempty"`
	Done     int    `json:"done,omitempty"`
	Failed   int    `json:"failed,omitempty"`
	Running  int    `json:"running,omitempty"`
	Messages int    `json:"messages,omitempty"`
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
//
// This covers batch runs only. A run hosted by the daemon (spawned through
// MCP) is not in this file — the daemon owns those, and duplicating them
// here would mean two places to keep in sync. Callers that need to see
// every live run should use Discover instead.
func ListLive() []LiveRun {
	raw := loadRegistry()
	live := pruneDead(raw)
	if len(live) != len(raw) {
		_ = writeRegistry(live)
	}
	return live
}

// DaemonRunLister reports the runs a daemon is currently hosting. The
// concrete implementation lives in the caller to avoid an import cycle
// (daemon depends on store, not the other way around).
//
// It returns whole LiveRun values rather than bare ids: a run is identified
// to the operator by its prompt and working directory, and a picker that
// only has ids cannot tell six runs apart.
type DaemonRunLister func() (runs []LiveRun, err error)

// Discover returns every run worth showing: batch runs from the registry
// file, runs hosted by the daemon, and finished runs read back from disk.
//
// Batch runs and daemon runs are advertised differently on purpose. A batch
// run owns its broker for the length of one command, so a file entry that
// disappears when the process exits is exactly right. Daemon runs come and
// go inside a process that outlives all of them, so the daemon is the
// authority and gets asked directly.
//
// Finished runs come from the snapshots on disk. Without them the monitor
// showed only what was running this second: 25 runs sat in ~/.narwhal/runs
// readable by `narwhal show` and by nothing else, and the daemon's own
// memory of retired runs died with the process. A monitor that forgets
// everything the moment it finishes cannot answer "what did that run do",
// which is most of what you want a monitor for.
func Discover(lister DaemonRunLister) []LiveRun {
	out := ListLive()
	seen := make(map[string]bool, len(out))
	for _, e := range out {
		seen[e.RunID] = true
	}

	if lister != nil {
		if daemonRuns, err := lister(); err == nil {
			for _, r := range daemonRuns {
				if seen[r.RunID] {
					continue
				}
				seen[r.RunID] = true
				// PID stays zero: it would be the daemon's, not a
				// run-owned process, and callers must not mistake it for
				// one.
				out = append(out, r)
			}
		}
	}

	for _, r := range listFinished(seen) {
		out = append(out, r)
	}

	// Newest first, and stable: the monitor re-discovers every second, so
	// an order that shifts between polls makes the picker flicker and moves
	// rows out from under the cursor.
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i].StartedAt, out[j].StartedAt
		if a != b {
			return a > b
		}
		return out[i].RunID > out[j].RunID
	})
	return out
}

// MaxFinishedRuns caps how many finished runs the picker carries.
//
// The directory grows without bound and the list is meant to be read at a
// glance; a hundred rows of last month is not history, it is noise. Older
// runs stay on disk for `narwhal show`.
const MaxFinishedRuns = 20

// listFinished reads persisted runs that are not already in the live set.
//
// BrokerURL is deliberately empty: there is no process to poll. That is
// the flag callers use to tell a finished run from a live one, and to read
// its state from the snapshot instead.
func listFinished(seen map[string]bool) []LiveRun {
	ids, err := ListRuns()
	if err != nil {
		return nil
	}
	var out []LiveRun
	for _, id := range ids {
		if seen[id] {
			continue
		}
		if len(out) >= MaxFinishedRuns {
			break
		}
		snap, err := LoadRun(id)
		if err != nil {
			continue
		}
		e := LiveRun{
			RunID:     id,
			Prompt:    snap.Prompt,
			CWD:       snap.CWD,
			StartedAt: snap.StartedAt,
			State:     string(snap.State),
			Tasks:     len(snap.Tasks),
			Messages:  len(snap.Messages),
		}
		for _, t := range snap.Tasks {
			switch t.State {
			case broker.TaskCompleted:
				e.Done++
			case broker.TaskFailed:
				e.Failed++
			case broker.TaskDispatched:
				e.Running++
			}
		}
		out = append(out, e)
	}
	return out
}

// FindLiveIn resolves a run by id from a caller-supplied set, or returns
// the newest when runID is empty.
func FindLiveIn(entries []LiveRun, runID string) (LiveRun, bool) {
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

// FindLive resolves a batch run by id, or returns the newest batch run when
// runID is empty. Daemon-hosted runs are not covered; use Discover +
// FindLiveIn for that.
func FindLive(runID string) (LiveRun, bool) {
	return FindLiveIn(ListLive(), runID)
}

// SummarizeRuns renders a short human-readable list for error messages.
func SummarizeRuns(entries []LiveRun) string {
	if len(entries) == 0 {
		return "(no live runs)"
	}
	out := ""
	for _, e := range entries {
		if e.PID > 0 {
			out += fmt.Sprintf("  %s  pid=%d  %s\n", e.RunID, e.PID, e.BrokerURL)
			continue
		}
		out += fmt.Sprintf("  %s  (daemon)  %s\n", e.RunID, e.BrokerURL)
	}
	return out
}

// LiveRunsSummary renders the batch-run list. Kept for callers that have no
// daemon lister at hand.
func LiveRunsSummary() string {
	return SummarizeRuns(ListLive())
}
