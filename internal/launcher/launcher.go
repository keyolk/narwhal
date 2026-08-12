// Package launcher manages the lifecycle of worker Claude Code processes.
//
// Each worker is started via `ccproxy claude --print`, which preserves the
// full Claude Code tool surface — including background Bash task completion
// notifications, the mechanism that makes AgentRadio-style passive awareness
// work in a directly-executed process (as opposed to a Workflow subagent,
// where our experiments showed notifications are not delivered).
//
// The launcher:
//   - generates per-agent wrapper scripts (send/drain/watch/state)
//   - sets up a per-agent working directory under ~/.narwhal/sessions/<run>/<agent>/
//   - injects the agent identity and broker URL via environment variables
//   - execs ccproxy claude --print with the agent-specific instructions
//   - captures stdout/stderr to per-agent log files
package launcher

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/keyolk/narwhal/internal/broker"
)

// WorkerConfig describes one worker to launch.
type WorkerConfig struct {
	AgentID       string
	TaskID        string
	Assignment    string
	IsCoordinator bool
	Model         string // --model flag for claude; empty = ccproxy default
}

// Launcher owns worker process lifecycle for a Run.
type Launcher struct {
	brokerURL   string
	runID       string
	cwd         string
	sessionDir  string
	workerModel string // default --model for workers when WorkerConfig.Model is empty

	mu      sync.Mutex
	workers map[string]*exec.Cmd
}

// New creates a Launcher for the given run.
func New(brokerURL, runID, cwd string) *Launcher {
	sessionDir := filepath.Join(homeDir(), ".narwhal", "sessions", runID)
	return &Launcher{
		brokerURL:  brokerURL,
		runID:      runID,
		cwd:        cwd,
		sessionDir: sessionDir,
		workers:    make(map[string]*exec.Cmd),
	}
}

// SetWorkerModel sets the default --model passed to worker processes. Pass
// "" to use ccproxy's account rotation. This is the Cursor economics insight:
// a frontier planner decomposes, a cheaper model executes — quality stays
// close while worker cost drops by an order of magnitude.
func (l *Launcher) SetWorkerModel(model string) { l.workerModel = model }

// SessionDir returns the on-disk directory for this run's agent workspaces.
func (l *Launcher) SessionDir() string { return l.sessionDir }

