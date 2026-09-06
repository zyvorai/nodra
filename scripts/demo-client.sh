#!/usr/bin/env bash
# Copyright 2026 Zyvor
# SPDX-License-Identifier: Apache-2.0
# ============================================================================
# demo-client.sh — Seed + verify a client-demo fleet against a live Nodra
# ============================================================================
# Walks the same chapters a sales engineer shows in Chrome:
#   login → enroll site → heartbeat → device → route → event → overview
#
# Usage:
#   NODRA_URL=http://212.8.248.187:20059 ./scripts/demo-client.sh
#   ./scripts/demo-client.sh --port 20059
#
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
PORT_FROM_CLI=""
while [ $# -gt 0 ]; do
  case "$1" in
    --port) PORT_FROM_CLI="$2"; shift 2 ;;
    --port=*) PORT_FROM_CLI="${1#*=}"; shift ;;
    --help|-h) sed -n '2,18p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "Unknown option: $1" >&2; exit 2 ;;
  esac
done

BASE="${NODRA_URL:-}"
if [ -z "$BASE" ] && [ -f "$ROOT/.deploy-last" ]; then
  # shellcheck disable=SC1091
  source "$ROOT/.deploy-last"
  [ -n "${HOST:-}" ] && [ -n "${PORT_FROM_CLI:-${PORT:-}}" ] && BASE="http://${HOST}:${PORT_FROM_CLI:-$PORT}"
fi
[ -n "$BASE" ] || { echo "Set NODRA_URL or run after deploy-remote.sh" >&2; exit 2; }
BASE="${BASE%/}"
USER_NAME="${NODRA_ADMIN_USER:-admin}"
PASS="${NODRA_ADMIN_PASSWORD:-${NODRA_ADMIN_TOKEN:-nodra-lab-admin}}"
ENROLL="${NODRA_ENROLLMENT_TOKEN:-nodra-lab-enroll}"

pass() { printf '  ✅ %s\n' "$*"; }
fail() { printf '  ❌ %s\n' "$*" >&2; exit 1; }

echo "Nodra client demo → ${BASE}"

html="$(curl -fsS "$BASE/")"
grep -Fq 'Sign in to Nodra' <<<"$html" || fail "login chapter missing"
pass "login chapter"

login="$(curl -fsS -X POST "$BASE/api/v1/auth/login" -H 'Content-Type: application/json' \
  -d "{\"username\":\"${USER_NAME}\",\"password\":\"${PASS}\"}")"
TOKEN="$(python3 -c "import json,sys;print(json.load(sys.stdin)['token'])" <<<"$login")"
[ -n "$TOKEN" ] || fail "login token empty"
pass "sign-in as ${USER_NAME}"

AUTH=(-H "Authorization: Bearer ${TOKEN}" -H 'Content-Type: application/json')

enroll="$(curl -fsS -X POST "$BASE/api/v1/enroll" -H 'Content-Type: application/json' \
  -d "{\"name\":\"Client Demo Site\",\"enrollment_token\":\"${ENROLL}\",\"metadata\":{\"demo\":\"true\"}}")"
SITE="$(python3 -c "import json,sys;print(json.load(sys.stdin)['site_id'])" <<<"$enroll")"
ATOK="$(python3 -c "import json,sys;print(json.load(sys.stdin)['agent_token'])" <<<"$enroll")"
pass "enrolled site ${SITE}"

curl -fsS -X POST "$BASE/api/v1/heartbeat" -H "Authorization: Bearer ${ATOK}" -H 'Content-Type: application/json' \
  -d "{\"site_id\":\"${SITE}\",\"version\":\"0.2.0\",\"metrics\":{\"queue_depth\":1,\"queue_bytes\":1024}}" >/dev/null
pass "heartbeat online"

dev="$(curl -fsS -X POST "$BASE/api/v1/devices/register" -H "Authorization: Bearer ${ATOK}" -H 'Content-Type: application/json' \
  -d "{\"site_id\":\"${SITE}\",\"name\":\"Demo PLC\",\"protocol\":\"mqtt\"}")"
pass "registered device $(python3 -c "import json,sys;print(json.load(sys.stdin).get('id',''))" <<<"$dev")"

curl -fsS -X POST "$BASE/api/v1/routes" "${AUTH[@]}" \
  -d '{"name":"Demo MES","topic":"demo/+/telemetry","target_url":"http://127.0.0.1:9/hook","method":"POST","enabled":true,"retry_max":2,"timeout_seconds":2}' >/dev/null
pass "created demo route"

curl -fsS -X POST "$BASE/api/v1/deployments" "${AUTH[@]}" \
  -d "{\"site_id\":\"${SITE}\",\"name\":\"demo-vision\",\"version\":\"1.0.0\",\"image\":\"example/vision:1.0\",\"desired_state\":\"running\"}" >/dev/null
pass "created edge app"

ov="$(curl -fsS -H "Authorization: Bearer ${TOKEN}" "$BASE/api/v1/overview")"
OV="$ov" python3 - <<'PY'
import json, os
o = json.loads(os.environ["OV"])
assert o.get("sites", 0) >= 1, o
assert o.get("online_sites", 0) >= 1, o
assert o.get("devices", 0) >= 1, o
assert o.get("routes", 0) >= 1, o
print("  ✅ overview strip ready for console demo")
PY

for path in sites devices twins routes deployments alerts deadletters; do
  body="$(curl -fsS -H "Authorization: Bearer ${TOKEN}" "$BASE/api/v1/${path}")"
  [[ "$body" == null ]] && fail "/api/v1/${path} returned null"
  [[ "$body" == \[* ]] || fail "/api/v1/${path} not a JSON array"
  pass "GET /api/v1/${path} is array"
done

echo "  ✨ client demo seed OK — open ${BASE}/ and sign in as ${USER_NAME}"
