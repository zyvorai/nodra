#!/usr/bin/env bash
# Copyright 2026 Zyvor AI Labs · https://zyvor.dev
# SPDX-License-Identifier: Apache-2.0
#
# smoke-relay-bridge.sh — Start a mock Relay Accept, run nodra-relay-bridge, POST a Nodra delivery.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

mkdir -p bin
CGO_ENABLED=0 go build -o bin/nodra-relay-bridge ./cmd/nodra-relay-bridge

MOCK_PORT="${NODRA_RELAY_SMOKE_MOCK_PORT:-18095}"
BRIDGE_PORT="${NODRA_RELAY_SMOKE_BRIDGE_PORT:-18096}"
TMP="$(mktemp -d)"
trap 'kill $(jobs -p) 2>/dev/null || true; rm -rf "$TMP"' EXIT

# Minimal mock Relay Accept (stdlib python)
python3 - <<PY &
from http.server import BaseHTTPRequestHandler, HTTPServer
import json

class H(BaseHTTPRequestHandler):
    def log_message(self, *a): pass
    def do_POST(self):
        n=int(self.headers.get("Content-Length","0"))
        body=self.rfile.read(n)
        data=json.loads(body.decode() or "{}")
        assert data.get("type"), data
        assert data.get("idempotency_key"), data
        assert data.get("source")=="nodra", data
        self.send_response(200)
        self.send_header("Content-Type","application/json")
        self.end_headers()
        self.wfile.write(json.dumps({"event":{"id":"evt_smoke"}}).encode())
    def do_GET(self):
        self.send_response(200); self.end_headers(); self.wfile.write(b"ok")

HTTPServer(("127.0.0.1", int("${MOCK_PORT}")), H).serve_forever()
PY
MOCK_PID=$!
sleep 0.4

RELAY_BASE_URL="http://127.0.0.1:${MOCK_PORT}" RELAY_AUTH_TOKEN=smoke \
  ./bin/nodra-relay-bridge --listen "127.0.0.1:${BRIDGE_PORT}" >/tmp/nodra-relay-bridge-smoke.log 2>&1 &
BRIDGE_PID=$!
for _ in $(seq 1 40); do
  curl -fsS "http://127.0.0.1:${BRIDGE_PORT}/healthz" >/dev/null && break
  sleep 0.1
done

curl -fsS -X POST "http://127.0.0.1:${BRIDGE_PORT}/v1/nodra/delivery" \
  -H 'Content-Type: application/json' \
  -d '{
    "event_id":"edge_smoke_1",
    "site_id":"site_smoke",
    "topic":"factory/line-1/alert",
    "payload":{"c":31.2,"severity":"high"},
    "headers":{"x-source":"smoke"},
    "delivery_id":"dlv_smoke_1",
    "event_time":"2026-09-06T12:00:00Z"
  }' | grep -Fq '"accepted":true'

echo "smoke-relay-bridge: PASS"
