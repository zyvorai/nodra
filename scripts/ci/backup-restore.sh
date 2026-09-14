#!/usr/bin/env bash
# Copyright 2026 Zyvor AI Labs · https://zyvor.dev
# SPDX-License-Identifier: Apache-2.0
# CI: backup/restore round-trip for Nodra control-plane data.
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/../.." && pwd)
cd "$ROOT"
TMP=$(mktemp -d)
trap 'kill ${PID:-0} 2>/dev/null || true; rm -rf "$TMP"' EXIT
make build
PORT=$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()')
DATA="$TMP/control"
mkdir -p "$DATA"
export NODRA_ADMIN_TOKEN=admin-backup
export NODRA_ENROLLMENT_TOKEN=enroll-backup
./bin/nodra-server --listen "127.0.0.1:${PORT}" --data "$DATA" >"$TMP/server.log" 2>&1 &
PID=$!
for i in $(seq 1 50); do
  curl -fsS "http://127.0.0.1:${PORT}/readyz" >/dev/null 2>&1 && break
  sleep 0.2
done
curl -fsS "http://127.0.0.1:${PORT}/readyz" >/dev/null
curl -fsS -X POST "http://127.0.0.1:${PORT}/api/v1/auth/login" \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"admin-backup"}' >/dev/null
kill "$PID"
wait "$PID" 2>/dev/null || true
PID=0
./scripts/backup-state.sh "$DATA" "$TMP/backup.tar.gz"
RESTORE="$TMP/restore"
mkdir -p "$RESTORE"
NODRA_RESTORE_FORCE=1 ./scripts/restore-state.sh "$TMP/backup.tar.gz" "$RESTORE"
./bin/nodra-server --listen "127.0.0.1:${PORT}" --data "$RESTORE" >"$TMP/server2.log" 2>&1 &
PID=$!
for i in $(seq 1 50); do
  curl -fsS "http://127.0.0.1:${PORT}/readyz" >/dev/null 2>&1 && break
  sleep 0.2
done
curl -fsS "http://127.0.0.1:${PORT}/readyz" >/dev/null
echo "PASS: nodra backup-restore"
