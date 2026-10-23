#!/usr/bin/env bash
# Drive the benchmark slice: for each task, run all arms and judge each.
#
# Arms are interleaved per task and per repeat (t1/r1: b0 narwhal ..., t1/r2:
# b0 narwhal ..., t2/r1: ...) so that a mid-run quota cliff does not land
# systematically on whichever arm was scheduled last.
#
# Usage: run_slice.sh <results-dir> <task-id>...
#
# ARMS controls which arms run (default: b0 fanout narwhal). REPEATS runs each
# arm that many times per task (default 3): one run per arm cannot tell an
# arm's difference from its own run-to-run spread. Trials land in
# <results>/<task>/<arm>/r<k>.
set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"

# Every trial runs isolated from the caller's Claude configuration — see
# isolated.sh and isolate/claude for what leaked before. The slice re-executes
# itself inside the isolation, passing on only its own knobs; a variable
# deliberately set empty (NARWHAL_WORKER_MODEL="") stays empty.
if [ -z "${BENCH_ISOLATED:-}" ]; then
  FWD=()
  for v in QA_DIR REPOS_DIR AGENT_TIMEOUT CONC ARMS REPEATS B0_MODEL \
           FANOUT_N FANOUT_MODEL NARWHAL_WORKER_MODEL NARWHAL_SYNTHESIS_MODEL \
           JUDGE_MODEL JUDGE_CMD BENCH_NO_ENV BENCH_ACCOUNTS QUOTA_RETRIES QUOTA_WAIT; do
    if [ -n "${!v+x}" ]; then FWD+=("$v=${!v}"); fi
  done
  exec "$HERE/isolated.sh" env ${FWD[@]+"${FWD[@]}"} bash "$HERE/run_slice.sh" "$@"
fi
# QA_DIR points at a checkout of Coral-Protocol/AgentRadio's data/qa. The
# tasks are not vendored here: they are ~5 MB of third-party fixtures and
# each one needs a multi-GB Docker image to be useful.
QA="${QA_DIR:-$HERE/../AgentRadio/data/qa}"
# Repositories live outside this checkout. Agents run in them with full read
# access, and from bench/repos/<x> the results directory — other arms'
# answers to the same question — is two levels up.
REPOS="${REPOS_DIR:-$HOME/.cache/narwhal-bench/repos}"
RESULTS="${1:?usage: run_slice.sh <results-dir> <task-id>...}"
shift
TASKS=("$@")

AGENT_TIMEOUT="${AGENT_TIMEOUT:-1800}"
CONC="${CONC:-3}"
# Narwhal arm defaults to Smart: haiku investigation workers + opus
# synthesis. Set NARWHAL_WORKER_MODEL="" and NARWHAL_SYNTHESIS_MODEL="" to
# revert to uniform ccproxy rotation (the original Narwhal arm). The `-`
# expansion keeps an explicitly empty value empty.
export NARWHAL_WORKER_MODEL="${NARWHAL_WORKER_MODEL-haiku}"
export NARWHAL_SYNTHESIS_MODEL="${NARWHAL_SYNTHESIS_MODEL-opus}"
ARMS="${ARMS:-b0 fanout narwhal}"
REPEATS="${REPEATS:-3}"
QUOTA_RETRIES="${QUOTA_RETRIES:-8}"
QUOTA_WAIT="${QUOTA_WAIT:-1800}"

mkdir -p "$RESULTS"

# Build narwhal from this checkout into the results directory and run that.
# The copy on PATH is whatever was installed last, by any session: measure-2
# ran against a binary another session had reinstalled from main mid-slice,
# without the planner accounting this harness depends on. A slice's binary is
# part of what it measured, so it is built once, kept with the results, and
# its version recorded.
RESULTS="$(cd "$RESULTS" && pwd)"
BIN="$RESULTS/.bin"
mkdir -p "$BIN"
VERSION="$(git -C "$HERE/.." rev-parse --short HEAD)$(git -C "$HERE/.." diff --quiet HEAD -- . ':!bench' || echo -dirty)"
( cd "$HERE/.." && go build -ldflags "-X main.version=$VERSION" -o "$BIN/narwhal" ./cmd/narwhal )
export PATH="$BIN:$PATH"
echo "narwhal $VERSION -> $BIN/narwhal"
echo "$VERSION" > "$RESULTS/narwhal-version.txt"

