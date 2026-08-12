#!/usr/bin/env bash
# Drive the benchmark slice: for each task, run all arms and judge each.
#
# Arms are interleaved per task (b0-t1, narwhal-t1, hybrid-t1, b0-t2, ...) so
# that a mid-run quota cliff does not land systematically on whichever arm was
# scheduled last.
#
# Usage: run_slice.sh <results-dir> <task-id>...
#
# ARMS controls which arms run (default: all three). Set ARMS="b0 narwhal" to
# skip the hybrid arm.
set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
# QA_DIR points at a checkout of Coral-Protocol/AgentRadio's data/qa. The
# tasks are not vendored here: they are ~5 MB of third-party fixtures and
# each one needs a multi-GB Docker image to be useful.
QA="${QA_DIR:-$HERE/../AgentRadio/data/qa}"
REPOS="${REPOS_DIR:-$HERE/repos}"
RESULTS="${1:?usage: run_slice.sh <results-dir> <task-id>...}"
shift
TASKS=("$@")

AGENT_TIMEOUT="${AGENT_TIMEOUT:-1800}"
CONC="${CONC:-3}"
# Narwhal arm defaults to Smart: haiku investigation workers + opus
# synthesis. Set NARWHAL_WORKER_MODEL="" and NARWHAL_SYNTHESIS_MODEL="" to
# revert to uniform ccproxy rotation (the original Narwhal arm).
ARMS="${ARMS:-b0 narwhal hybrid}"

mkdir -p "$RESULTS"

for TASK in "${TASKS[@]}"; do
  TASK_DIR="$QA/$TASK"
  IMG=$(grep docker_image "$TASK_DIR/task.toml" | head -1 | cut -d'"' -f2)
  # One extraction per image, shared by every task that uses it and by both
  # arms — a per-arm copy would differ only in inode, but a per-arm extraction
  # doubles the disk for nothing.
  REPO="$REPOS/$(echo "$IMG" | sed 's|.*:||')"
  bash "$HERE/extract_repo.sh" "$TASK_DIR" "$REPO"

  for ARM in $ARMS; do
    TRIAL="$RESULTS/$TASK/$ARM"
    if [ -f "$TRIAL/verifier/evaluation_results.json" ]; then
      echo "== skip $TASK/$ARM (already judged)"
      continue
    fi
    mkdir -p "$TRIAL"
    echo "== run $TASK/$ARM"
    case "$ARM" in
      b0)      bash "$HERE/run_b0.sh"      "$TASK_DIR" "$REPO" "$TRIAL" "$AGENT_TIMEOUT" || true ;;
      narwhal) bash "$HERE/run_narwhal.sh" "$TASK_DIR" "$REPO" "$TRIAL" "$AGENT_TIMEOUT" "$CONC" || true ;;
      hybrid)  bash "$HERE/run_hybrid.sh"  "$TASK_DIR" "$REPO" "$TRIAL" "$AGENT_TIMEOUT" "$CONC" || true ;;
      *) echo "unknown arm: $ARM" >&2; continue ;;
    esac
    echo "== judge $TASK/$ARM"
    python3 "$HERE/judge.py" "$TASK_DIR" "$TRIAL" || true
  done
done

python3 "$HERE/summarize.py" "$RESULTS"
