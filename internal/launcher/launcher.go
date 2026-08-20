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
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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
	// sessions maps agent id → the Claude session UUID its worker runs
	// under, so a caller in this process can resume one without reading
	// the agent directory back.
	sessions map[string]string
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
# Non-blocking read of the whole channel since afterSeq. Unlike watch, this
# is NOT filtered to messages mentioning you — a peer's broadcast finding is
# the main thing worth draining for, and the server stopped filtering here
# after a message whose mentions slot held a priority went missing.
# Pass back the "cursor" from the response to read only what is new.
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
# usage: task-done <taskId> <outcome> [final] [after]
# Mark a task as completed with the given outcome text.
#
# If the task has dependencies that are still running, this call BLOCKS
# until they finish — that is the mechanism that keeps a synthesis task
# from writing its answer early. Blocking here rather than returning and
# asking the worker to retry is deliberate: a --print worker's process
# exits when its turn ends, so a worker that is told to "wait and try
# again" simply dies. Holding this request open holds the turn open.
#
# When peers post during the wait, the server answers 202 with those
# messages instead of completing: the outcome was written before they
# arrived, so completing on it would record a synthesis that had not seen
# them. The 202 is surfaced to the worker, which is expected to fold them
# in and call again.
#
# No --max-time: the wait is bounded by the server, and a client-side
# timeout would defeat the whole point.
set -uo pipefail
TASK="$1"; OUTCOME="$2"; FINAL="${3:-false}"; AFTER="${4:-0}"
# AFTER is how far you have read the radio — the cursor drain handed back.
# The server uses it to decide what to put in a 202, so pass it and the
# 202 carries what you have not seen. Omit it and the server falls back to
# its own last sequence, which is what it used to guess with: a peer
# message posted after your last drain but before this call was counted as
# already-seen and left out of the very list meant to deliver it.
BODY=$(python3 -c 'import json,sys; print(json.dumps({"outcome":sys.argv[1],"final":sys.argv[2]=="final","after":int(sys.argv[3])}))' "$OUTCOME" "$FINAL" "$AFTER")
OUTFILE="%s/outcome-$TASK.json"

# The broker can be gone: it is a separate long-lived process, and
# restarting it under a running worker is an ordinary thing to do by
# accident. A worker that finishes into a closed port has done the work and
# written the files, so losing the result to a connection error is the
# worst possible outcome. Retry, then record locally so the run can be
# reconciled from disk.
ATTEMPT=0
while :; do
  OUT=$(curl -s -w '\n%%{http_code}' -X POST %s/task/$TASK/done \
    -H "Content-Type: application/json" -d "$BODY")
  RC=$?
  CODE="${OUT##*$'\n'}"
  if [ $RC -eq 0 ] && [ -n "$CODE" ] && [ "$CODE" != "000" ]; then
    break
  fi
  ATTEMPT=$((ATTEMPT + 1))
  if [ $ATTEMPT -ge 4 ]; then
    printf '%%s' "$BODY" > "$OUTFILE"
    echo "task-done could not reach the broker after $ATTEMPT attempts." >&2
    echo "Your outcome was written to $OUTFILE so it is not lost." >&2
    exit 2
  fi
  # Short backoff: a restarting broker is back in a couple of seconds, and
  # a broker that is gone for good should not hold the worker hostage.
  sleep $ATTEMPT
done

echo "${OUT%%%%$'\n'*}"
if [ "$CODE" = "202" ]; then
  echo "" >&2
  echo "NOT COMPLETE YET. Your peers finished while this call waited, and the" >&2
  echo "messages above arrived after you wrote your outcome. Fold them into" >&2
  echo "your answer and run:" >&2
  echo "  bash $0 $TASK \"<updated answer>\" final $AFTER" >&2
  exit 4
fi
if [ "$CODE" = "409" ]; then
  echo "task-done timed out waiting for peers listed in pending_deps." >&2
  echo "Call task-done again — it blocks until they finish." >&2
  exit 3
fi
if [ "$CODE" != "200" ]; then
  echo "task-done failed with HTTP $CODE" >&2
  exit 1
fi
`, agentDir, base),
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
		"dep-add": fmt.Sprintf(`#!/bin/bash
