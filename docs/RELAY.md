---
hero:
  eyebrow: RELAY
  title: Nodra ↔ Zyvor Relay
---

Nodra keeps the edge online (MQTT/HTTP, offline WAL, twins). [Zyvor Relay](https://github.com/zyvorai/relay) owns the durable ops loop: **Accept → Notify → Ack → Act → Verify**.

They join over HTTP: Nodra cloud routes POST webhooks to **`nodra-relay-bridge`**, which maps deliveries into Relay `POST /v1/events` (or optional relay-pubsub `:publish`).

```text
nodrad ──► nodra-server ── route target_url ──► nodra-relay-bridge
                                                      │
                                      RELAY_BASE_URL  │  or GATEWAY_BASE_URL
                                                      ▼
                                               Zyvor Relay Accept
```

## Quick start

```bash
make build
export RELAY_BASE_URL=https://relay.example:8443
export RELAY_AUTH_TOKEN=<jwt-from-relay-login>
# optional lab self-signed:
# export RELAY_TLS_INSECURE=1

./bin/nodra-relay-bridge --listen :8095
```

Create a Nodra cloud route (Streams console or CLI):

```bash
nodractl --server http://127.0.0.1:8080 --token "$NODRA_ADMIN_TOKEN" \
  routes create \
  --name relay-alerts \
  --topic 'factory/+/alert' \
  --target http://127.0.0.1:8095/v1/nodra/delivery
```

Edge publishes on `factory/line-1/alert` → Nodra matches the route → bridge Accepts into Relay as type `factory.line-1.alert`.

## Environment

| Env | Purpose |
|---|---|
| `NODRA_RELAY_BRIDGE_LISTEN` | Bridge listen (default `:8095`) |
| `RELAY_BASE_URL` | Direct Relay base (required if no gateway) |
| `RELAY_AUTH_TOKEN` | Bearer JWT for Accept |
| `GATEWAY_BASE_URL` | Optional relay-pubsub base (preferred when set) |
| `GATEWAY_AUTH_TOKEN` | Optional gateway bearer |
| `FASAL_GCP_PROJECT` | Gateway project id (default `fasal-onprem`) |
| `RELAY_TLS_INSECURE` | Skip TLS verify for lab certs |

## Field mapping

| Nodra delivery | Relay Accept |
|---|---|
| `delivery_id` (else `event_id`) | `idempotency_key` |
| topic with `/` → `.`, or header `x-nodra-relay-type` | `type` |
| `"nodra"` | `source` |
| payload/header severity (default `medium`) | `severity` |
| `site_id`, `topic`, `payload`, `event_time`, headers (+ payload keys) | `data` |

Override type/severity from the edge with headers on the event:

- `x-nodra-relay-type: irrigation.required`
- `x-nodra-relay-severity: critical`

Bridge returns **2xx only when Relay Accept succeeds**, so Nodra retries and DLQ behave correctly.

## Compose sketch

See [`examples/relay/`](https://github.com/zyvorai/nodra/tree/main/examples/relay) for a bridge + mock Accept compose file.

```bash
./scripts/smoke-relay-bridge.sh
# or: make smoke-relay-bridge
```

## Act / verify (later)

Point Relay `RELAY_ACTION_TARGETS` and stamped `data.verification_probe.url` at Nodra local HTTP handlers or twin endpoints when you need Act→edge and Verify. This bridge only covers **Accept ingest**.

## Related

- Relay architecture: https://github.com/zyvorai/relay/blob/main/docs/ARCHITECTURE.md  
- Relay Nodra note: https://github.com/zyvorai/relay/blob/main/docs/NODRA.md  
- relay-edge publish paths: similar dual direct/gateway client  
