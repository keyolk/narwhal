#!/usr/bin/env bash
# Fan-out arm: N independent B0 sessions in parallel, then one session merges
# their answers.
#
# This is the control Narwhal most needs to beat. Narwhal spends more tokens
# than B0 and runs agents in parallel, so beating B0 alone does not show that
# the planner, the radio or the synthesis contract earn anything — "run it
# several times and combine" might do as well with no coordination at all.
# Set FANOUT_N so this arm's spend lands near Narwhal's; the comparison is
# then coordination against no coordination at similar cost.
#
# Usage: run_fanout.sh <task-dir> <repo> <trial-dir> [timeout-sec]
set -euo pipefail

TASK_DIR="${1:?usage: run_fanout.sh <task-dir> <repo> <trial-dir> [timeout]}"
REPO="${2:?}"
TRIAL="${3:?}"
TIMEOUT="${4:-1800}"
N="${FANOUT_N:-3}"
MODEL="${FANOUT_MODEL:-opus}"
HERE="$(cd "$(dirname "$0")" && pwd)"

mkdir -p "$TRIAL/agent"
# Absolute, because the agent is started after `cd "$REPO"` and a relative
# trial path would then point inside the repository.
TRIAL="$(cd "$TRIAL" && pwd)"

# Neutral answer paths for the same reason as run_b0.sh: a path containing the
# username can come back with its dots rewritten as hyphens.
# Keyed by the last three path parts (<task>/<arm>/r<k>) so repeats and tasks
# never share an answer file.
ANSWER_DIR="/tmp/narwhal-bench/$(echo "$TRIAL" | awk -F/ '{print $(NF-2)"-"$(NF-1)"-"$NF}')"
ANSWER="$ANSWER_DIR/answer.txt"
mkdir -p "$ANSWER_DIR"
rm -f "$ANSWER" "$ANSWER_DIR"/answer-*.txt

SIDS=()
START=$(python3 -c 'import time; print(time.time())')

# The merge has to fit in the same budget as the other arms, so the
# independent runs get the larger share and the merge what is left.
FAN_TIMEOUT=$(( TIMEOUT * 3 / 4 ))
MERGE_TIMEOUT=$(( TIMEOUT - FAN_TIMEOUT ))

PIDS=()
for i in $(seq 1 "$N"); do
  SID=$(python3 -c 'import uuid; print(uuid.uuid4())')
  SIDS+=("$SID")
  python3 "$HERE/make_prompt.py" "$TASK_DIR" "$REPO" "$ANSWER_DIR/answer-$i.txt" --arm b0 \
    > "$TRIAL/prompt-$i.txt"
  ( cd "$REPO" && gtimeout "$FAN_TIMEOUT" ccproxy claude --print \
      --model "$MODEL" --session-id "$SID" \
      --permission-mode bypassPermissions \
      < "$TRIAL/prompt-$i.txt" ) > "$TRIAL/agent/stdout-$i.txt" 2> "$TRIAL/agent/stderr-$i.txt" &
  PIDS+=($!)
done
set +e
for p in "${PIDS[@]}"; do wait "$p"; done
set -e

GOT=0
for i in $(seq 1 "$N"); do
  if [ -f "$ANSWER_DIR/answer-$i.txt" ]; then
    cp "$ANSWER_DIR/answer-$i.txt" "$TRIAL/agent/answer-$i.txt"
    GOT=$(( GOT + 1 ))
  fi
done

# The merge sees the original question and every answer that was written.
# It keeps repository access so it can settle a disagreement by reading the
# code, which is what Narwhal's synthesis task is allowed to do too.
python3 "$HERE/make_prompt.py" "$TASK_DIR" "$REPO" "$ANSWER" --arm b0 > "$TRIAL/prompt.txt"
{
  echo
  echo "## Independent answers"
  echo
  echo "$GOT other sessions answered this question independently. Their answers"
  echo "are below. Write one final answer that keeps every concrete, correct"
  echo "detail any of them found (file paths, function names, exact values)."
  echo "Where they disagree, read the code to decide; do not average."
  for i in $(seq 1 "$N"); do
    if [ -f "$ANSWER_DIR/answer-$i.txt" ]; then
      echo
      echo "### Answer $i"
      echo
      cat "$ANSWER_DIR/answer-$i.txt"
    fi
  done
} >> "$TRIAL/prompt.txt"

MSID=$(python3 -c 'import uuid; print(uuid.uuid4())')
SIDS+=("$MSID")
set +e
( cd "$REPO" && gtimeout "$MERGE_TIMEOUT" ccproxy claude --print \
    --model "$MODEL" --session-id "$MSID" \
    --permission-mode bypassPermissions \
    < "$TRIAL/prompt.txt" ) > "$TRIAL/agent/stdout.txt" 2> "$TRIAL/agent/stderr.txt"
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

python3 "$HERE/meta.py" "$TRIAL" fanout "$START" "$END" "$RC" \
  --tier "$MODEL" --sessions "${SIDS[@]}"
