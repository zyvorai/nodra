#!/usr/bin/env bash
# Copyright 2026 Zyvor
# SPDX-License-Identifier: Apache-2.0
# ============================================================================
# smoke-remote.sh — Verify a running Nodra control plane
# ============================================================================
# Usage:
#   NODRA_URL=http://212.8.248.187:18447 ./scripts/smoke-remote.sh
#   ./scripts/smoke-remote.sh --port 18447
#   ./scripts/smoke-remote.sh   # uses HOST:PORT from .deploy-last
#
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
PORT_FROM_CLI=""
while [ $# -gt 0 ]; do
  case "$1" in
    --port) [ $# -ge 2 ] || { echo "--port requires a value" >&2; exit 2; }; PORT_FROM_CLI="$2"; shift 2 ;;
    --port=*) PORT_FROM_CLI="${1#*=}"; shift ;;
    --help|-h)
      sed -n '2,16p' "$0" | sed 's/^# \{0,1\}//'
      exit 0
      ;;
    *) echo "Unknown option: $1" >&2; exit 2 ;;
  esac
done

BASE="${NODRA_URL:-}"
HOST_FROM_LAST=""
PORT_FROM_LAST=""
if [ -f "$ROOT/.deploy-last" ]; then
  # shellcheck disable=SC1091
  source "$ROOT/.deploy-last"
  HOST_FROM_LAST="${HOST:-}"
  PORT_FROM_LAST="${PORT:-}"
fi

if [ -z "$BASE" ]; then
  PORT_RESOLVED="${PORT_FROM_CLI:-${NODRA_PORT:-$PORT_FROM_LAST}}"
  HOST_RESOLVED="${NODRA_HOST:-$HOST_FROM_LAST}"
  if [ -n "$HOST_RESOLVED" ] && [ -n "$PORT_RESOLVED" ]; then
    BASE="http://${HOST_RESOLVED}:${PORT_RESOLVED}"
  fi
fi
[ -n "$BASE" ] || {
  echo "Set NODRA_URL=http://host:port, or --port / NODRA_PORT with host from .deploy-last" >&2
  exit 2
}
BASE="${BASE%/}"
TMP="${TMPDIR:-/tmp}"
TOKEN="${NODRA_ADMIN_TOKEN:-nodra-lab-admin}"

pass() { printf '  ✅ %s\n' "$*"; }
fail() { printf '  ❌ %s\n' "$*" >&2; exit 1; }

echo "Nodra smoke → ${BASE}"

code="$(curl -sS -o "${TMP}/nodra-health.json" -w '%{http_code}' "${BASE}/healthz")"
[ "$code" = "200" ] || fail "healthz HTTP ${code}"
grep -q '"status":"ok"\|"status": "ok"' "${TMP}/nodra-health.json" || fail "healthz body unexpected"
pass "healthz"

code="$(curl -sS -o "${TMP}/nodra-ready.json" -w '%{http_code}' "${BASE}/readyz")"
[ "$code" = "200" ] || fail "readyz HTTP ${code}"
grep -qi 'ready' "${TMP}/nodra-ready.json" || fail "readyz body unexpected"
pass "readyz"

code="$(curl -sS -o "${TMP}/nodra-dash.html" -w '%{http_code}' "${BASE}/")"
[ "$code" = "200" ] || fail "dashboard HTTP ${code}"
grep -Fq 'Cloud optional.' "${TMP}/nodra-dash.html" || fail "dashboard missing hero copy"
grep -q 'brand-zyvor' "${TMP}/nodra-dash.html" || fail "missing Zyvor brand mark"
grep -qi 'Built by Zyvor' "${TMP}/nodra-dash.html" || fail "footer missing Built by Zyvor"
pass "dashboard + Zyvor brand"

code="$(curl -sS -o "${TMP}/nodra-version.json" -w '%{http_code}' "${BASE}/api/v1/version")"
[ "$code" = "200" ] || fail "version HTTP ${code}"
pass "api/v1/version"

code="$(curl -sS -o "${TMP}/nodra-overview.json" -w '%{http_code}' \
  -H "Authorization: Bearer ${TOKEN}" "${BASE}/api/v1/overview")"
[ "$code" = "200" ] || fail "overview HTTP ${code} (token=${TOKEN})"
pass "api/v1/overview (admin)"

code="$(curl -sS -o "${TMP}/nodra-metrics.txt" -w '%{http_code}' "${BASE}/metrics")"
[ "$code" = "200" ] || fail "metrics HTTP ${code}"
grep -Fq 'nodra_events_total' "${TMP}/nodra-metrics.txt" || fail "metrics missing nodra_events_total"
pass "metrics"

echo "  ✨ smoke OK"