# usage: dep-add "<taskId>" "<dep1,dep2,...>"
# Add dependency edges to an existing task. The task itself is immutable;
# only its dependency list changes. Use when a worker discovers a
# relationship mid-run that the planner did not know about.
set -euo pipefail
TASK="$1"; DEPS="${2:-}"
BODY="DEP_ADD|$TASK|$DEPS"
curl -s -X POST %s/send \
  -H "Content-Type: application/json" \
  -d "$(python3 -c 'import json,sys; print(json.dumps({"thread_id":"worklog","content":sys.argv[1],"mentions":[],"priority":"normal"}))' "$BODY")"
`, base),
		"dep-remove": fmt.Sprintf(`#!/bin/bash
# usage: dep-remove "<taskId>" "<dep1,dep2,...>"
# Remove dependency edges from an existing task. Use when a relationship
# the planner assumed turns out not to hold.
set -euo pipefail
TASK="$1"; DEPS="${2:-}"
BODY="DEP_REMOVE|$TASK|$DEPS"
curl -s -X POST %s/send \
  -H "Content-Type: application/json" \
  -d "$(python3 -c 'import json,sys; print(json.dumps({"thread_id":"worklog","content":sys.argv[1],"mentions":[],"priority":"normal"}))' "$BODY")"
`, base),
		"file-claim": fmt.Sprintf(`#!/bin/bash
# usage: file-claim "<taskId>" "<path1,path2,...>"
# Claim the files you are about to modify. If a peer already holds one,
# the coordinator replies on the radio with who holds it — negotiate there
# rather than overwriting. Claim before your first write, not after.
set -euo pipefail
TASK="$1"; PATHS="${2:-}"
BODY="FILE_CLAIM|$TASK|$PATHS"
curl -s -X POST %s/send \
  -H "Content-Type: application/json" \
  -d "$(python3 -c 'import json,sys; print(json.dumps({"thread_id":"worklog","content":sys.argv[1],"mentions":[],"priority":"normal"}))' "$BODY")"
`, base),
		"file-release": fmt.Sprintf(`#!/bin/bash
# usage: file-release "<taskId>" "<path1,path2,...>"
# Give up files you are done with so a peer can take them. Claims are
# released automatically when you exit, but releasing early unblocks peers.
set -euo pipefail
TASK="$1"; PATHS="${2:-}"
BODY="FILE_RELEASE|$TASK|$PATHS"
curl -s -X POST %s/send \
  -H "Content-Type: application/json" \
  -d "$(python3 -c 'import json,sys; print(json.dumps({"thread_id":"worklog","content":sys.argv[1],"mentions":[],"priority":"normal"}))' "$BODY")"
`, base),
		"escalate": fmt.Sprintf(`#!/bin/bash