// SetupAgent creates the per-agent working directory and writes the wrapper
// scripts that let the Claude Code worker talk to the broker. The agent's
// token is baked into each wrapper so the model never handles the URL.
func (l *Launcher) SetupAgent(a *broker.Agent, cfg WorkerConfig) (string, error) {
	agentDir := filepath.Join(l.sessionDir, "agents", a.ID)
	scriptsDir := filepath.Join(agentDir, "scripts")
	if err := os.MkdirAll(scriptsDir, 0o700); err != nil {
		return "", fmt.Errorf("create agent dir: %w", err)
	}

	// Wrapper scripts: each one hits the broker endpoint with the agent's
	// token baked in. The model calls these by simple name, never seeing
	// the URL or token.
	base := fmt.Sprintf("%s/api/v1/agents/%s", l.brokerURL, a.Token)

	scripts := map[string]string{
		"send": fmt.Sprintf(`#!/bin/bash
# usage: send <threadId> <content> [mentionsCSV] [priority]
# Send a radio message. Sender identity is baked into this script.
set -euo pipefail
THREAD="$1"; CONTENT="$2"; MENTIONS="${3:-}"; PRIO="${4:-normal}"
# A bare priority in the mentions slot is what a caller writing
#   send worklog "..." urgent
# means — the alternative reading, an @-mention of an agent named "urgent",
# addresses a peer that cannot exist. Left alone this silently narrows the
# message to a nonexistent recipient and peers never see it, which is exactly
# the failure that is hardest to notice from the sending side.
case "$MENTIONS" in
  fyi|normal|urgent)
    if [ $# -lt 4 ]; then PRIO="$MENTIONS"; MENTIONS=""; fi ;;
esac
curl -s -X POST %s/send \
  -H "Content-Type: application/json" \
  -d "$(python3 -c 'import json,sys; print(json.dumps({"thread_id":sys.argv[1],"content":sys.argv[2],"mentions":sys.argv[3].split(",") if sys.argv[3] else [],"priority":sys.argv[4]}))' "$THREAD" "$CONTENT" "$MENTIONS" "$PRIO")"
`, base),
		"drain": fmt.Sprintf(`#!/bin/bash
# usage: drain [afterSeq]
# Non-blocking check for new radio messages mentioning this agent.
set -euo pipefail
AFTER="${1:-0}"
curl -s -X POST %s/drain \
  -H "Content-Type: application/json" \
  -d "$(python3 -c 'import json,sys; print(json.dumps({"after":int(sys.argv[1])}))' "$AFTER")"
`, base),
		"watch": fmt.Sprintf(`#!/bin/bash
# usage: watch [afterSeq] [timeoutMs]
# Long-poll for messages. Run as a BACKGROUND Bash task so Claude Code
# delivers a completion notification when a message arrives — this is
# the AgentRadio passive-awareness mechanism.
set -euo pipefail
AFTER="${1:-0}"; TIMEOUT="${2:-60000}"
curl -s -X POST %s/watch \
  -H "Content-Type: application/json" \
  -d "$(python3 -c 'import json,sys; print(json.dumps({"after":int(sys.argv[1]),"timeout_ms":int(sys.argv[2])}))' "$AFTER" "$TIMEOUT")"
`, base),
		"state": fmt.Sprintf(`#!/bin/bash
# usage: state
# Print the full run state visible to this agent.
curl -s %s/state
`, base),
		"task-done": fmt.Sprintf(`#!/bin/bash
# usage: task-done <taskId> <outcome>
# Mark a task as completed with the given outcome text.
set -euo pipefail
TASK="$1"; OUTCOME="$2"
curl -s -X POST %s/task/$TASK/done \
  -H "Content-Type: application/json" \
  -d "$(python3 -c 'import json,sys; print(json.dumps({"outcome":sys.argv[1]}))' "$OUTCOME")"
`, base),
		"split": fmt.Sprintf(`#!/bin/bash
# usage: split "<taskId>" "<name>" "<assignment>" [depsCSV]
# Request the coordinator to add a new task to the run. Existing tasks
# are immutable; this is the only way the graph grows mid-run.
set -euo pipefail
TASK="$1"; NAME="$2"; ASSIGN="$3"; DEPS="${4:-}"
# Build a SPLIT_REQUEST message body and post it to the planning thread.
BODY="SPLIT_REQUEST|$TASK|$NAME|$ASSIGN|$DEPS"
curl -s -X POST %s/send \
  -H "Content-Type: application/json" \
  -d "$(python3 -c 'import json,sys; print(json.dumps({"thread_id":"planning","content":sys.argv[1],"mentions":[],"priority":"normal"}))' "$BODY")"
`, base),
	}

	for name, content := range scripts {
		path := filepath.Join(scriptsDir, name)
		if err := os.WriteFile(path, []byte(content), 0o700); err != nil {
			return "", fmt.Errorf("write %s: %w", name, err)
		}
	}

	// Write the agent instructions file. The launcher will append this to
	// the Claude Code system prompt via --append-system-prompt.
	//
	// Scripts are referenced by absolute path: the worker's cwd is the
	// target repository, not its own workspace, so a relative "./scripts/"
	// does not resolve. A worker caught this in the first orchestration run
	// and radioed the correction to its peer.
	instructions := buildAgentInstructions(a, cfg, scriptsDir)
	instrPath := filepath.Join(agentDir, "instructions.md")
	if err := os.WriteFile(instrPath, []byte(instructions), 0o600); err != nil {
		return "", fmt.Errorf("write instructions: %w", err)
	}

	return agentDir, nil
}

