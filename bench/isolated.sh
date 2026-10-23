#!/usr/bin/env bash
# Run a command the way a benchmark trial must run: with the isolating
# `claude` shim first on PATH and an environment that carries nothing from
# the session that started it.
#
# The environment matters as much as the settings files. A slice started from
# inside a Claude Code session inherits that session's variables, and they
# change how every agent in the slice behaves: CLAUDE_CODE_PLUGIN_DIRS loads
# a plugin with its own hooks, CLAUDE_CODE_DISABLE_ADAPTIVE_THINKING and
# CLAUDE_EFFORT change how the model thinks, and CLAUDE_CODE_SESSION_ID makes
# the child look like part of the parent. Starting from `env -i` and adding
# back only what tools need to find themselves avoids naming each one.
#
# Usage: isolated.sh <command> [args...]
set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
SHIM="$HERE/isolate"

# Resolve the real binary with the shim off PATH, so a nested call does not
# resolve to the shim itself.
CLEAN_PATH=$(printf '%s' "$PATH" | tr ':' '\n' | grep -vx "$SHIM" | paste -sd: -)
REAL=$(PATH="$CLEAN_PATH" command -v claude) || { echo "claude not on PATH" >&2; exit 1; }
REAL_CCPROXY=$(PATH="$CLEAN_PATH" command -v ccproxy) || { echo "ccproxy not on PATH" >&2; exit 1; }

exec env -i \
  HOME="$HOME" USER="${USER:-}" LOGNAME="${LOGNAME:-${USER:-}}" \
  TMPDIR="${TMPDIR:-/tmp}" TERM=dumb LANG="${LANG:-en_US.UTF-8}" \
  SHELL=/bin/bash \
  PATH="$SHIM:$CLEAN_PATH" \
  BENCH_REAL_CLAUDE="$REAL" BENCH_REAL_CCPROXY="$REAL_CCPROXY" BENCH_ISOLATED=1 \
  ${BENCH_ACCOUNTS:+BENCH_ACCOUNTS="$BENCH_ACCOUNTS"} \
  "$@"