# usage: escalate "<taskId>" "<model|empty>" "<reason>"
# Ask the coordinator to retry this task on a stronger model. Use when the
# area turns out to need more than the tier you were given — a thin answer
# from a model that cannot do the work is worse than a retry.
# Leave the model empty to move one tier up (haiku → sonnet → opus).
set -euo pipefail
TASK="$1"; MODEL="${2:-}"; REASON="${3:-}"
BODY="MODEL_ESCALATE|$TASK|$MODEL|$REASON"
curl -s -X POST %s/send \
  -H "Content-Type: application/json" \
  -d "$(python3 -c 'import json,sys; print(json.dumps({"thread_id":"worklog","content":sys.argv[1],"mentions":[],"priority":"urgent"}))' "$BODY")"
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
	fmt.Fprintf(&b, "    Send a message. Threads: worklog (findings — the usual one),\n")
	fmt.Fprintf(&b, "    planning (plan changes), results (final output),\n")
	fmt.Fprintf(&b, "    environment (facts about the shared environment, see below).\n")
	fmt.Fprintf(&b, "    priority: fyi, normal, urgent.\n")
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
	fmt.Fprintf(&b, "    Existing tasks are immutable — you cannot edit your own task.\n")
	fmt.Fprintf(&b, "- bash %s/dep-add \"<taskId>\" \"<dep1,dep2,...>\"\n", scriptsDir)
	fmt.Fprintf(&b, "    Add dependency edges to an existing task. Use when you discover a\n")
	fmt.Fprintf(&b, "    relationship mid-run that the planner did not know about — e.g.\n")
	fmt.Fprintf(&b, "    'this area depends on that area being finished first.'\n")
	fmt.Fprintf(&b, "- bash %s/dep-remove \"<taskId>\" \"<dep1,dep2,...>\"\n", scriptsDir)
	fmt.Fprintf(&b, "    Remove dependency edges that turn out not to hold.\n")
	fmt.Fprintf(&b, "- bash %s/file-claim %s \"<path1,path2,...>\"\n", scriptsDir, cfg.TaskID)
	fmt.Fprintf(&b, "    Claim files BEFORE you modify them. If a peer holds one, the\n")
	fmt.Fprintf(&b, "    coordinator replies urgently with who holds it — negotiate on the\n")
	fmt.Fprintf(&b, "    radio instead of overwriting.\n")
	fmt.Fprintf(&b, "- bash %s/file-release %s \"<path1,path2,...>\"\n", scriptsDir, cfg.TaskID)
	fmt.Fprintf(&b, "    Give up files you are done with. Your claims are released when your\n")
	fmt.Fprintf(&b, "    task completes, but releasing early unblocks a waiting peer — a\n")
	fmt.Fprintf(&b, "    peer blocked on a path you finished with an hour ago is an hour\n")
	fmt.Fprintf(&b, "    wasted.\n")
	fmt.Fprintf(&b, "- bash %s/escalate %s \"\" \"<reason>\"\n", scriptsDir, cfg.TaskID)
	fmt.Fprintf(&b, "    Ask to be retried on a stronger model. Use when the area needs more\n")
	fmt.Fprintf(&b, "    than the tier you were given — a thin answer from a model that cannot\n")
	fmt.Fprintf(&b, "    do the work is worse than a retry. Say concretely what exceeded you.\n")
	fmt.Fprintf(&b, "\n## Passive Awareness (CRITICAL)\n\n")
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
	fmt.Fprintf(&b, "If your task depends on peers, task-done BLOCKS until they finish.\n")
	fmt.Fprintf(&b, "That is the intended behaviour, not a hang — call it when you have\n")
	fmt.Fprintf(&b, "written what you can and let it hold. Do NOT decide to \"wait and call\n")
	fmt.Fprintf(&b, "it later\": your process ends when your turn does, so a plan to wait\n")
	fmt.Fprintf(&b, "is not waiting. Blocking inside task-done is what keeps you alive.\n\n")
	fmt.Fprintf(&b, "If peers posted while you were held, task-done exits 4 and prints what\n")
	fmt.Fprintf(&b, "arrived. Your task is NOT complete. Those messages landed after you\n")
	fmt.Fprintf(&b, "wrote your answer, so fold them in and call it once more:\n\n")
	fmt.Fprintf(&b, "  bash %s/task-done %s \"<updated answer>\" final\n\n", scriptsDir, cfg.TaskID)
	fmt.Fprintf(&b, "Do NOT modify files unless your assignment explicitly says to.\n")
	fmt.Fprintf(&b, "\n## The environment thread\n\n")
	fmt.Fprintf(&b, "Post to the \"environment\" thread when you learn something about the\n")
	fmt.Fprintf(&b, "shared setup rather than about the question:\n\n")
	fmt.Fprintf(&b, "  - the build is broken, and how you worked around it\n")
	fmt.Fprintf(&b, "  - a tool or dependency the repo expects is missing here\n")
	fmt.Fprintf(&b, "  - a command that looks right but does not work in this checkout\n")
	fmt.Fprintf(&b, "  - a quota, permission, or sandbox limit you hit\n\n")
	fmt.Fprintf(&b, "Peers read this thread before repeating your work. A broken build\n")
	fmt.Fprintf(&b, "reported here is discovered once; reported to worklog it reads as one\n")
	fmt.Fprintf(&b, "more finding and the next three workers rediscover it themselves.\n")
	fmt.Fprintf(&b, "Drain it early — someone may have already mapped the ground you are\n")
	fmt.Fprintf(&b, "about to walk.\n")
	fmt.Fprintf(&b, "\n## Writing Files\n\n")
	fmt.Fprintf(&b, "If your assignment does have you modify files, claim them first:\n\n")
	fmt.Fprintf(&b, "  bash %s/file-claim %s \"path/one.go,path/two.go\"\n\n", scriptsDir, cfg.TaskID)
	fmt.Fprintf(&b, "Peers work in parallel on the same repository. A claim is how they\n")
	fmt.Fprintf(&b, "find out you are there. If the coordinator answers that a path is\n")
	fmt.Fprintf(&b, "already held, do not write it — say what you need on the radio and\n")
	fmt.Fprintf(&b, "let the holder either hand it over or make the change for you.\n")
	return b.String()
}