// buildAgentInstructions produces the system prompt fragment that teaches
// the worker how to use the radio and when to call task-done.
//
// scriptsDir must be absolute: the worker runs with its cwd set to the
// target repository, so relative script paths do not resolve.
func buildAgentInstructions(a *broker.Agent, cfg WorkerConfig, scriptsDir string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are %s in Narwhal run %s.\n", a.ID, a.RunID)
	fmt.Fprintf(&b, "Your task id is %s.\n\n", cfg.TaskID)
	fmt.Fprintf(&b, "Your task: %s\n\n", cfg.Assignment)
	fmt.Fprintf(&b, "## Radio Channel (peer communication)\n\n")
	fmt.Fprintf(&b, "Wrapper scripts live at this ABSOLUTE path (your cwd is the target\n")
	fmt.Fprintf(&b, "repository, so relative paths will NOT work):\n\n")
	fmt.Fprintf(&b, "  %s\n\n", scriptsDir)
	fmt.Fprintf(&b, "Your identity is baked into each script — never pass a URL or token.\n\n")
	fmt.Fprintf(&b, "- bash %s/send <threadId> \"<content>\" [mentionsCSV] [priority]\n", scriptsDir)
	fmt.Fprintf(&b, "    Send a message. threadId is usually \"worklog\". priority: fyi, normal, urgent.\n")
	fmt.Fprintf(&b, "    A message with no mentions is a broadcast, which is what you usually want:\n")
	fmt.Fprintf(&b, "      bash %s/send worklog \"finding\" \"\" urgent\n", scriptsDir)
	fmt.Fprintf(&b, "    Mention a peer only to address it specifically:\n")
	fmt.Fprintf(&b, "      bash %s/send worklog \"finding\" task-2,task-3 urgent\n", scriptsDir)
	fmt.Fprintf(&b, "- bash %s/drain [afterSeq]\n", scriptsDir)
	fmt.Fprintf(&b, "    Non-blocking check for new messages. Run after each investigation unit.\n")
	fmt.Fprintf(&b, "- bash %s/watch [afterSeq] [timeoutMs]\n", scriptsDir)
	fmt.Fprintf(&b, "    Long-poll for messages. Run as a BACKGROUND Bash task so you get a\n")
	fmt.Fprintf(&b, "    completion notification when a message arrives. Keep exactly one watcher\n")
	fmt.Fprintf(&b, "    running at all times; restart it immediately when it finishes.\n")
	fmt.Fprintf(&b, "- bash %s/state\n", scriptsDir)
	fmt.Fprintf(&b, "    Print the full run state.\n")
	fmt.Fprintf(&b, "- bash %s/task-done %s \"<outcome>\"\n", scriptsDir, cfg.TaskID)
	fmt.Fprintf(&b, "    Declare your task complete with a summary of findings.\n")
	fmt.Fprintf(&b, "- bash %s/split \"<newTaskId>\" \"<name>\" \"<assignment>\" [depsCSV]\n", scriptsDir)
	fmt.Fprintf(&b, "    Request a NEW task be added to the run. Use when you discover work\n")
	fmt.Fprintf(&b, "    that is genuinely independent of yours and should run in parallel.\n")
	fmt.Fprintf(&b, "    Existing tasks are immutable — you cannot edit your own task.\n\n")
	fmt.Fprintf(&b, "## Passive Awareness (CRITICAL)\n\n")
	fmt.Fprintf(&b, "1. Start ONE background watcher before you begin work:\n")
	fmt.Fprintf(&b, "     bash %s/watch\n", scriptsDir)
	fmt.Fprintf(&b, "   Run it with run_in_background=true so it never blocks your turn.\n")
	fmt.Fprintf(&b, "2. When the harness notifies you that it finished, restart it immediately,\n")
	fmt.Fprintf(&b, "   then handle whatever arrived.\n")
	fmt.Fprintf(&b, "3. After every investigation unit, run drain to catch messages that landed\n")
	fmt.Fprintf(&b, "   between watcher cycles.\n")
	fmt.Fprintf(&b, "4. URGENT messages may invalidate assumptions your current work rests on —\n")
	fmt.Fprintf(&b, "   handle them before starting the next piece of work.\n")
	fmt.Fprintf(&b, "5. Broadcast findings that affect a peer's active work the moment you find\n")
	fmt.Fprintf(&b, "   them. Sending costs you nothing and does not interrupt the receiver.\n\n")
	fmt.Fprintf(&b, "## Task Completion\n\n")
	fmt.Fprintf(&b, "When your task is complete, run:\n")
	fmt.Fprintf(&b, "  bash %s/task-done %s \"summary of your findings\"\n\n", scriptsDir, cfg.TaskID)
	fmt.Fprintf(&b, "You MUST call task-done, otherwise the coordinator records a failed\n")
	fmt.Fprintf(&b, "dispatch and retries your task.\n\n")
	fmt.Fprintf(&b, "Do NOT modify files unless your assignment explicitly says to.\n")
	return b.String()
}

