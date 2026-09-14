#!/usr/bin/env bash
# Copyright 2026 Zyvor AI Labs · https://zyvor.dev
# SPDX-License-Identifier: Apache-2.0
# CI: docker compose stack smoke for Nodra.
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/../.." && pwd)
cd "$ROOT"
cleanup() { docker compose down -v --remove-orphans >/dev/null 2>&1 || true; }
trap cleanup EXIT
export NODRA_ADMIN_TOKEN=nodra-demo-admin
export NODRA_ADMIN_PASSWORD=nodra-demo-admin
export NODRA_ENROLLMENT_TOKEN=nodra-demo-enroll
docker compose up --build -d
for i in $(seq 1 60); do
  if curl -fsS http://127.0.0.1:8080/healthz >/dev/null 2>&1; then
    break
  fi
  sleep 2
done
curl -fsS http://127.0.0.1:8080/healthz >/dev/null
curl -fsS http://127.0.0.1:8080/readyz >/dev/null
LOGIN=$(curl -fsS -X POST http://127.0.0.1:8080/api/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"nodra-demo-admin"}')
echo "$LOGIN" | grep -Fq '"token"'
TOKEN=$(python3 -c 'import json,sys; print(json.load(sys.stdin)["token"])' <<<"$LOGIN")
curl -fsS -H "Authorization: Bearer $TOKEN" http://127.0.0.1:8080/api/v1/overview >/dev/null
echo "PASS: nodra compose-stack"
