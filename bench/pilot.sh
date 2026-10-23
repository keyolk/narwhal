#!/usr/bin/env bash
# The pilot slice: eight runtime-heavy SWE-Atlas QnA tasks, every arm, three
# repeats. Its job is to measure the spread between repeats of one arm, so
# the task count of the full run can be sized from it, and to give a first
# read of narwhal against b0 and the same-budget fanout control.
#
# Tasks were chosen for runtime rubrics (>= 40% of rubrics ask for observed
# output) across six repositories, keeping images small enough to fit the
# disk: drakkan/sftpgo, foxcpp/maddy x2, minio/minio, paperless-ngx x2,
# simple-login/app, kovidgoyal/kitty (the task measure-2 ran).
#
# Usage: pilot.sh [results-dir]   (default bench/results/pilot-1)
#        PREPARE_ONLY=1 pilot.sh  pull images and extract repos, no agents
set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
RESULTS="${1:-$HERE/results/pilot-1}"
export QA_DIR="${QA_DIR:-$HOME/src/AgentRadio/data/qa}"
export REPOS_DIR="${REPOS_DIR:-$HOME/.cache/narwhal-bench/repos}"
# Claude accounts only: a turn routed to another backend is an excluded trial.
export BENCH_ACCOUNTS="${BENCH_ACCOUNTS-claude-sendbird,claude-personal}"
# The judge goes through the same pinned ccproxy (isolate/ccproxy adds the
# accounts). Calling claude directly uses one account and stalls the slice
# when that one is out; every judge call's served model is checked either way.
export JUDGE_CMD="${JUDGE_CMD-ccproxy claude}"

TASKS=(
  task-6905333b74f22949d97baa2a
  task-6905333b74f22949d97baa10
  task-6905333b74f22949d97baa17
  task-6905333b74f22949d97baa1a
  task-6905333b74f22949d97ba9d5
  task-6905333b74f22949d97ba9d4
  task-6905333b74f22949d97ba9ab
  task-6905333b74f22949d97ba9f0
)

if [ -n "${PREPARE_ONLY:-}" ]; then
  for t in "${TASKS[@]}"; do
    img=$(grep docker_image "$QA_DIR/$t/task.toml" | head -1 | cut -d'"' -f2)
    docker pull -q --platform linux/amd64 "$img" >/dev/null
    bash "$HERE/extract_repo.sh" "$QA_DIR/$t" "$REPOS_DIR/${img##*:}"
  done
  exit 0
fi

mkdir -p "$RESULTS"
ARMS="${ARMS:-b0 fanout narwhal narwhal-nogate}" \
REPEATS="${REPEATS:-3}" \
AGENT_TIMEOUT="${AGENT_TIMEOUT:-3600}" \
FANOUT_N="${FANOUT_N:-2}" \
  bash "$HERE/run_slice.sh" "$RESULTS" "${TASKS[@]}"