// Launch starts a worker as a `ccproxy claude --print` process.
// It returns immediately; the process runs autonomously and writes its
// final output to the task's dispatch record via the broker API.
func (l *Launcher) Launch(agentDir string, cfg WorkerConfig) error {
	instrPath := filepath.Join(agentDir, "instructions.md")
	instructions, err := os.ReadFile(instrPath)
	if err != nil {
		return fmt.Errorf("read instructions: %w", err)
	}

	logPath := filepath.Join(l.sessionDir, "agents", cfg.AgentID, "claude-output.txt")
	logFile, err := os.Create(logPath)
	if err != nil {
		return fmt.Errorf("create log file: %w", err)
	}

	// bypassPermissions is required: workers run headless with --print, so
	// there is no interactive approval path. Without it every Bash call
	// touching the agent workspace (wrapper scripts, ack artifacts) is
	// blocked by the permission gate and the worker cannot do its job.
	// AgentRadio's own startup scripts use the same flag for this reason.
	args := []string{"claude", "--print",
		"--permission-mode", "bypassPermissions",
		"--append-system-prompt", string(instructions),
	}
	// Per-worker model override, then the launcher default, then ccproxy's
	// own account rotation. The planner picks the model; workers default to
	// whatever the launcher was configured with.
	model := cfg.Model
	if model == "" {
		model = l.workerModel
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	args = append(args, cfg.Assignment)
	cmd := exec.Command("ccproxy", args...)
	cmd.Dir = l.cwd
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Env = append(os.Environ(),
		"NARWHAL_RUN_ID="+l.runID,
		"NARWHAL_AGENT_ID="+cfg.AgentID,
		"NARWHAL_SESSION_DIR="+l.sessionDir,
		"PATH="+os.Getenv("PATH"),
	)

	if err := cmd.Start(); err != nil {
		logFile.Close()
		return fmt.Errorf("start ccproxy claude: %w", err)
	}

	l.mu.Lock()
	l.workers[cfg.AgentID] = cmd
	l.mu.Unlock()

	// Monitor the process in the background. When it exits, the log file
	// is closed and the worker is removed from the active map.
	go func() {
		_ = cmd.Wait()
		logFile.Close()
		l.mu.Lock()
		delete(l.workers, cfg.AgentID)
		l.mu.Unlock()
	}()

	return nil
}

// Wait blocks until all launched workers have exited or the timeout elapses.
func (l *Launcher) Wait(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		l.mu.Lock()
		active := len(l.workers)
		l.mu.Unlock()
		if active == 0 {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("timeout: %d workers still running", len(l.workers))
}

// ActiveWorkers returns the agent IDs of currently running workers.
func (l *Launcher) ActiveWorkers() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]string, 0, len(l.workers))
	for id := range l.workers {
		out = append(out, id)
	}
	return out
}

// KillAll terminates every running worker and returns the agent IDs it
// signalled. Used when the operator cancels a run: the tasks keep whatever
// state they reached, because cancellation is an operator decision rather
// than a failure of the work.
func (l *Launcher) KillAll() []string {
	l.mu.Lock()
	cmds := make(map[string]*exec.Cmd, len(l.workers))
	for id, cmd := range l.workers {
		cmds[id] = cmd
	}
	l.mu.Unlock()

	killed := make([]string, 0, len(cmds))
	for id, cmd := range cmds {
		if cmd.Process == nil {
			continue
		}
		if err := cmd.Process.Kill(); err != nil {
			continue
		}
		killed = append(killed, id)
	}
	return killed
}

func homeDir() string {
	h, _ := os.UserHomeDir()
	return h
}
