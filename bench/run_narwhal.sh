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
# Absolute, because the agent is started after `cd "$REPO"` and a relative
# trial path would then point inside the repository.
TRIAL="$(cd "$TRIAL" && pwd)"

# The answer is written to a short neutral path, not into the trial directory,
# and copied back afterwards.
#
# Why: Claude Code's scratchpad path embeds the username with dots replaced by
# hyphens (/private/tmp/claude-<uid>/-Users-jane-doe/...), but a worker
# reconstructing a path under its own $HOME writes the dotted form
# (-Users-jane.doe). Both directories then exist, the worker reports
# success, and the judge finds nothing. A path with no username in it cannot
# be rewritten this way.
# Keyed by the last three path parts (<task>/<arm>/r<k>) so repeats and tasks
# never share an answer file.
ANSWER_DIR="/tmp/narwhal-bench/$(echo "$TRIAL" | awk -F/ '{print $(NF-2)"-"$(NF-1)"-"$NF}')"
ANSWER="$ANSWER_DIR/answer.txt"
mkdir -p "$ANSWER_DIR"
rm -f "$ANSWER"

python3 "$HERE/make_prompt.py" "$TASK_DIR" "$REPO" "$ANSWER" --arm narwhal \
  > "$TRIAL/prompt.txt"

# The DAG completing is not the same as the artifact existing: without an
# explicit synthesis contract the workers each report to the radio and nobody
# writes the file the judge reads.
#
# Only the file is added here. How the synthesis is wired — deps on every
# task, task-done as the completion gate — is the planner's own contract
# (server/plan_instructions.go). This block used to also ask for a synthesis
# with no deps that polled the state script, which E9 showed does not make a
# worker wait; every October batch run copied it into the synthesis
# assignment, contradicting the planner's instructions.
cat >> "$TRIAL/prompt.txt" <<EOF

## Answer file

The synthesis task's assignment must also state that it writes the complete
answer to ${ANSWER}, wrapped in <<FINAL_ANSWER>> tags, using the exact format
given above, and that it keeps the concrete details peers reported (file
paths, function names, exact values) rather than summarizing them away.

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
# Remove the scratch copy once it is in the trial: a later trial's agent
# explores with full read access, and the same question's answer from another
# arm sitting in /tmp is one `ls` away.
rm -rf "$ANSWER_DIR"

python3 "$HERE/meta.py" "$TRIAL" "${ARM_NAME:-narwhal}" "$START" "$END" "$RC" \
  --narwhal "$TRIAL/agent/narwhal.json" \
  --worker-tier "$WORKER_MODEL" --synthesis-tier "$SYNTHESIS_MODEL"
