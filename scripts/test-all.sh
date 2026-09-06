#!/usr/bin/env bash
# Copyright 2026 Zyvor AI Labs · https://zyvor.dev
# SPDX-License-Identifier: Apache-2.0
# test-all.sh — Full local + remote verification for Nodra
# ============================================================================
# Usage:
#   ./scripts/test-all.sh
#   ./scripts/test-all.sh --port 20059
#   ./scripts/test-all.sh --host 212.8.248.187 --port 20059 --skip-deploy
#   NODRA_PORT=20059 ./scripts/test-all.sh
#
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
# shellcheck source=lib/ports.sh
source "$ROOT/scripts/lib/ports.sh"

PORT_FROM_CLI=""
HOST_FROM_CLI=""
SKIP_DEPLOY=0
SKIP_REMOTE=0
while [ $# -gt 0 ]; do
  case "$1" in
    --port) PORT_FROM_CLI="$2"; shift 2 ;;
    --port=*) PORT_FROM_CLI="${1#*=}"; shift ;;
    --host) HOST_FROM_CLI="$2"; shift 2 ;;
    --host=*) HOST_FROM_CLI="${1#*=}"; shift ;;
    --skip-deploy) SKIP_DEPLOY=1; shift ;;
    --skip-remote) SKIP_REMOTE=1; shift ;;
    --help|-h) sed -n '2,16p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "Unknown option: $1" >&2; exit 2 ;;
  esac
done

[ -n "$HOST_FROM_CLI" ] && NODRA_HOST="$HOST_FROM_CLI"
nodra_load_last "$ROOT"
PORT="$(nodra_resolve_port "$PORT_FROM_CLI" "${NODRA_LAST_PORT:-}")"
HOST="$(nodra_resolve_host)"
if [ -n "$PORT" ] && ! nodra_validate_port "$PORT"; then
  echo "Invalid port: $PORT" >&2
  exit 2
fi

pass() { printf '  ✅ %s\n' "$*"; }
step() { printf '\n══ %s ══\n' "$*"; }

cd "$ROOT"

step "go fmt / vet / test"
test -z "$(gofmt -l .)" || { echo "gofmt needed:"; gofmt -l .; exit 1; }
pass "gofmt clean"
go vet ./...
pass "go vet"
go test ./... -count=1
pass "go test"

step "build"
make build
pass "binaries built (server, nodrad, nodractl, nodra-sim)"

step "local smoke (ports NODRA_SMOKE_SERVER_PORT / NODRA_SMOKE_AGENT_PORT)"
./scripts/smoke.sh
pass "local smoke"

step "helm lint + demo template"
helm lint charts/nodra --set secrets.adminToken=test-admin --set secrets.enrollmentToken=test-enroll >/dev/null
helm template nodra charts/nodra -f charts/nodra/values-demo.yaml \
  --set image.repository=nodra --set image.tag=demo \
  --set service.port=8080 >/dev/null
# Optional NodePort path (configurable public port)
helm template nodra charts/nodra \
  --set secrets.adminToken=t --set secrets.enrollmentToken=t \
  --set service.type=NodePort --set service.port=8080 --set service.nodePort=30059 >/dev/null
pass "helm lint/template (ClusterIP + NodePort)"

if [ "$SKIP_REMOTE" = 1 ]; then
  echo "  ⏭ skipped remote deploy/smoke (--skip-remote)"
  echo "  ✨ local test-all OK"
  exit 0
fi

[ -n "$HOST" ] || { echo "Set --host / NODRA_HOST or deploy once to create .deploy-last" >&2; exit 2; }
[ -n "$PORT" ] || { echo "Set --port / NODRA_PORT" >&2; exit 2; }
DEPLOY_USER="$(awk -F= '/^USER=/ {print $2; exit}' "$ROOT/.deploy-last" 2>/dev/null || true)"
DEPLOY_USER="${DEPLOY_USER:-sus}"

if [ "$SKIP_DEPLOY" = 0 ]; then
  step "deploy-remote ${HOST} --port ${PORT}"
  ./scripts/deploy-remote.sh "$HOST" "$DEPLOY_USER" --port "$PORT"
  pass "deployed on ${HOST}:${PORT}"
else
  step "skip deploy — smoke against ${HOST}:${PORT}"
fi

BASE="http://${HOST}:${PORT}"
step "smoke-remote ${BASE}"
NODRA_URL="$BASE" ./scripts/smoke-remote.sh --port "$PORT"
pass "smoke-remote"

step "activity + A–Z sim seed (once, 3 sites for speed)"
export NODRA_ADMIN_TOKEN="${NODRA_ADMIN_TOKEN:-nodra-lab-admin}"
export NODRA_ENROLLMENT_TOKEN="${NODRA_ENROLLMENT_TOKEN:-nodra-lab-enroll}"
./bin/nodra-sim --server "$BASE" --once --sites 3 --state /tmp/nodra-test-all-sim.json
OV="$(curl -fsS -H "Authorization: Bearer ${NODRA_ADMIN_TOKEN}" "${BASE}/api/v1/overview")"
echo "$OV" | grep -Eq '"sites":[1-9]' || { echo "overview sites missing: $OV" >&2; exit 1; }
ACT="$(curl -fsS -H "Authorization: Bearer ${NODRA_ADMIN_TOKEN}" "${BASE}/api/v1/activity?limit=20")"
echo "$ACT" | grep -Eq 'chapter|seed|Site' || { echo "activity empty: $ACT" >&2; exit 1; }
pass "sim + activity logs"

step "demo-client against ${BASE}"
NODRA_URL="$BASE" NODRA_ADMIN_TOKEN="$NODRA_ADMIN_TOKEN" NODRA_ENROLLMENT_TOKEN="$NODRA_ENROLLMENT_TOKEN" \
  ./scripts/demo-client.sh --port "$PORT"
pass "demo-client"

HTML="$(curl -fsS "$BASE/")"
grep -Fq 'data-tab="logs"' <<<"$HTML" || { echo "Logs tab missing" >&2; exit 1; }
pass "Logs tab present"

echo ""
echo "  ✨ test-all OK — ${BASE}"
echo "     Sign in: admin / ${NODRA_ADMIN_TOKEN}"
echo "     Port:    ${PORT} (override with --port / NODRA_PORT)"
