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
# Port resolution: --port → NODRA_PORT → .deploy-last PORT
#
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
# shellcheck source=lib/ports.sh
source "$ROOT/scripts/lib/ports.sh"

PORT_FROM_CLI=""
while [ $# -gt 0 ]; do
  case "$1" in
    --port) [ $# -ge 2 ] || { echo "--port requires a value" >&2; exit 2; }; PORT_FROM_CLI="$2"; shift 2 ;;
    --port=*) PORT_FROM_CLI="${1#*=}"; shift ;;
    --help|-h)
      sed -n '2,18p' "$0" | sed 's/^# \{0,1\}//'
      exit 0
      ;;
    *) echo "Unknown option: $1" >&2; exit 2 ;;
  esac
done

BASE="$(nodra_resolve_base_url "$ROOT" "$PORT_FROM_CLI" || true)"
[ -n "$BASE" ] || {
  echo "Set NODRA_URL=http://host:port, or --port / NODRA_PORT with host from .deploy-last" >&2
  exit 2
}
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
grep -Fq 'Sign in to Nodra' "${TMP}/nodra-dash.html" || fail "login chapter missing"
grep -Fq 'login-store-page' "${TMP}/nodra-dash.html" || fail "Kryton-style login store missing"
grep -Fq 'login-wordmark' "${TMP}/nodra-dash.html" || fail "login wordmark missing"
grep -qi 'zyvor' "${TMP}/nodra-dash.html" || fail "missing Zyvor brand"
grep -qi 'Apache-2.0' "${TMP}/nodra-dash.html" || fail "footer missing Apache-2.0"
grep -Fq 'data-tab="logs"' "${TMP}/nodra-dash.html" || fail "Logs tab missing"
pass "login chapter + Kryton store + Logs tab"

code="$(curl -sS -o "${TMP}/nodra-login.json" -w '%{http_code}' \
  -H 'Content-Type: application/json' \
  -d "{\"username\":\"admin\",\"password\":\"${TOKEN}\"}" \
  "${BASE}/api/v1/auth/login")"
[ "$code" = "200" ] || fail "login HTTP ${code}"
grep -q '"token"' "${TMP}/nodra-login.json" || fail "login missing token"
pass "api/v1/auth/login"

code="$(curl -sS -o "${TMP}/nodra-version.json" -w '%{http_code}' "${BASE}/api/v1/version")"
[ "$code" = "200" ] || fail "version HTTP ${code}"
pass "api/v1/version"

code="$(curl -sS -o "${TMP}/nodra-overview.json" -w '%{http_code}' \
  -H "Authorization: Bearer ${TOKEN}" "${BASE}/api/v1/overview")"
[ "$code" = "200" ] || fail "overview HTTP ${code} (after login token)"
pass "api/v1/overview (admin)"

code="$(curl -sS -o "${TMP}/nodra-activity.json" -w '%{http_code}' \
  -H "Authorization: Bearer ${TOKEN}" "${BASE}/api/v1/activity?limit=5")"
[ "$code" = "200" ] || fail "activity HTTP ${code}"
[[ "$(head -c1 "${TMP}/nodra-activity.json")" == "[" ]] || fail "activity not JSON array"
pass "api/v1/activity"

code="$(curl -sS -o "${TMP}/nodra-metrics.txt" -w '%{http_code}' "${BASE}/metrics")"
[ "$code" = "200" ] || fail "metrics HTTP ${code}"
grep -Fq 'nodra_events_total' "${TMP}/nodra-metrics.txt" || fail "metrics missing nodra_events_total"
pass "metrics"

echo "  ✨ smoke OK"
