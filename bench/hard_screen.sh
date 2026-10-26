#!/usr/bin/env bash
# One B0 run over tasks likely to avoid Opus's rubric ceiling. This is a
# selection pass, not evidence in the final arm comparison: choose tasks from
# its scores, then run fresh repeats in a different results directory.
#
# Candidates were fixed before seeing their B0 scores. They are every task not
# in pilot-1 that (a) belongs to one of pilot-1's already cached repositories,
# (b) has at least 11 rubrics, and (c) has at least four rubrics whose text asks
# for runtime output, a run, launch, log, test, observation, or visible screen
# evidence. Keeping the rule in this file makes the selected population and
# the absence of post-score cherry-picking auditable.
#
# Usage: hard_screen.sh [results-dir]  (default: bench/results/hard-screen-1)
set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
RESULTS="${1:-$HERE/results/hard-screen-1}"
export QA_DIR="${QA_DIR:-$HOME/src/AgentRadio/data/qa}"
export REPOS_DIR="${REPOS_DIR:-$HOME/.cache/narwhal-bench/repos}"
export BENCH_ACCOUNTS="${BENCH_ACCOUNTS-claude-sendbird,claude-personal}"
export JUDGE_CMD="${JUDGE_CMD-ccproxy claude}"

TASKS=(
  task-6905333b74f22949d97ba9f8
  task-6905333b74f22949d97baa27
  task-6905333b74f22949d97baa11
  task-6905333b74f22949d97baa05
  task-6905333b74f22949d97ba9db
  task-6905333b74f22949d97ba9a6
  task-6905333b74f22949d97ba9af
  task-6905333b74f22949d97ba9f4
  task-6905333b74f22949d97ba9f2
  task-6905333b74f22949d97ba9ad
  task-6905333b74f22949d97ba9d8
  task-6905333b74f22949d97ba9a5
)

ARMS=b0 REPEATS=1 AGENT_TIMEOUT="${AGENT_TIMEOUT:-3600}" \
  bash "$HERE/run_slice.sh" "$RESULTS" "${TASKS[@]}"
