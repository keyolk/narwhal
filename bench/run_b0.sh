#!/usr/bin/env bash
# B0 baseline arm: one Claude Code session, no coordination.
#
# This mirrors AgentRadio's B0 — a single agent handling the whole question —
# so the multi-agent arm is measured against the thing it has to beat.
#
# Usage: run_b0.sh <task-dir> <repo> <trial-dir> [timeout-sec]
set -euo pipefail

TASK_DIR="${1:?usage: run_b0.sh <task-dir> <repo> <trial-dir> [timeout]}"
REPO="${2:?}"
TRIAL="${3:?}"
TIMEOUT="${4:-1800}"
HERE="$(cd "$(dirname "$0")" && pwd)"

mkdir -p "$TRIAL/agent"
# Absolute, because the agent is started after `cd "$REPO"` and a relative
# trial path would then point inside the repository.
TRIAL="$(cd "$TRIAL" && pwd)"

# Both arms write to a short neutral path and the answer is copied back. See
# run_narwhal.sh for why: a path containing the username can come back with
# its dots rewritten as hyphens, and the two spellings are different
# directories. B0 has not tripped on this, but keeping the arms on identical
# plumbing means a future failure cannot be mistaken for a difference in
# approach.
# Keyed by the last three path parts (<task>/<arm>/r<k>) so repeats and tasks
# never share an answer file.
ANSWER_DIR="/tmp/narwhal-bench/$(echo "$TRIAL" | awk -F/ '{print $(NF-2)"-"$(NF-1)"-"$NF}')"
ANSWER="$ANSWER_DIR/answer.txt"
mkdir -p "$ANSWER_DIR"
rm -f "$ANSWER"

python3 "$HERE/make_prompt.py" "$TASK_DIR" "$REPO" "$ANSWER" --arm b0 \
  > "$TRIAL/prompt.txt"

# The model is pinned and the session id chosen up front. ccproxy maps a tier
# to whichever account has quota, so an unpinned B0 could be served by a
# different model than the arm it is compared against; the session id is how
# meta.py finds the transcript afterwards to check what actually ran.
MODEL="${B0_MODEL:-opus}"
SID=$(python3 -c 'import uuid; print(uuid.uuid4())')

START=$(python3 -c 'import time; print(time.time())')
set +e
# cd into the repository rather than relying on the caller's cwd: the agent's
# working directory is part of what it explores, and inheriting the harness's
# cwd points it at the wrong tree entirely.
#
# gtimeout keeps a wedged session from eating the whole run; --print with
# bypassPermissions is the same headless configuration Narwhal's workers use,
# so the arms differ in coordination rather than in permission handling.
( cd "$REPO" && gtimeout "$TIMEOUT" ccproxy claude --print \
    --model "$MODEL" --session-id "$SID" \
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

python3 "$HERE/meta.py" "$TRIAL" b0 "$START" "$END" "$RC" \
  --tier "$MODEL" --sessions "$SID"
