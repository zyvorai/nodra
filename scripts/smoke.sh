#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
TMP="$(mktemp -d)"
SERVER_PORT="${NODRA_SMOKE_SERVER_PORT:-18080}"
AGENT_PORT="${NODRA_SMOKE_AGENT_PORT:-19091}"
SERVER_PID=""; AGENT_PID=""
cleanup(){ [[ -n "$AGENT_PID" ]] && kill "$AGENT_PID" 2>/dev/null || true; [[ -n "$SERVER_PID" ]] && kill "$SERVER_PID" 2>/dev/null || true; rm -rf "$TMP"; }
trap cleanup EXIT
mkdir -p "$TMP/bin"
LDFLAGS="-X github.com/zyvorai/nodra/internal/version.Version=smoke"
CGO_ENABLED=0 go build -trimpath -ldflags "$LDFLAGS" -o "$TMP/bin/nodra-server" "$ROOT/cmd/nodra-server"
CGO_ENABLED=0 go build -trimpath -ldflags "$LDFLAGS" -o "$TMP/bin/nodrad" "$ROOT/cmd/nodrad"
CGO_ENABLED=0 go build -trimpath -ldflags "$LDFLAGS" -o "$TMP/bin/nodractl" "$ROOT/cmd/nodractl"
NODRA_ADMIN_TOKEN=admin-smoke NODRA_ENROLLMENT_TOKEN=enroll-smoke "$TMP/bin/nodra-server" --listen "127.0.0.1:$SERVER_PORT" --data "$TMP/control" >"$TMP/server.log" 2>&1 & SERVER_PID=$!
for _ in $(seq 1 50); do curl -fsS "http://127.0.0.1:$SERVER_PORT/readyz" >/dev/null && break; sleep .1; done
curl -fsS "http://127.0.0.1:$SERVER_PORT/readyz" >/dev/null
"$TMP/bin/nodrad" init --config "$TMP/nodrad.json" --server "http://127.0.0.1:$SERVER_PORT" --site smoke-edge --enrollment-token enroll-smoke --data "$TMP/agent-data" --listen "127.0.0.1:$AGENT_PORT" --mqtt-listen 127.0.0.1:18883 --local-token local-smoke >/dev/null
"$TMP/bin/nodrad" --config "$TMP/nodrad.json" >"$TMP/agent.log" 2>&1 & AGENT_PID=$!
for _ in $(seq 1 50); do curl -fsS "http://127.0.0.1:$AGENT_PORT/healthz" >/dev/null && break; sleep .1; done
"$TMP/bin/nodractl" publish --agent "http://127.0.0.1:$AGENT_PORT" --token local-smoke --topic smoke/site/telemetry --data '{"ok":true}' >/dev/null
for _ in $(seq 1 50); do OUT=$("$TMP/bin/nodractl" --server "http://127.0.0.1:$SERVER_PORT" --token admin-smoke events); echo "$OUT" | grep -q 'smoke/site/telemetry' && break; sleep .1; done
"$TMP/bin/nodractl" --server "http://127.0.0.1:$SERVER_PORT" --token admin-smoke events | grep -q 'smoke/site/telemetry'
HOME_HTML="$(curl -fsS "http://127.0.0.1:$SERVER_PORT/")"
grep -Fq 'Sign in to Nodra' <<<"$HOME_HTML"
grep -Fq 'login-store-page' <<<"$HOME_HTML"
grep -Fq 'Built by Zyvor' <<<"$HOME_HTML"
LOGIN="$(curl -fsS -X POST "http://127.0.0.1:$SERVER_PORT/api/v1/auth/login" -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"admin-smoke"}')"
echo "$LOGIN" | grep -Fq '"token":"admin-smoke"'
OVERVIEW="$(curl -fsS -H 'Authorization: Bearer admin-smoke' "http://127.0.0.1:$SERVER_PORT/api/v1/overview")"
echo "$OVERVIEW" | grep -Eq '"sites":[1-9]'
for path in sites devices routes deployments alerts deadletters; do
  body="$(curl -fsS -H 'Authorization: Bearer admin-smoke' "http://127.0.0.1:$SERVER_PORT/api/v1/${path}")"
  [[ "$body" == \[* ]] || { echo "smoke: /api/v1/${path} not array: $body" >&2; exit 1; }
done
METRICS="$(curl -fsS "http://127.0.0.1:$SERVER_PORT/metrics")"
grep -Fq 'nodra_events_total' <<<"$METRICS"
echo "smoke: PASS"
