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
	planInstructions := buildPlanInstructions(runID, addr, mainAgent.Token, *prompt)
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

// buildPlanInstructions tells the coordinating agent how to decompose a
// request and create tasks via the broker API.
func buildPlanInstructions(runID, brokerURL, mainToken, prompt string) string {
	return fmt.Sprintf(`You are the COORDINATOR (planner) for Narwhal run %s.

A broker HTTP API is running at %s. Your environment has:
  NARWHAL_BROKER_URL=%s
  NARWHAL_AGENT_TOKEN=%s  (use this as the agent token in API paths)

Your job is to decompose the user's request into a task DAG.

## User Request

%s

## Steps

1. Analyze the request and identify genuinely independent work areas.
   Do NOT create tasks for trivial work. Each task should be a meaningful
   unit that a dedicated worker can complete autonomously.

2. For each task, create it via the broker API using curl:

   curl -s -X POST $NARWHAL_BROKER_URL/api/v1/run/%s/task \
     -H "Content-Type: application/json" \
     -d '{"id":"task-1","name":"auth-audit","assignment":"Analyze auth/ for security issues","deps":[]}'

   - id: unique task id (task-1, task-2, ...)
   - name: short human-readable name
   - assignment: what the worker should do (be specific — include file paths)
   - deps: task IDs this depends on (empty array for independent tasks)
   - model: (optional) claude model for this task's worker, e.g. "haiku",
     "sonnet", "opus". Omit to use the launcher default. Use a cheaper model
     for narrow investigation tasks and a stronger one for synthesis.

3. Use a synthesis task with NO deps (so it starts in parallel with the
   investigation tasks). Its assignment must state that it:
     - starts a background watcher on the radio immediately
     - drains the radio repeatedly, accumulating peer findings as they arrive
     - waits until every investigation task has called task-done before writing
       the final answer
   Set the synthesis task's "model" to "opus" — it integrates peer findings
   with fidelity, which needs frontier intelligence. Investigation tasks
   should use a cheaper model (haiku) since they are narrow.

4. After creating ALL tasks, signal completion by sending a message:

   curl -s -X POST $NARWHAL_BROKER_URL/api/v1/agents/$NARWHAL_AGENT_TOKEN/send \
     -H "Content-Type: application/json" \
     -d '{"thread_id":"planning","content":"PLAN_DONE","mentions":[],"priority":"normal"}'

5. Aim for 2-5 tasks. Too many for a small codebase is wasteful.
   Too few wastes the parallelism opportunity.

## Rules

- Do NOT analyze the codebase yourself. You are a PLANNER, not a worker.
- Keep assignments specific: mention file paths, functions, or subsystems.
- If the request is simple enough for one worker, create exactly one task.
- Do NOT create more than 5 tasks.`, runID, brokerURL, brokerURL, mainToken, prompt, runID)
}
