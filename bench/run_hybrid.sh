#!/usr/bin/env bash
# Hybrid arm: frontier planner (opus) decomposes, cheap workers (haiku)
# investigate. This is the Cursor economics insight — the planner needs
# frontier intelligence for decomposition and design decisions, workers
# need only follow instructions in a narrow area.
#
# Usage: run_hybrid.sh <task-dir> <repo> <trial-dir> [timeout-sec] [concurrency]
set -euo pipefail

TASK_DIR="${1:?usage: run_hybrid.sh <task-dir> <repo> <trial-dir> [timeout] [conc]}"
REPO="${2:?}"
TRIAL="${3:?}"
TIMEOUT="${4:-1800}"
CONC="${5:-3}"
PLANNER_MODEL="${PLANNER_MODEL:-opus}"
WORKER_MODEL="${WORKER_MODEL:-haiku}"
HERE="$(cd "$(dirname "$0")" && pwd)"

mkdir -p "$TRIAL/agent"

# See run_narwhal.sh for why the answer path has no username in it.
ANSWER_DIR="/tmp/narwhal-bench/$(basename "$(dirname "$TRIAL")")-$(basename "$TRIAL")"
ANSWER="$ANSWER_DIR/answer.txt"
mkdir -p "$ANSWER_DIR"
rm -f "$ANSWER"

python3 "$HERE/make_prompt.py" "$TASK_DIR" "$REPO" "$ANSWER" --arm narwhal \
  > "$TRIAL/prompt.txt"

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
START=$(python3 -c 'import time; print(time.time())')
set +e
gtimeout $(( TIMEOUT + 120 )) narwhal plan \
  --prompt "$(cat "$TRIAL/prompt.txt")" \
  --cwd "$REPO" \
  --concurrency "$CONC" \
  --planner-model "$PLANNER_MODEL" \
  --worker-model "$WORKER_MODEL" \
  --plan-timeout "${PLAN_TIMEOUT}s" \
  --timeout "${TIMEOUT}s" \
  > "$TRIAL/agent/narwhal.json" 2> "$TRIAL/agent/narwhal.log"
RC=$?
set -e
END=$(python3 -c 'import time; print(time.time())')

if [ -f "$ANSWER" ]; then
  cp "$ANSWER" "$TRIAL/agent/answer.txt"
fi

python3 - "$TRIAL" "$START" "$END" "$RC" "$PLANNER_MODEL" "$WORKER_MODEL" <<'EOF'
import json, os, sys
trial, start, end, rc = sys.argv[1], float(sys.argv[2]), float(sys.argv[3]), int(sys.argv[4])
pm, wm = sys.argv[5], sys.argv[6]
meta = {"arm": "hybrid", "planner_model": pm, "worker_model": wm,
        "exit_code": rc, "wall_clock_sec": round(end - start, 1)}
path = f"{trial}/agent/narwhal.json"
try:
    data = json.load(open(path))
    snap = data.get("snapshot", {})
    meta["tasks"] = len(snap.get("tasks", []))
    meta["messages"] = len(snap.get("messages", []))
    meta["session_dir"] = data.get("session_dir")
    res = data.get("result", {})
    meta["completed"] = len(res.get("Completed") or [])
    meta["failed"] = len(res.get("Failed") or [])
    meta["unreached"] = len(res.get("Unreached") or [])
    meta["timed_out"] = bool(res.get("TimedOut"))
except Exception as e:
    meta["parse_error"] = str(e)
json.dump(meta, open(f"{trial}/run_meta.json", "w"), indent=2)
print("hybrid done: " + " ".join(f"{k}={v}" for k, v in meta.items()
      if k not in ("session_dir",)))
EOF
