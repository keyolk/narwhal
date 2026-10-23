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
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/keyolk/narwhal/internal/broker"
)

// WorkerConfig describes one worker to launch.
type WorkerConfig struct {
	AgentID    string
	TaskID     string
	Assignment string
	// Request is the run's original request, as the operator wrote it.
	// The assignment is the planner's slice of it, and a slice drops
	// things: on SWE-Atlas measure-2 the question asked what is visible
	// on screen, the planner gave the launch to one task and marked the
	// other three "(read-only): trace…", and nobody but that one task
	// knew the run was meant to show runtime evidence.
	Request       string
	IsCoordinator bool
	Model         string // --model flag for claude; empty = ccproxy default
	// Check is the end condition this task must answer before it can
	// complete. Empty for most tasks. It goes in the instructions so the
	// worker can run it while it still has the context to, rather than
	// meeting it for the first time as a 202 at task-done.
	Check string
	// ResumeSession is the Claude session to continue rather than start:
	// a finished task reopened with a follow-up resumes the worker that
	// did it, with Prompt as its next turn. See broker/followup.go.
	ResumeSession string
	Prompt        string
	// Reviewer marks a synthesis or checkpoint: it reviews the workers'
	// candidate questions before they reach the operator. See
	// broker/questions.go.
	Reviewer bool
	// Permissions is the run's permission policy; the zero value is
	// bypassPermissions. See broker/permissions.go.
	Permissions broker.Permissions
}

