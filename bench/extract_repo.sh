#!/usr/bin/env bash
# Extract a task's /app repository from its Docker image onto the host.
#
# Both arms read the same host copy, so neither is advantaged by a warmer
# checkout. Extraction (rather than running agents inside the container)
# is what makes the host-installed ccproxy/claude usable at all.
#
# Usage: extract_repo.sh <task-dir> <dest>
set -euo pipefail

TASK_DIR="${1:?usage: extract_repo.sh <task-dir> <dest>}"
DEST="${2:?usage: extract_repo.sh <task-dir> <dest>}"

IMG=$(grep docker_image "$TASK_DIR/task.toml" | head -1 | cut -d'"' -f2)
[ -n "$IMG" ] || { echo "no docker_image in $TASK_DIR/task.toml" >&2; exit 1; }

if [ -d "$DEST/.extracted" ]; then
  echo "already extracted: $DEST"
  exit 0
fi

mkdir -p "$DEST"
CID=$(docker create "$IMG" /bin/true)
trap 'docker rm -f "$CID" >/dev/null 2>&1 || true' EXIT
docker cp "$CID:/app/." "$DEST/"
mkdir -p "$DEST/.extracted"
echo "extracted $IMG:/app -> $DEST"
