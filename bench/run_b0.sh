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

# Both arms write to a short neutral path and the answer is copied back. See
# run_narwhal.sh for why: a path containing the username can come back with
# its dots rewritten as hyphens, and the two spellings are different
# directories. B0 has not tripped on this, but keeping the arms on identical
# plumbing means a future failure cannot be mistaken for a difference in
# approach.
ANSWER_DIR="/tmp/narwhal-bench/$(basename "$(dirname "$TRIAL")")-$(basename "$TRIAL")"
ANSWER="$ANSWER_DIR/answer.txt"
mkdir -p "$ANSWER_DIR"
rm -f "$ANSWER"

python3 "$HERE/make_prompt.py" "$TASK_DIR" "$REPO" "$ANSWER" --arm b0 \
  > "$TRIAL/prompt.txt"

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
    --permission-mode bypassPermissions \
    < "$TRIAL/prompt.txt" ) > "$TRIAL/agent/stdout.txt" 2> "$TRIAL/agent/stderr.txt"
RC=$?
set -e
END=$(python3 -c 'import time; print(time.time())')

if [ -f "$ANSWER" ]; then
  cp "$ANSWER" "$TRIAL/agent/answer.txt"
fi

python3 - "$TRIAL" "$START" "$END" "$RC" <<'EOF'
import json, sys
trial, start, end, rc = sys.argv[1], float(sys.argv[2]), float(sys.argv[3]), int(sys.argv[4])
json.dump({"arm": "b0", "exit_code": rc, "wall_clock_sec": round(end - start, 1)},
          open(f"{trial}/run_meta.json", "w"), indent=2)
print(f"b0 done: rc={rc} wall={end-start:.0f}s")
EOF