# narwhal's settings for the slice: the operator's own file, with the planner's
# history digest off. Same question, same directory: with it on, a repeat's
# planner is shown how the previous trial's tasks ended. The copy also pins
# the settings — a `narwhal config set` elsewhere mid-slice does not reach it.
# config-nogate.json is the same with the gave-up gate off, for the
# narwhal-nogate arm.
python3 - "$BIN" <<'PY'
import json, os, sys
src = os.path.expanduser("~/.narwhal/config.json")
base = json.load(open(src)) if os.path.exists(src) else {}
base["plan.history"] = False
json.dump(base, open(os.path.join(sys.argv[1], "config.json"), "w"), indent=2)
json.dump({**base, "jev.gave-up.enabled": False},
          open(os.path.join(sys.argv[1], "config-nogate.json"), "w"), indent=2)
PY
export NARWHAL_CONFIG="$BIN/config.json"

for TASK in "${TASKS[@]}"; do
  TASK_DIR="$QA/$TASK"
  IMG=$(grep docker_image "$TASK_DIR/task.toml" | head -1 | cut -d'"' -f2)
  # One extraction per image, shared by every task that uses it and by both
  # arms — a per-arm copy would differ only in inode, but a per-arm extraction
  # doubles the disk for nothing.
  REPO="$REPOS/$(echo "$IMG" | sed 's|.*:||')"
  bash "$HERE/extract_repo.sh" "$TASK_DIR" "$REPO"

  for K in $(seq 1 "$REPEATS"); do
    for ARM in $ARMS; do
      TRIAL="$RESULTS/$TASK/$ARM/r$K"
      if [ -f "$TRIAL/verifier/evaluation_results.json" ]; then
        echo "== skip $TASK/$ARM/r$K (already judged)"
        continue
      fi
      # The agent ran but the judge did not finish (quota, model drift):
      # judge again, do not pay for the agent twice.
      if [ -f "$TRIAL/run_meta.json" ]; then
        echo "== rejudge $TASK/$ARM/r$K"
        python3 "$HERE/judge.py" "$TASK_DIR" "$TRIAL" || true
        continue
      fi
      # An attempt that stopped on an account limit measured the account, not
      # the arm. It is kept beside the trial for the record, and the trial is
      # run again after a wait, up to QUOTA_RETRIES times.
      attempt=0
      while :; do
        mkdir -p "$TRIAL"
        echo "== run $TASK/$ARM/r$K"
        case "$ARM" in
          b0)      bash "$HERE/run_b0.sh"      "$TASK_DIR" "$REPO" "$TRIAL" "$AGENT_TIMEOUT" || true ;;
          fanout)  bash "$HERE/run_fanout.sh"  "$TASK_DIR" "$REPO" "$TRIAL" "$AGENT_TIMEOUT" || true ;;
          narwhal) bash "$HERE/run_narwhal.sh" "$TASK_DIR" "$REPO" "$TRIAL" "$AGENT_TIMEOUT" "$CONC" || true ;;
          narwhal-nogate)
            NARWHAL_CONFIG="$BIN/config-nogate.json" ARM_NAME=narwhal-nogate \
              bash "$HERE/run_narwhal.sh" "$TASK_DIR" "$REPO" "$TRIAL" "$AGENT_TIMEOUT" "$CONC" || true ;;
          hybrid)  bash "$HERE/run_hybrid.sh"  "$TASK_DIR" "$REPO" "$TRIAL" "$AGENT_TIMEOUT" "$CONC" || true ;;
          *) echo "unknown arm: $ARM" >&2; continue 2 ;;
        esac
        if python3 -c 'import json,sys; sys.exit(0 if json.load(open(sys.argv[1])).get("quota_hit") else 1)' "$TRIAL/run_meta.json" 2>/dev/null; then
          attempt=$(( attempt + 1 ))
          mv "$TRIAL" "$TRIAL.quota-$(date +%s)"
          if [ "$attempt" -ge "$QUOTA_RETRIES" ]; then
            echo "== quota: giving up on $TASK/$ARM/r$K after $attempt attempts"
            continue 2
          fi
          echo "== quota: $TASK/$ARM/r$K hit an account limit; retrying in ${QUOTA_WAIT}s"
          sleep "$QUOTA_WAIT"
          continue
        fi
        break
      done
      echo "== judge $TASK/$ARM/r$K"
      python3 "$HERE/judge.py" "$TASK_DIR" "$TRIAL" || true
    done
  done
done

python3 "$HERE/summarize.py" "$RESULTS"
