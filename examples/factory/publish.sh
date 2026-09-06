#!/usr/bin/env sh
set -eu
AGENT="${1:-http://127.0.0.1:9091}"
TOKEN="${2:-local-secret}"
curl -fsS -X POST "$AGENT/v1/publish" \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"topic":"factory/press-07/telemetry","payload":{"temperature":72.4,"vibration":0.17,"rpm":1180}}'
echo
