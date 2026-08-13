// Command narwhal is the CLI entrypoint for the Narwhal multi-agent runtime.
//
// Usage:
//
//	narwhal run --workers auto --prompt "analyze this repo's auth flow"
//	narwhal run --workers 3 --prompt "..." --cwd ~/src/myrepo
//	narwhal show <run-id>
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/keyolk/narwhal/internal/broker"
	"github.com/keyolk/narwhal/internal/coordinator"
	"github.com/keyolk/narwhal/internal/launcher"
	"github.com/keyolk/narwhal/internal/server"
	"github.com/keyolk/narwhal/internal/store"
)

// version is stamped at build time by the Makefile:
//
//	-ldflags "-X main.version=$(git describe --tags --always --dirty)"
//
// A plain `go build` leaves it as "dev", which is the honest answer for a
// binary built without that stamp.
var version = "dev"

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "run":
		runCmd(os.Args[2:])
	case "plan":
		planCmd(os.Args[2:])
	case "show":
		showCmd(os.Args[2:])
	case "monitor":
		monitorCmd(os.Args[2:])
	case "daemon":
		daemonCmd(os.Args[2:])
	case "mcp":
		mcpCmd(os.Args[2:])
	case "experiment":
		experimentCmd(os.Args[2:])
	case "version":
		fmt.Println("narwhal " + version)
	case "-h", "--help", "help":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func runCmd(args []string) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	prompt := fs.String("prompt", "", "task prompt for the run")
	workers := fs.String("workers", "auto", "number of workers (auto or a number)")
	cwd := fs.String("cwd", "", "working directory (defaults to current dir)")
	timeout := fs.Duration("timeout", 30*time.Minute, "max time to wait for workers")
	fs.Parse(args)

	if *prompt == "" {
		fmt.Fprintln(os.Stderr, "error: --prompt is required")
		os.Exit(1)
	}

	if *cwd == "" {
		*cwd, _ = os.Getwd()
	}

	runID := generateRunID()

	b := broker.New()
	reg := broker.NewAgentRegistry()

	// Create the run with "main" as the coordinator.
	mainAgent := reg.Register("main", runID, true)
	r := b.CreateRun(runID, *prompt, *cwd, mainAgent.ID)

	// Start the broker HTTP server.
	srv := server.New(b, reg)
	addr, err := srv.Start()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: start broker: %v\n", err)
		os.Exit(1)
	}
	defer srv.Shutdown()

	// Advertise this run so `narwhal monitor` in another terminal can find
	// its broker while the run is still in flight.
	_ = store.RegisterLive(store.LiveRun{
		RunID:     runID,
		PID:       os.Getpid(),
		BrokerURL: addr,
		CWD:       *cwd,
		Prompt:    *prompt,
	})
	defer store.DeregisterLive(os.Getpid())

	fmt.Fprintf(os.Stderr, "[narwhal] run %s started\n", runID)
	fmt.Fprintf(os.Stderr, "[narwhal] broker: %s\n", addr)
	fmt.Fprintf(os.Stderr, "[narwhal] cwd: %s\n", *cwd)
	fmt.Fprintf(os.Stderr, "[narwhal] monitor: narwhal monitor --run %s\n", runID)

	workerCount := 1
	if *workers != "auto" {
		fmt.Sscanf(*workers, "%d", &workerCount)
	}

	l := launcher.New(addr, runID, *cwd)

	// Radio threads: planning carries decisions, worklog carries in-flight
	// findings. Keeping them separate mirrors AgentRadio's protocol so
	// decision traffic does not drown out live discovery sharing.
	r.CreateStandardThreads()

	// Phase 2: the caller declares a flat set of independent tasks. A
	// coordinating agent will later build a real DAG and add tasks mid-run
	// via split-request; the coordinator loop already handles both.
	for i := 1; i <= workerCount; i++ {
		taskID := fmt.Sprintf("task-%d", i)
		r.AddTask(taskID, fmt.Sprintf("worker-%d", i), *prompt, nil)
	}

	cfg := coordinator.DefaultConfig()
	cfg.Timeout = *timeout
	if workerCount < cfg.MaxConcurrency {
		cfg.MaxConcurrency = workerCount
	}

	coord := coordinator.New(r, reg, l, cfg)
	res := coord.Run()

	// Persist the final snapshot so `narwhal show` can read it after exit.
	if err := store.SaveRun(r.Snapshot()); err != nil {
		fmt.Fprintf(os.Stderr, "[narwhal] warning: save run: %v\n", err)
	}

	fmt.Fprintf(os.Stderr, "[narwhal] completed=%d failed=%d unreached=%d timed_out=%v\n",
		len(res.Completed), len(res.Failed), len(res.Unreached), res.TimedOut)

	out := map[string]any{
		"result":      res,
		"snapshot":    r.Snapshot(),
		"session_dir": l.SessionDir(),
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(out)
}

func showCmd(args []string) {
	if len(args) < 1 {
		// No run-id: list recent runs.
		ids, err := store.ListRuns()
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: list runs: %v\n", err)
			os.Exit(1)
		}
		if len(ids) == 0 {
			fmt.Println("(no persisted runs)")
			return
		}
		fmt.Printf("Recent runs (newest first):\n")
		for _, id := range ids {
			s, err := store.LoadRun(id)
			if err != nil {
				fmt.Printf("  %s  (unreadable: %v)\n", id, err)
				continue
			}
			completed := 0
			failed := 0
			for _, t := range s.Tasks {
				switch t.State {
				case broker.TaskCompleted:
					completed++
				case broker.TaskFailed:
					failed++
				}
			}
			msgCount := 0
			if s.Messages != nil {
				msgCount = len(s.Messages)
			}
			fmt.Printf("  %s  state=%s tasks=%d (done=%d fail=%d) msgs=%d\n",
				id, s.State, len(s.Tasks), completed, failed, msgCount)
		}
		return
	}

	s, err := store.LoadRun(args[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(s)
}

func generateRunID() string {
	return fmt.Sprintf("run-%d", time.Now().UnixNano()/1e6)
}

func printUsage() {
	fmt.Fprintln(os.Stderr, `narwhal — graph-engineered multi-agent runtime with passive awareness

Usage:
  narwhal run   --prompt "..." [--workers N] [--cwd DIR] [--timeout DUR]
                Flat parallel workers on one prompt.

  narwhal plan  --prompt "..." [--cwd DIR] [--concurrency N] [--timeout DUR]
                A planner agent decomposes the request into a task DAG,
                then the coordinator dispatches workers in dependency order.

  narwhal monitor [--run ID] [--interval DUR] [--no-color]
                Live view of a running run: DAG progress and radio traffic.
                Defaults to the newest live run.

  narwhal show  [run-id]
                List finished runs, or print one run's full snapshot.

  narwhal daemon <start|stop|status>
                Long-lived broker for interactive use. Started on demand by
                the MCP server; you rarely need to run this yourself.

  narwhal mcp   [--no-auto-start]
                MCP server over stdio. Register it with Claude Code:
                  claude mcp add --scope user narwhal narwhal mcp

  narwhal experiment [--cwd DIR]
                Two-worker passive-awareness validation scenario.

  narwhal version

Common options:
  --prompt       task prompt (required for run/plan)
  --cwd          working directory (default: current directory)
  --timeout      max wait time for the run (default: 30m)
  --workers      worker count for run (default: 1)
  --concurrency  max parallel workers for plan (default: 3)`)
}
