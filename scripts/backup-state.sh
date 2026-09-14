#!/usr/bin/env bash
# Copyright 2026 Zyvor AI Labs · https://zyvor.dev
# SPDX-License-Identifier: Apache-2.0
# Backup a Nodra control-plane data directory (file store + local delivery/DLQ WALs).
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: scripts/backup-state.sh <data-dir> [archive.tar.gz]

Creates a gzip tarball of the Nodra data directory. Prefer stopping the single
control-plane writer first (or take a filesystem snapshot) so WAL files are
quiescent. Activity/Logs ring is in-memory and is never part of the backup.
EOF
}

if [[ "${1:-}" == "-h" || "${1:-}" == "--help" || $# -lt 1 ]]; then
  usage
  exit 0
fi

DATA_DIR=$(cd "$1" && pwd)
STAMP=$(date -u +%Y%m%dT%H%M%SZ)
OUT=${2:-"nodra-backup-${STAMP}.tar.gz"}
OUT=$(python3 -c 'import os,sys; print(os.path.abspath(sys.argv[1]))' "$OUT")

tar -C "$DATA_DIR" -czf "$OUT" .
(
  cd "$DATA_DIR"
  find . -type f | LC_ALL=C sort | while read -r f; do
    sha256sum "$f"
  done
) >"${OUT}.SHA256SUMS"

echo "wrote $OUT"
echo "wrote ${OUT}.SHA256SUMS"
ls -lh "$OUT"
