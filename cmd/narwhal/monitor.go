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
func daemonRunLister() (string, []string, error) {
	info, err := nd.Status()
	if err != nil {
		return "", nil, err
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(info.URL + "/api/v1/control/status")
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", nil, fmt.Errorf("daemon status %d", resp.StatusCode)
	}
	var payload struct {
		Runs []struct {
			RunID string `json:"run_id"`
		} `json:"runs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", nil, err
	}
	ids := make([]string, 0, len(payload.Runs))
	for _, r := range payload.Runs {
		ids = append(ids, r.RunID)
	}
	return info.URL, ids, nil
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

	live, ok := store.FindLiveIn(entries, *runID)
	if !ok {
		if *runID == "" {
			fmt.Fprintf(os.Stderr, "no live runs.\n%s", store.SummarizeRuns(entries))
		} else {
			fmt.Fprintf(os.Stderr, "run %q is not live.\n%s", *runID, store.SummarizeRuns(entries))
			fmt.Fprintf(os.Stderr, "\nFor a finished run, use: narwhal show %s\n", *runID)
		}
		os.Exit(1)
	}

	p := tea.NewProgram(newTUIModel(live, *interval), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "monitor: %v\n", err)
		os.Exit(1)
	}
}
