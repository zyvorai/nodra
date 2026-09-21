#!/usr/bin/env bash
# Copyright 2026 Zyvor AI Labs · https://zyvor.dev
# SPDX-License-Identifier: Apache-2.0
# Restore a dump from scripts/backup-postgres.sh into an empty or forced database.
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: NODRA_DATABASE_URL=postgres://... scripts/restore-postgres.sh <file.dump>

Stop the control plane first. Refuses to run unless NODRA_RESTORE_FORCE=1,
because pg_restore --clean drops objects that are already in the target.
This does not replay WAL and cannot restore to an arbitrary point in time.
EOF
}

if [[ "${1:-}" == "-h" || "${1:-}" == "--help" || $# -ne 1 ]]; then
  usage
  exit 0
fi
if [[ -z "${NODRA_DATABASE_URL:-}" ]]; then
  echo "NODRA_DATABASE_URL is required" >&2
  exit 2
fi
if [[ -z "${NODRA_RESTORE_FORCE:-}" ]]; then
  echo "set NODRA_RESTORE_FORCE=1 after stopping nodra-server" >&2
  exit 1
fi
command -v pg_restore >/dev/null || { echo "pg_restore is required" >&2; exit 2; }
[[ -f "$1" ]] || { echo "dump not found: $1" >&2; exit 1; }

pg_restore --clean --if-exists --no-owner --no-password --dbname="$NODRA_DATABASE_URL" "$1"
echo "restored $1"
