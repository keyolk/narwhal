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
	AgentID     string
	TaskID      string
	Assignment  string
	IsCoordinator bool
}

// Launcher owns worker process lifecycle for a Run.
type Launcher struct {
	brokerURL string
	runID     string
	cwd       string
	sessionDir string

	mu       sync.Mutex
	workers  map[string]*exec.Cmd
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
	}

	for name, content := range scripts {
		path := filepath.Join(scriptsDir, name)
		if err := os.WriteFile(path, []byte(content), 0o700); err != nil {
			return "", fmt.Errorf("write %s: %w", name, err)
		}
	}

	// Write the agent instructions file. The launcher will append this to
	// the Claude Code system prompt via --append-system-prompt.
	instructions := buildAgentInstructions(a, cfg)
	instrPath := filepath.Join(agentDir, "instructions.md")
	if err := os.WriteFile(instrPath, []byte(instructions), 0o600); err != nil {
		return "", fmt.Errorf("write instructions: %w", err)
	}

	return agentDir, nil
}

// buildAgentInstructions produces the system prompt fragment that teaches
// the worker how to use the radio and when to call task-done.
func buildAgentInstructions(a *broker.Agent, cfg WorkerConfig) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are %s in Narwhal run %s.\n\n", a.ID, a.RunID)
	fmt.Fprintf(&b, "Your task: %s\n\n", cfg.Assignment)
	fmt.Fprintf(&b, "## Radio Channel (peer communication)\n\n")
	fmt.Fprintf(&b, "Wrapper scripts are in ./scripts/ (relative to your working directory).\n")
	fmt.Fprintf(&b, "Your identity is baked in — never pass a URL or token.\n\n")
	fmt.Fprintf(&b, "- bash scripts/send <threadId> \"<content>\" [mentionsCSV] [priority]\n")
	fmt.Fprintf(&b, "    Send a message. priority: fyi, normal, urgent.\n")
	fmt.Fprintf(&b, "- bash scripts/drain [afterSeq]\n")
	fmt.Fprintf(&b, "    Non-blocking check for new messages. Run after each investigation unit.\n")
	fmt.Fprintf(&b, "- bash scripts/watch [afterSeq] [timeoutMs]\n")
	fmt.Fprintf(&b, "    Long-poll for messages. Run as a BACKGROUND Bash task so you get a\n")
	fmt.Fprintf(&b, "    completion notification when a message arrives. Keep exactly one watcher\n")
	fmt.Fprintf(&b, "    running at all times; restart it immediately when it finishes.\n")
	fmt.Fprintf(&b, "- bash scripts/state\n")
	fmt.Fprintf(&b, "    Print the full run state.\n")
	fmt.Fprintf(&b, "- bash scripts/task-done <taskId> \"<outcome>\"\n")
	fmt.Fprintf(&b, "    Declare your task complete with a summary of findings.\n\n")
	fmt.Fprintf(&b, "## Passive Awareness (CRITICAL)\n\n")
	fmt.Fprintf(&b, "1. Keep EXACTLY ONE background watcher running: bash scripts/watch\n")
	fmt.Fprintf(&b, "2. When it finishes, restart it immediately, then process the messages.\n")
	fmt.Fprintf(&b, "3. After every investigation unit, run bash scripts/drain to catch\n")
	fmt.Fprintf(&b, "   messages that arrived between watcher cycles.\n")
	fmt.Fprintf(&b, "4. URGENT messages from peers may change your assumptions — handle them\n")
	fmt.Fprintf(&b, "   before starting the next piece of work.\n\n")
	fmt.Fprintf(&b, "## Task Completion\n\n")
	fmt.Fprintf(&b, "When your task is complete, run:\n")
	fmt.Fprintf(&b, "  bash scripts/task-done %s \"summary of your findings\"\n\n", cfg.TaskID)
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

	cmd := exec.Command("ccproxy", "claude", "--print",
		"--append-system-prompt", string(instructions),
		cfg.Assignment,
	)
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

func homeDir() string {
	h, _ := os.UserHomeDir()
	return h
}
