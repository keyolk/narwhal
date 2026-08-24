// plan.go implements `narwhal plan`, which runs a coordinating agent that
// decomposes a user request into a task DAG before the coordinator dispatches
// workers.
//
// The coordinator agent is itself a `ccproxy claude --print` process. It
// receives the user's prompt plus the broker URL, and is instructed to:
//
//  1. analyze the request and identify independently investigable areas
//  2. create tasks via the broker HTTP API (POST /run/<id>/task)
//  3. declare the plan complete by posting to the planning thread
//
// The main loop then runs the coordinator over the graph the planning agent
// built, rather than over a flat set of identical tasks. This is the bridge
// between "a human says what to do" and "the system figures out how to
// parallelize it" — the same role AgentRadio's Phase 2 negotiation plays,
// but centralized in a dedicated planning pass instead of distributed across
// four peer agents.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/keyolk/narwhal/internal/broker"
	"github.com/keyolk/narwhal/internal/coordinator"
	"github.com/keyolk/narwhal/internal/launcher"
	"github.com/keyolk/narwhal/internal/server"
	"github.com/keyolk/narwhal/internal/store"
)

func planCmd(args []string) {
	fs := flag.NewFlagSet("plan", flag.ExitOnError)
	prompt := fs.String("prompt", "", "task prompt for the run")
	cwd := fs.String("cwd", "", "working directory")
	timeout := fs.Duration("timeout", 30*time.Minute, "max time for the whole run")
	planTimeout := fs.Duration("plan-timeout", 5*time.Minute, "max time for the planning phase")
	maxConcurrency := fs.Int("concurrency", 3, "max parallel workers")
	plannerModel := fs.String("planner-model", "", "claude --model for the planner (default: ccproxy rotation)")
	workerModel := fs.String("worker-model", "", "claude --model for workers (default: ccproxy rotation)")
	synthesisModel := fs.String("synthesis-model", "", "claude --model for the synthesis task (default: same as --worker-model)")
	fs.Parse(args)

	if *prompt == "" {
		fmt.Fprintln(os.Stderr, "error: --prompt is required")
		os.Exit(1)
	}
	if *cwd == "" {
		*cwd, _ = os.Getwd()
	}

	runID := fmt.Sprintf("plan-%d", time.Now().UnixNano()/1e6)

	b := broker.New()
	reg := broker.NewAgentRegistry()
	mainAgent := reg.Register("main", runID, true)
	r := b.CreateRun(runID, *prompt, *cwd, mainAgent.ID)

	srv := server.New(b, reg)
	addr, err := srv.Start()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: start broker: %v\n", err)
		os.Exit(1)
	}
	defer srv.Shutdown()

	// Advertise this run so `narwhal monitor` can attach while it runs.
	_ = store.RegisterLive(store.LiveRun{
		RunID:     runID,
		PID:       os.Getpid(),
		BrokerURL: addr,
		CWD:       *cwd,
		Prompt:    *prompt,
	})
	defer store.DeregisterLive(os.Getpid())

	fmt.Fprintf(os.Stderr, "[narwhal] plan %s started\n", runID)
	fmt.Fprintf(os.Stderr, "[narwhal] broker: %s\n", addr)
	fmt.Fprintf(os.Stderr, "[narwhal] cwd: %s\n", *cwd)
	fmt.Fprintf(os.Stderr, "[narwhal] monitor: narwhal monitor --run %s\n", runID)

	// Radio threads available to both the planning agent and workers.
	r.CreateStandardThreads()

	// Phase 1: run the coordinating agent to decompose the request into a DAG.
	// The agent talks to the broker HTTP API to create tasks with deps.
	planInstructions := server.PlanInstructionsFor(runID, addr, mainAgent.Token, *prompt, *cwd)
	planDone := make(chan error, 1)
	planArgs := []string{"claude", "--print",
		"--permission-mode", "bypassPermissions",
		"--append-system-prompt", planInstructions,
	}
	if *plannerModel != "" {
		planArgs = append(planArgs, "--model", *plannerModel)
	}
	planArgs = append(planArgs, "Decompose the user's request into a task DAG and create the tasks via the broker API. When done, post PLAN_DONE to the planning thread.")
	planCmd := exec.Command("ccproxy", planArgs...)
	planCmd.Dir = *cwd
	planLog, _ := os.Create(filepath.Join(launcher.New(addr, runID, *cwd).SessionDir(), "planner-output.txt"))
	planCmd.Stdout = planLog
	planCmd.Stderr = planLog
	planCmd.Env = append(os.Environ(),
		"NARWHAL_RUN_ID="+runID,
		"NARWHAL_BROKER_URL="+addr,
		"NARWHAL_AGENT_TOKEN="+mainAgent.Token,
	)
	if err := planCmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "error: start planner: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "[narwhal] planner launched\n")

	go func() {
		planDone <- planCmd.Wait()
		planLog.Close()
	}()

	select {
	case err := <-planDone:
		if err != nil {
			fmt.Fprintf(os.Stderr, "[narwhal] planner exited: %v\n", err)
		} else {
			fmt.Fprintf(os.Stderr, "[narwhal] planner completed\n")
		}
	case <-time.After(*planTimeout):
		fmt.Fprintf(os.Stderr, "[narwhal] planner timed out\n")
		if planCmd.Process != nil {
			_ = planCmd.Process.Kill()
		}
	}

	// Report what the planner created.
	tasks := r.SnapshotTasks()
	fmt.Fprintf(os.Stderr, "[narwhal] planner created %d tasks\n", len(tasks))
	for _, t := range tasks {
		fmt.Fprintf(os.Stderr, "  %s  state=%s  deps=%v\n", t.ID, t.State, t.Deps)
	}

	if len(tasks) == 0 {
		fmt.Fprintf(os.Stderr, "[narwhal] no tasks created; aborting\n")
		os.Exit(1)
	}

	// Phase 2: run the coordinator over the planner's DAG.
	l := launcher.New(addr, runID, *cwd)
	l.SetWorkerModel(*workerModel)
	cfg := coordinator.DefaultConfig()
	cfg.MaxConcurrency = *maxConcurrency
	cfg.Timeout = *timeout
	// The synthesis task integrates every peer finding with fidelity —
	// that needs frontier intelligence even when narrow investigation
	// does not. Override the worker model for the synthesis task only.
	if *synthesisModel != "" {
		cfg.SynthesisModel = *synthesisModel
	}

	coord := coordinator.New(r, reg, l, cfg)
	res := coord.Run()

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