// WorkerConfigFor builds the config for a task's worker.
//
// One function, because there were two sites building this by hand and
// they drifted: the batch coordinator passed the task's model and the
// interactive path did not, so a tier the planner chose reached the
// graph, the monitor and the snapshot and never reached the worker. It
// went unnoticed for the life of the feature — of the 13 tasks on disk
// that named a tier, the 5 served by a different model are all
// interactive runs and the batch runs have none. Token accounting is
// what made that countable.
//
// It lives here rather than in either dispatcher because both of them
// already depend on this package, and because the lesson is the one
// Task.snapshot() records: adding a field to WorkerConfig must not
// require remembering a second site.
func WorkerConfigFor(run *broker.Run, task *broker.Task) WorkerConfig {
	return WorkerConfig{
		AgentID:    "worker-" + task.ID,
		TaskID:     task.ID,
		Assignment: task.Assignment,
		Request:    run.Request(),
		Model:      task.CurrentModel(),
		// The end condition travels with the assignment. A worker that
		// first meets its check as a 202 has already written its answer
		// and dropped the context it needed to test it.
		Check:    task.CurrentCheck(),
		Reviewer: task.IsReviewer(),
	}
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
# usage: task-done <taskId> <outcome> [final] [after] [checkResult]
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
TASK="$1"; OUTCOME="$2"; FINAL="${3:-false}"; AFTER="${4:-0}"; CHECK="${5:-}"
# CHECK is what your task's end condition showed when you ran it. If your
# task was given one, the server answers 202 with the check instead of
# completing, and you call again with the result here. Report what it
# actually showed — a check that only ever confirms is not a check.
# AFTER is how far you have read the radio — the cursor drain handed back.
# The server uses it to decide what to put in a 202, so pass it and the
# 202 carries what you have not seen. Omit it and the server falls back to
# its own last sequence, which is what it used to guess with: a peer
# message posted after your last drain but before this call was counted as
# already-seen and left out of the very list meant to deliver it.
BODY=$(python3 -c 'import json,sys; print(json.dumps({"outcome":sys.argv[1],"final":sys.argv[2]=="final","after":int(sys.argv[3]),"check_result":sys.argv[4]}))' "$OUTCOME" "$FINAL" "$AFTER" "$CHECK")
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

BOD="${OUT%%%%$'\n'*}"
echo "$BOD"
if [ "$CODE" = "202" ]; then
  # Two different 202s reach here and they ask for different things, so
  # tell them apart by what the body carries rather than by the code. One
  # says peers posted while this call waited; the other says the task has
  # an end condition that has not been answered.
  if printf '%%s' "$BOD" | grep -q '"check"'; then
    echo "" >&2
    echo "NOT COMPLETE YET. This task was given an end condition (\"check\" above)." >&2
    echo "Run it, then call again with what it showed:" >&2
    echo "  bash $0 $TASK \"<answer>\" final $AFTER \"<what the check showed>\"" >&2
    echo "Report the real result, including one that contradicts your answer." >&2
    exit 5
  fi
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
		"artifact": fmt.Sprintf(`#!/bin/bash
# usage: artifact <taskId> <pr|branch|commit|file> <value>
# Record something this task made, as it is made: a PR you opened, a branch
# you pushed, a commit, a file you created or changed. The run's result
# lists these, the next step of the work is planned with them, and the
# operator checks them — a hash mentioned only in your outcome text is a
# claim nobody can list.
set -euo pipefail
curl -s -X POST %s/task/$1/artifact \
  -H "Content-Type: application/json" \
  -d "$(python3 -c 'import json,sys; print(json.dumps({"kind":sys.argv[1],"value":sys.argv[2]}))' "$2" "$3")"
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
		"ask": fmt.Sprintf(`#!/bin/bash
# usage: ask "<question>" "<what you will assume>" [assume|blocking]
#            [--option "<A>" --option "<B>" ...] [--recommend <n>] [--context "<what you found>"]
# Ask the operator about an ambiguity you cannot settle from the code or the
# request. The question is collected with the run's others and answered
# together. assume (default): returns at once; carry on with your assumption.
# blocking: waits for the answer and prints it; use only when no reasonable
# assumption lets you go on. It waits about a minute and a half at a time:
# {"status":"waiting"} means ask again with: ask --wait <id> When the choice is between
# options your research found, list them, say which you recommend, and give
# the context that makes it a question.
set -euo pipefail
if [ "${1:-}" = "--wait" ]; then
  curl -s --max-time 110 "%[1]s/ask-wait?id=${2:?usage: ask --wait <id>}"
  exit 0
fi
# The arguments are read in Python: positionally as the usage says, or with
# --assume/--blocking, which workers reach for — a shell shift took the
# flag itself as the assumption.
python3 - "$@" <<'PY' > /tmp/narwhal-ask-$$.json
import json, sys
args = sys.argv[1:]
pos, opts, ctx, rec, assumed, mode = [], [], "", 0, None, "assume"
i = 0
while i < len(args):
    a = args[i]
    has = i + 1 < len(args)
    if a == "--option" and has: opts.append(args[i+1]); i += 2
    elif a == "--context" and has: ctx = args[i+1]; i += 2
    elif a == "--recommend" and has: rec = int(args[i+1]); i += 2
    elif a == "--assume" and has: assumed = args[i+1]; i += 2
    elif a == "--blocking": mode = "blocking"; i += 1
    elif a in ("assume", "blocking") and len(pos) >= 1: mode = a; i += 1
    elif a.startswith("--"): i += 1
    else: pos.append(a); i += 1
q = pos[0] if pos else ""
if assumed is None:
    assumed = pos[1] if len(pos) > 1 else ""
print(json.dumps({"question": q, "assumed": assumed, "blocking": mode == "blocking",
                  "options": opts, "context": ctx, "recommended": rec}))
PY
curl -s --max-time 110 -X POST %[1]s/ask -H "Content-Type: application/json" -d @/tmp/narwhal-ask-$$.json
rm -f /tmp/narwhal-ask-$$.json
`, base),
		"candidates": fmt.Sprintf(`#!/bin/bash
# usage: candidates
# The workers' questions waiting for your review (synthesis or checkpoint).
set -euo pipefail
curl -s %s/candidates
`, base),
		"resolve": fmt.Sprintf(`#!/bin/bash
# usage: resolve '<json list>'
#   [{"id":3,"answer":"what the findings settle"},
#    {"id":4,"merge_into":3},
#    {"id":5,"raise":true,"options":["A","B"],"recommended":1,"context":"what the research found"}]
# Settle candidate questions from the peers' findings, merge duplicates, and
# raise the rest to the operator with the options and your recommendation.
set -euo pipefail
curl -s -X POST %s/resolve -H "Content-Type: application/json" \
  -d "$(python3 -c 'import json,sys; print(json.dumps({"resolutions":json.loads(sys.argv[1])}))' "$1")"
`, base),
		"judge": fmt.Sprintf(`#!/bin/bash
# usage: judge "<yes/no question>" "<state>"   (or pipe the state on stdin)
# Ask a fast decision model a yes/no question about some state and get a
# probability back in about a second. Prints e.g. {"decided":true,"probability":0.83}.
# "decided":false means no answer (no key, gateway down): decide yourself.
set -euo pipefail
QUESTION="$1"; STATE="${2:-}"
if [ -z "$STATE" ] && [ ! -t 0 ]; then STATE="$(cat)"; fi
curl -s -X POST %s/judge \
  -H "Content-Type: application/json" \
  -d "$(python3 -c 'import json,sys; print(json.dumps({"question":sys.argv[1],"state":sys.argv[2]}))' "$QUESTION" "$STATE")"
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
	if r := strings.TrimSpace(cfg.Request); r != "" && r != strings.TrimSpace(cfg.Assignment) {
		fmt.Fprintf(&b, "## The request this run answers\n\n")
		fmt.Fprintf(&b, "Your task is one part of this. Do your part; read the whole so you\n")
		fmt.Fprintf(&b, "know what kind of evidence the answer needs and what your peers are\n")
		fmt.Fprintf(&b, "covering.\n\n")
		fmt.Fprintf(&b, "<request>\n%s\n</request>\n\n", clipRequest(r))
	}
	fmt.Fprintf(&b, "## Before you conclude it cannot be done\n\n")
	fmt.Fprintf(&b, "A conclusion that something is impossible, missing, unsupported or\n")
	fmt.Fprintf(&b, "unreproducible is the one your peers and the synthesis will not\n")
	fmt.Fprintf(&b, "re-check. Before you reach it, try at least one concrete alternative:\n")
	fmt.Fprintf(&b, "a different environment (a container or image, if one is available),\n")
	fmt.Fprintf(&b, "installing or building what is missing, another tool, or a wider search.\n")
	fmt.Fprintf(&b, "In the outcome, list what you tried and what each attempt returned.\n")
	fmt.Fprintf(&b, "Post each approach you abandon, and why, to the worklog as you abandon\n")
	fmt.Fprintf(&b, "it — a peer may see a way around it.\n\n")
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
	fmt.Fprintf(&b, "- bash %s/artifact %s <pr|branch|commit|file> <value>\n", scriptsDir, cfg.TaskID)
	fmt.Fprintf(&b, "    Record each thing you make AS you make it: a PR URL, a branch you\n")
	fmt.Fprintf(&b, "    pushed, a commit hash, a file you created. One call per artifact.\n")
	fmt.Fprintf(&b, "    The run's result and the next step of the work are built from these.\n")
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
	fmt.Fprintf(&b, "- bash %s/ask \"<question>\" \"<what you will assume>\" [assume|blocking]\n", scriptsDir)
	fmt.Fprintf(&b, "    Ask the OPERATOR when the request or the code leaves a choice open\n")
	fmt.Fprintf(&b, "    that would change your result — which of two readings was meant,\n")
	fmt.Fprintf(&b, "    whether something is in scope, which behaviour to keep. Do not invent\n")
	fmt.Fprintf(&b, "    an answer silently: ask, state what you will assume, and carry on\n")
	fmt.Fprintf(&b, "    (assume, the default). The operator answers the run's questions\n")
	fmt.Fprintf(&b, "    together; if the answer differs, your task is reopened with it. Use\n")
	fmt.Fprintf(&b, "    blocking only when no reasonable assumption lets you go on — it waits\n")
	fmt.Fprintf(&b, "    about 90s at a time; on {\"status\":\"waiting\"} run ask --wait <id>\n")
	fmt.Fprintf(&b, "    again until answered. Not for things you can find out yourself. When the\n")
	fmt.Fprintf(&b, "    choice is between options your research found, add --option for each,\n")
	fmt.Fprintf(&b, "    --recommend <n> and --context \"<what you found>\". The operator has not\n")
	fmt.Fprintf(&b, "    read what you read: spell out labels and abbreviations in the question.\n")
	if cfg.Reviewer {
		fmt.Fprintf(&b, "\n## You review the workers' questions\n\n")
		fmt.Fprintf(&b, "Your peers' questions reach you before the operator: you see their\n")
		fmt.Fprintf(&b, "findings together, so you can tell which questions the findings already\n")
		fmt.Fprintf(&b, "answer, which ask the same thing, and which are real choices only the\n")
		fmt.Fprintf(&b, "operator can make. Once the findings are in — before you write your\n")
		fmt.Fprintf(&b, "answer — run:\n\n")
		fmt.Fprintf(&b, "- bash %s/candidates\n", scriptsDir)
		fmt.Fprintf(&b, "    The questions waiting for you.\n")
		fmt.Fprintf(&b, "- bash %s/resolve '<json list>'\n", scriptsDir)
		fmt.Fprintf(&b, "    For each: {\"id\":N,\"answer\":\"...\"} when the findings settle it (the asker\n")
		fmt.Fprintf(&b, "    is told, or reopened if it assumed otherwise); {\"id\":N,\"merge_into\":M} for a\n")
		fmt.Fprintf(&b, "    duplicate; {\"id\":N,\"raise\":true,\"options\":[...],\"recommended\":k,\n")
		fmt.Fprintf(&b, "    \"context\":\"...\"} for a real choice — give the options the research found and\n")
		fmt.Fprintf(&b, "    your recommendation, so the operator can answer with a number.\n")
		fmt.Fprintf(&b, "Do not raise what the findings settle; do not settle what only the\n")
		fmt.Fprintf(&b, "operator can decide. What you leave is raised when you finish.\n")
	}
	fmt.Fprintf(&b, "- bash %s/judge \"<yes/no question>\" \"<state>\"\n", scriptsDir)
	fmt.Fprintf(&b, "    Ask a fast decision model a yes/no question about concrete state —\n")
	fmt.Fprintf(&b, "    a test log, a diff, a peer's message — and get a probability in about\n")
	fmt.Fprintf(&b, "    a second. Use it for calls you would otherwise guess: is this failure\n")
	fmt.Fprintf(&b, "    flaky or real, does this output answer the assignment, does this\n")
	fmt.Fprintf(&b, "    peer's change conflict with mine. It is advice; you decide. The\n")
	fmt.Fprintf(&b, "    operator sees every question and answer.\n")
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
	if c := strings.TrimSpace(cfg.Check); c != "" {
		// Stated here as well as handed back by the gate. The 202 arrives
		// when the worker has already written its answer, which is the
		// worst moment to discover it should have been testing something
		// — it has to reconstruct context it was about to drop. Knowing
		// up front makes the check part of the work rather than an
		// after-the-fact defence of it.
		fmt.Fprintf(&b, "### Your end condition\n\n")
		fmt.Fprintf(&b, "This task carries a check. Your answer is not accepted until you\n")
		fmt.Fprintf(&b, "run it and report what it showed:\n\n")
		fmt.Fprintf(&b, "  %s\n\n", c)
		fmt.Fprintf(&b, "Run it as part of the work, not after it. Then pass the result:\n\n")
		fmt.Fprintf(&b, "  bash %s/task-done %s \"<answer>\" final 0 \"<what the check showed>\"\n\n",
			scriptsDir, cfg.TaskID)
		fmt.Fprintf(&b, "Report what it ACTUALLY showed. A result that contradicts your\n")
		fmt.Fprintf(&b, "answer is the valuable one — it means the check did its job, and\n")
		fmt.Fprintf(&b, "the right response is to correct the answer, not the check. A check\n")
		fmt.Fprintf(&b, "that only ever confirms is not a check.\n\n")
		fmt.Fprintf(&b, "If you call task-done without a result, it exits 5 and hands the\n")
		fmt.Fprintf(&b, "check back. Your task is NOT complete at that point.\n\n")
	}
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

// requestLimit bounds the request quoted into a worker's instructions.
// A request is operator-written and usually short; a pasted log should
// not crowd out the instructions around it.
const requestLimit = 8000

func clipRequest(s string) string {
	if len(s) <= requestLimit {
		return s
	}
	n := requestLimit
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "\n…(request truncated)"
}

// NewSessionUUID mints a Claude session id for an agent the launcher does
// not start itself: the planner.
func NewSessionUUID() (string, error) { return newSessionUUID() }

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

// RecordedSessionID is the Claude session a worker of this run was launched
// with, as recorded on disk — which outlives the launcher and the daemon —
// or "" when there is none.
func (l *Launcher) RecordedSessionID(agentID string) string {
	b, err := os.ReadFile(filepath.Join(l.sessionDir, "agents", agentID, "claude-session-id"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// CWD is the directory the run's workers start in.
func (l *Launcher) CWD() string { return l.cwd }

// TranscriptPath resolves a session id to the file Claude Code writes it
// to: ~/.claude/projects/<encoded-cwd>/<session-id>.jsonl.
//
// The directory name is the working directory with every character that is
// not alphanumeric replaced by a hyphen — "/private/tmp/x" becomes
// "-private-tmp-x". Note this is the *resolved* path: /tmp is a symlink to
// /private/tmp on macOS, and Claude records the resolved form, so a
// transcript for a run whose cwd was recorded as /tmp/x is filed under
// -private-tmp-x.
func TranscriptPath(cwd, sessionID string) string {
	home := homeDir()
	if home == "" || sessionID == "" {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(cwd); err == nil {
		cwd = resolved
	}
	var b strings.Builder
	for _, r := range cwd {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return filepath.Join(home, ".claude", "projects", b.String(), sessionID+".jsonl")
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

	perm, err := permissionArgs(cfg.Permissions, filepath.Join(agentDir, "scripts"))
	if err != nil {
		logFile.Close()
		return err
	}
	args := append([]string{"claude", "--print"}, perm...)
	args = append(args, "--append-system-prompt", string(instructions))
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
	if cfg.ResumeSession != "" {
		// The same session, continued: its transcript grows in place, so
		// the monitor follows it under the id it already knows.
		args = append(args, "--resume", cfg.ResumeSession)
		l.mu.Lock()
		if l.sessions == nil {
			l.sessions = map[string]string{}
		}
		l.sessions[cfg.AgentID] = cfg.ResumeSession
		l.mu.Unlock()
	} else if sid, err := newSessionUUID(); err == nil {
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
	prompt := cfg.Assignment
	if cfg.ResumeSession != "" {
		prompt = cfg.Prompt
	}
	args = append(args, prompt)
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

// permissionArgs is the permission part of a worker's command line.
//
// bypassPermissions is the default: workers run headless with --print, so
// there is no interactive approval path. Without it every Bash call
// touching the agent workspace (wrapper scripts, ack artifacts) is blocked
// by the permission gate and the worker cannot do its job. AgentRadio's own
// startup scripts use the same flag for this reason.
//
// A dontAsk run gets the same reach to its own workspace by a different
// route: every wrapper script in scriptsDir is added to the allow list, in
// both forms a worker calls it (the instructions say "bash <path>", and a
// worker that drops the prefix still works). Everything else is what the
// run's rules allow; Claude Code refuses the rest without asking.
//
// The rules travel as --settings JSON rather than --allowedTools. That flag
// is variadic and would take the prompt after it as one more rule.
func permissionArgs(p broker.Permissions, scriptsDir string) ([]string, error) {
	mode := p.Mode
	if mode == "" {
		mode = "bypassPermissions"
	}
	args := []string{"--permission-mode", mode}
	allow := slices.Clone(p.Allow)
	if mode == "dontAsk" {
		entries, err := os.ReadDir(scriptsDir)
		if err != nil {
			return nil, fmt.Errorf("list wrapper scripts for the allow list: %w", err)
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			path := filepath.Join(scriptsDir, e.Name())
			allow = append(allow, "Bash(bash "+path+":*)", "Bash("+path+":*)")
		}
	}
	if len(allow) == 0 && len(p.Deny) == 0 {
		return args, nil
	}
	rules := map[string][]string{}
	if len(allow) > 0 {
		rules["allow"] = allow
	}
	if len(p.Deny) > 0 {
		rules["deny"] = p.Deny
	}
	settings, err := json.Marshal(map[string]any{"permissions": rules})
	if err != nil {
		return nil, err
	}
	return append(args, "--settings", string(settings)), nil
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
