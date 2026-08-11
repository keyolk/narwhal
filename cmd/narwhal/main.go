// Command narwhal is the CLI entrypoint for the Narwhal multi-agent runtime.
//
// Usage:
//
//	narwhal run --workers auto --prompt "analyze this repo's auth flow"
//	narwhal run --workers 3 --prompt "..." --cwd ~/src/sendbird/ccproxy
//	narwhal show <run-id>
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/keyolk/narwhal/internal/broker"
	"github.com/keyolk/narwhal/internal/launcher"
	"github.com/keyolk/narwhal/internal/server"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "run":
		runCmd(os.Args[2:])
	case "show":
		showCmd(os.Args[2:])
	case "experiment":
		experimentCmd(os.Args[2:])
	case "version":
		fmt.Println("narwhal dev")
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

	fmt.Fprintf(os.Stderr, "[narwhal] run %s started\n", runID)
	fmt.Fprintf(os.Stderr, "[narwhal] broker: %s\n", addr)
	fmt.Fprintf(os.Stderr, "[narwhal] cwd: %s\n", *cwd)
	fmt.Fprintf(os.Stderr, "[narwhal] prompt: %s\n", *prompt)

	// For phase 1: the coordinator (main) decomposes the task into workers.
	// In a real run, main would be a Claude Code session that calls
	// spawn_worker via MCP tools. For now we create a single worker to
	// validate the end-to-end path.
	workerCount := 1
	if *workers != "auto" {
		fmt.Sscanf(*workers, "%d", &workerCount)
	}

	l := launcher.New(addr, runID, *cwd)

	// Create a planning thread and a worklog thread.
	r.CreateThread("planning", "planning",
		[]string{"main"})
	r.CreateThread("worklog", "worklog",
		[]string{"main"})

	for i := 1; i <= workerCount; i++ {
		agentID := fmt.Sprintf("worker-%d", i)
		taskID := fmt.Sprintf("task-%d", i)

		// Register the worker agent.
		agent := reg.Register(agentID, runID, false)

		// Create a task for this worker.
		r.AddTask(taskID, agentID, *prompt, nil)

		// Set up the agent workspace and wrapper scripts.
		cfg := launcher.WorkerConfig{
			AgentID:    agentID,
			TaskID:     taskID,
			Assignment: *prompt,
		}
		agentDir, err := l.SetupAgent(agent, cfg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[narwhal] setup %s: %v\n", agentID, err)
			continue
		}

		// Launch the worker as ccproxy claude --print.
		if err := l.Launch(agentDir, cfg); err != nil {
			fmt.Fprintf(os.Stderr, "[narwhal] launch %s: %v\n", agentID, err)
			continue
		}
		fmt.Fprintf(os.Stderr, "[narwhal] launched %s (task %s)\n", agentID, taskID)
	}

	// Wait for all workers to finish.
	if err := l.Wait(*timeout); err != nil {
		fmt.Fprintf(os.Stderr, "[narwhal] %v\n", err)
	}

	// Print the final run state.
	snap := r.Snapshot()
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(snap)
}

func showCmd(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: narwhal show <run-id>")
		os.Exit(1)
	}
	// Phase 1: show reads from the in-memory broker which is gone after the
	// process exits. A later phase will persist run state to disk.
	fmt.Fprintf(os.Stderr, "note: in-memory broker; show is only useful within a running session\n")
	fmt.Fprintf(os.Stderr, "run-id: %s\n", args[0])
}

func generateRunID() string {
	return fmt.Sprintf("run-%d", time.Now().UnixNano()/1e6)
}

func printUsage() {
	fmt.Fprintln(os.Stderr, `narwhal — graph-engineered multi-agent runtime with passive awareness

Usage:
  narwhal run --prompt "..." [--workers auto|N] [--cwd DIR] [--timeout DURATION]
  narwhal show <run-id>
  narwhal version

Options:
  --prompt     task prompt for the run (required for run)
  --workers    number of workers: "auto" or a number (default: auto)
  --cwd        working directory (default: current directory)
  --timeout    max wait time for workers (default: 30m)`)
}
