#!/usr/bin/env bash
# Narwhal arm: planner decomposes the question into a DAG, workers investigate
# in parallel over a shared radio, a synthesis task writes the answer.
#
# Usage: run_narwhal.sh <task-dir> <repo> <trial-dir> [timeout-sec] [concurrency]
set -euo pipefail

TASK_DIR="${1:?usage: run_narwhal.sh <task-dir> <repo> <trial-dir> [timeout] [conc]}"
REPO="${2:?}"
TRIAL="${3:?}"
TIMEOUT="${4:-1800}"
CONC="${5:-3}"
HERE="$(cd "$(dirname "$0")" && pwd)"

mkdir -p "$TRIAL/agent"

# The answer is written to a short neutral path, not into the trial directory,
# and copied back afterwards.
#
# Why: Claude Code's scratchpad path embeds the username with dots replaced by
# hyphens (/private/tmp/claude-502/-Users-gavin-jeong/...), but a worker
# reconstructing a path under its own $HOME writes the dotted form
# (-Users-gavin.jeong). Both directories then exist, the worker reports
# success, and the judge finds nothing. A path with no username in it cannot
# be rewritten this way.
ANSWER_DIR="/tmp/narwhal-bench/$(basename "$(dirname "$TRIAL")")-$(basename "$TRIAL")"
ANSWER="$ANSWER_DIR/answer.txt"
mkdir -p "$ANSWER_DIR"
rm -f "$ANSWER"

python3 "$HERE/make_prompt.py" "$TASK_DIR" "$REPO" "$ANSWER" --arm narwhal \
  > "$TRIAL/prompt.txt"

# The DAG completing is not the same as the artifact existing: without an
# explicit synthesis contract the workers each report to the radio and nobody
# writes the file the judge reads.
cat >> "$TRIAL/prompt.txt" <<EOF

## Plan requirement

Create a synthesis task with NO deps (so it starts immediately alongside
the investigation tasks). Its assignment must state that it:

  1. starts a background watcher on the radio immediately
  2. drains the radio repeatedly, accumulating peer findings as they arrive
  3. waits until every investigation task has called task-done (check via
     the state script) before writing the final answer
  4. writes the complete answer to ${ANSWER}, wrapped in <<FINAL_ANSWER>> tags,
     using the exact format given above
  5. preserves the concrete details peers reported (file paths, function
     names, exact values) rather than summarizing them away

This makes synthesis run in parallel with investigation, not after it —
the DAG has depth 1 instead of 2, which is what keeps wall-clock competitive
with a single agent.

EOF

PLAN_TIMEOUT=$(( TIMEOUT / 5 ))
WORKER_MODEL="${NARWHAL_WORKER_MODEL:-}"
SYNTHESIS_MODEL="${NARWHAL_SYNTHESIS_MODEL:-}"
START=$(python3 -c 'import time; print(time.time())')
set +e
gtimeout $(( TIMEOUT + 120 )) narwhal plan \
  --prompt "$(cat "$TRIAL/prompt.txt")" \
  --cwd "$REPO" \
  --concurrency "$CONC" \
  ${WORKER_MODEL:+--worker-model "$WORKER_MODEL"} \
  ${SYNTHESIS_MODEL:+--synthesis-model "$SYNTHESIS_MODEL"} \
  --plan-timeout "${PLAN_TIMEOUT}s" \
  --timeout "${TIMEOUT}s" \
  > "$TRIAL/agent/narwhal.json" 2> "$TRIAL/agent/narwhal.log"
RC=$?
set -e
END=$(python3 -c 'import time; print(time.time())')

# Bring the answer back into the trial directory, where the judge reads it.
if [ -f "$ANSWER" ]; then
  cp "$ANSWER" "$TRIAL/agent/answer.txt"
fi

python3 - "$TRIAL" "$START" "$END" "$RC" <<'EOF'
import json, os, sys
trial, start, end, rc = sys.argv[1], float(sys.argv[2]), float(sys.argv[3]), int(sys.argv[4])
meta = {"arm": "narwhal", "exit_code": rc, "wall_clock_sec": round(end - start, 1)}
path = f"{trial}/agent/narwhal.json"
try:
    data = json.load(open(path))
    snap = data.get("snapshot", {})
    meta["tasks"] = len(snap.get("tasks", []))
    meta["messages"] = len(snap.get("messages", []))
    meta["session_dir"] = data.get("session_dir")
    res = data.get("result", {})
    # coordinator.Result carries no json tags, so Go marshals the Go field
    # names verbatim.
    meta["completed"] = len(res.get("Completed") or [])
    meta["failed"] = len(res.get("Failed") or [])
    meta["unreached"] = len(res.get("Unreached") or [])
    meta["timed_out"] = bool(res.get("TimedOut"))
except Exception as e:  # noqa: BLE001 — a crashed run still needs its meta
    meta["parse_error"] = str(e)
json.dump(meta, open(f"{trial}/run_meta.json", "w"), indent=2)
print("narwhal done: " + " ".join(f"{k}={v}" for k, v in meta.items() if k != "session_dir"))
EOF