// newSessionUUID mints the UUID a worker's Claude session will be recorded
// under. Claude Code accepts --session-id, so choosing it here rather than
// letting Claude pick means the monitor can hand the user an exact
// `claude --resume <id>` instead of guessing which transcript in
// ~/.claude/projects belongs to which worker.
//
// Format is UUIDv4 as Claude requires; the version and variant bits are set
// explicitly because a plain random hex string is rejected.
func newSessionUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:], nil
}

// recordSession writes the worker's Claude session id next to its other
// artifacts. The monitor is a separate process — it polls the broker over
// HTTP and never shares memory with the launcher — so the id has to reach
// it through the filesystem or not at all.
func (l *Launcher) recordSession(agentID, sessionID string) {
	path := filepath.Join(l.sessionDir, "agents", agentID, "claude-session-id")
	if err := os.WriteFile(path, []byte(sessionID+"\n"), 0o600); err != nil {
		// Not worth failing a launch over: the worker runs fine, the
		// monitor just cannot offer to resume it.
		fmt.Fprintf(os.Stderr, "[launcher] warning: record session id for %s: %v\n", agentID, err)
	}
}

// SessionID returns the Claude session UUID a worker was launched with,
// or "" if it has not been launched or the id could not be minted.
func (l *Launcher) SessionID(agentID string) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.sessions[agentID]
}

// Launch starts a worker as a `ccproxy claude --print` process.
// It returns immediately; the process runs autonomously and writes its
// final output to the task's dispatch record via the broker API.
func (l *Launcher) Launch(agentDir string, cfg WorkerConfig) error {
	// Check the working directory before spawning. When cwd is missing or
	// is not a directory, fork/exec fails with ENOTDIR and Go renders it
	// against the *binary* path: "fork/exec /path/to/ccproxy: not a
	// directory". That sends you to inspect a file that is perfectly fine
	// — the failing path is nowhere in the message.
	if fi, err := os.Stat(l.cwd); err != nil {
		return fmt.Errorf("working directory %s: %w", l.cwd, err)
	} else if !fi.IsDir() {
		return fmt.Errorf("working directory %s is not a directory", l.cwd)
	}
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
	// Pin the session id so the worker's transcript can be found later.
	// A --print worker writes nothing to stdout until it finishes, so the
	// captured log is empty for the whole run and there is nothing to
	// attach to; the transcript under ~/.claude/projects is the live
	// record. Claude picks a random UUID unless told otherwise, which
	// would leave the monitor guessing which of several transcripts in a
	// shared cwd belongs to which worker.
	//
	// A failure here is not fatal: the worker still runs, it just cannot
	// be resumed by id.
	if sid, err := newSessionUUID(); err == nil {
		args = append(args, "--session-id", sid)
		l.mu.Lock()
		if l.sessions == nil {
			l.sessions = map[string]string{}
		}
		l.sessions[cfg.AgentID] = sid
		l.mu.Unlock()
		l.recordSession(cfg.AgentID, sid)
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
	l.recordPID(cfg.AgentID, cmd.Process.Pid)

	// Monitor the process in the background. When it exits, the log file
	// is closed and the worker is removed from the active map.
	go func() {
		_ = cmd.Wait()
		logFile.Close()
		l.mu.Lock()
		delete(l.workers, cfg.AgentID)
		l.mu.Unlock()
		l.clearPID(cfg.AgentID)
	}()

	return nil
}

// recordPID writes the worker's process id next to its other artifacts.
//
// A daemon that restarts loses the map of running workers, and the workers
// themselves survive — they are detached processes. Without a pid on disk
// the new daemon cannot tell a task whose worker is still going from one
// whose worker died, and the difference decides whether re-dispatching it
// is a recovery or a duplicate.
func (l *Launcher) recordPID(agentID string, pid int) {
	path := filepath.Join(l.sessionDir, "agents", agentID, "claude-pid")
	if err := os.WriteFile(path, []byte(strconv.Itoa(pid)+"\n"), 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "[launcher] warning: record pid for %s: %v\n", agentID, err)
	}
}

// clearPID removes the pid file when a worker exits, so a later adoption
// does not mistake a recycled pid for the worker still running.
func (l *Launcher) clearPID(agentID string) {
	_ = os.Remove(filepath.Join(l.sessionDir, "agents", agentID, "claude-pid"))
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
