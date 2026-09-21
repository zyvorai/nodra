#!/usr/bin/env bash
# Copyright 2026 Zyvor AI Labs · https://zyvor.dev
# SPDX-License-Identifier: Apache-2.0
# Logical backup of a Nodra PostgreSQL database. This is a pg_dump, not
# continuous WAL archiving. Point-in-time recovery is a PostgreSQL operator
# setting and is not performed by this script.
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: NODRA_DATABASE_URL=postgres://... scripts/backup-postgres.sh [out-dir]

Writes nodra-<utc>.dump (custom format) and a sha256 file. The URL is not
printed. Stop writers first if you need a quiescent snapshot; pg_dump is
consistent for a single database but it is not PITR.
EOF
}

if [[ "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
  usage
  exit 0
fi
if [[ -z "${NODRA_DATABASE_URL:-}" ]]; then
  echo "NODRA_DATABASE_URL is required" >&2
  exit 2
fi
command -v pg_dump >/dev/null || { echo "pg_dump is required" >&2; exit 2; }

OUT_DIR=${1:-.}
mkdir -p "$OUT_DIR"
STAMP=$(date -u +%Y%m%dT%H%M%SZ)
OUT="$OUT_DIR/nodra-${STAMP}.dump"
pg_dump --format=custom --no-password --file="$OUT" "$NODRA_DATABASE_URL"
SUM=$(sha256sum "$OUT" | awk '{print $1}')
printf '%s  %s\n' "$SUM" "$(basename "$OUT")" >"${OUT}.sha256"
echo "wrote $OUT"
echo "wrote ${OUT}.sha256"
