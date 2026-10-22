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
# Absolute, because the agent is started after `cd "$REPO"` and a relative
# trial path would then point inside the repository.
TRIAL="$(cd "$TRIAL" && pwd)"

# See run_narwhal.sh for why the answer path has no username in it.
# Keyed by the last three path parts (<task>/<arm>/r<k>) so repeats and tasks
# never share an answer file.
ANSWER_DIR="/tmp/narwhal-bench/$(echo "$TRIAL" | awk -F/ '{print $(NF-2)"-"$(NF-1)"-"$NF}')"
ANSWER="$ANSWER_DIR/answer.txt"
mkdir -p "$ANSWER_DIR"
rm -f "$ANSWER"

python3 "$HERE/make_prompt.py" "$TASK_DIR" "$REPO" "$ANSWER" --arm narwhal \
  > "$TRIAL/prompt.txt"

cat >> "$TRIAL/prompt.txt" <<EOF

## Answer file

The synthesis task's assignment must also state that it writes the complete
answer to ${ANSWER}, wrapped in <<FINAL_ANSWER>> tags, using the exact format
given above, and that it keeps the concrete details peers reported (file
paths, function names, exact values) rather than summarizing them away.

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
# Remove the scratch copy once it is in the trial: a later trial's agent
# explores with full read access, and the same question's answer from another
# arm sitting in /tmp is one `ls` away.
rm -rf "$ANSWER_DIR"

python3 "$HERE/meta.py" "$TRIAL" hybrid "$START" "$END" "$RC" \
  --narwhal "$TRIAL/agent/narwhal.json" \
  --planner-tier "$PLANNER_MODEL" --worker-tier "$WORKER_MODEL"
