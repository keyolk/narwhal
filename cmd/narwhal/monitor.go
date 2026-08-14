// monitor.go wires `narwhal monitor` to the interactive TUI in
// monitor_tui.go, and handles run discovery before the TUI starts.
//
// Discovery has to look in two places. Batch runs (`narwhal run` / `plan`)
// advertise themselves in ~/.narwhal/live.json and disappear when the
// process exits. Daemon-hosted runs (spawned through MCP) live inside a
// process that outlives them, so the daemon is asked directly.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	nd "github.com/keyolk/narwhal/internal/daemon"
	"github.com/keyolk/narwhal/internal/store"
)

// daemonRunLister asks the daemon which runs it is hosting. An error means
// "no daemon runs" rather than a failure: monitoring a batch run must still
// work when the daemon was never started.
func daemonRunLister() ([]store.LiveRun, error) {
	info, err := nd.Status()
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(info.URL + "/api/v1/control/status")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("daemon status %d", resp.StatusCode)
	}
	var payload struct {
		Runs []struct {
			RunID         string `json:"run_id"`
			Prompt        string `json:"prompt"`
			CWD           string `json:"cwd"`
			StartedAt     int64  `json:"started_at"`
			State         string `json:"state"`
			Tasks         int    `json:"tasks"`
			Done          int    `json:"done"`
			Failed        int    `json:"failed"`
			Messages      int    `json:"messages"`
			ActiveWorkers int    `json:"active_workers"`
		} `json:"runs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	runs := make([]store.LiveRun, 0, len(payload.Runs))
	for _, r := range payload.Runs {
		// The daemon reports how each run is going and the picker was
		// throwing it away, so a run whose tasks had all completed still
		// read as "running" — the presence of a broker was being taken for
		// the presence of work, and the daemon holds a broker for every run
		// it has ever hosted.
		runs = append(runs, store.LiveRun{
			RunID:     r.RunID,
			BrokerURL: info.URL,
			Prompt:    r.Prompt,
			CWD:       r.CWD,
			StartedAt: r.StartedAt,
			State:     r.State,
			Tasks:     r.Tasks,
			Done:      r.Done,
			Failed:    r.Failed,
			Messages:  r.Messages,
			Running:   r.ActiveWorkers,
		})
	}
	return runs, nil
}

func monitorCmd(args []string) {
	fs := flag.NewFlagSet("monitor", flag.ExitOnError)
	runID := fs.String("run", "", "run id to monitor (default: newest live run)")
	interval := fs.Duration("interval", 1*time.Second, "poll interval")
	list := fs.Bool("list", false, "list live runs and exit")
	fs.Parse(args)

	entries := store.Discover(daemonRunLister)

	if *list {
		fmt.Print(store.SummarizeRuns(entries))
		return
	}

	if len(entries) == 0 {
		fmt.Fprintf(os.Stderr, "no live runs.\n%s", store.SummarizeRuns(entries))
		os.Exit(1)
	}

	// With an explicit --run, open it directly. Without one, open the
	// newest run but start on the picker when several are live — an
	// interactive session creates a run per request, so silently choosing
	// one of several would hide the rest.
	cur, picker := 0, len(entries) > 1
	if *runID != "" {
		found := false
		for i, e := range entries {
			if e.RunID == *runID {
				cur, picker, found = i, false, true
				break
			}
		}
		if !found {
			fmt.Fprintf(os.Stderr, "run %q is not live.\n%s", *runID, store.SummarizeRuns(entries))
			fmt.Fprintf(os.Stderr, "\nFor a finished run, use: narwhal show %s\n", *runID)
			os.Exit(1)
		}
	}

	p := tea.NewProgram(newTUIModel(entries, cur, *interval, picker), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "monitor: %v\n", err)
		os.Exit(1)
	}
}
