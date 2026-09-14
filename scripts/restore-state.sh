#!/usr/bin/env bash
# Copyright 2026 Zyvor AI Labs · https://zyvor.dev
# SPDX-License-Identifier: Apache-2.0
# Restore a Nodra control-plane data directory from scripts/backup-state.sh output.
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: scripts/restore-state.sh <archive.tar.gz> <data-dir>

Stops nothing by itself — stop nodra-server (single writer) before restoring.
Never mount the same WAL directory on two live control-plane processes.
After restore, start one replica and check GET /readyz.
EOF
}

if [[ "${1:-}" == "-h" || "${1:-}" == "--help" || $# -ne 2 ]]; then
  usage
  exit 0
fi

ARCHIVE=$(python3 -c 'import os,sys; print(os.path.abspath(sys.argv[1]))' "$1")
DATA_DIR=$2
mkdir -p "$DATA_DIR"
DATA_DIR=$(cd "$DATA_DIR" && pwd)

if [[ ! -f "$ARCHIVE" ]]; then
  echo "archive not found: $ARCHIVE" >&2
  exit 1
fi

# Refuse restore into a non-empty live-looking tree unless forced.
if [[ -z "${NODRA_RESTORE_FORCE:-}" ]]; then
  if [[ -e "$DATA_DIR/state.wal" || -e "$DATA_DIR/state.snapshot.json" || -d "$DATA_DIR/deliveries" ]]; then
    echo "refusing to overwrite existing Nodra data in $DATA_DIR" >&2
    echo "set NODRA_RESTORE_FORCE=1 after stopping the writer if this is intentional" >&2
    exit 1
  fi
fi

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
tar -C "$tmp" -xzf "$ARCHIVE"
# Wipe target only when forced and we already checked operator intent.
if [[ -n "${NODRA_RESTORE_FORCE:-}" ]]; then
  find "$DATA_DIR" -mindepth 1 -maxdepth 1 -exec rm -rf {} +
fi
cp -a "$tmp"/. "$DATA_DIR"/
echo "restored $ARCHIVE → $DATA_DIR"
echo "start one nodra-server and verify: curl -fsS http://127.0.0.1:PORT/readyz"
