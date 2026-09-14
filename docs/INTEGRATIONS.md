---
hero:
  eyebrow: INTEGRATIONS
  title: Suite boundaries — Nodra, Fleet, Device Agent, OTA
---

Nodra is the **edge data plane** (ingress, WAL, routes, twins, apps). Adjacent
Zyvor products own other planes. This page records what is real today versus
config/docs stubs.

## Device Agent → Nodra

| Path | Status |
|---|---|
| MQTT publish into `nodrad` (inventory/status/sensors/events) | **Real** — Device Agent `nodra` integration; lab hosts set `nodra_enabled` |
| J1939 via Device Agent SSE connector | **Real** — `connectors/j1939` |
| Shared industrial protocol decoding in Device Agent | **Out of scope** — Nodra owns Modbus/J1939 meaning |

Point Device Agent at the edge MQTT listener (`nodrad`), not the control-plane
HTTP port, unless you are using an HTTP ingest path intentionally.

## Fleet ↔ Nodra

| Path | Status |
|---|---|
| Fleet UI “integrations” card storing Nodra URL/API key | **Config stub only** — no outbound Fleet→Nodra caller in v0.3 |
| Site inventory merge from Device Agent | **Fleet agent path** — not via Nodra |
| Shared desired-state / rollout of `nodrad` itself | **Not implemented** — use Fleet runtime adapters or packaging |

Do not assume enabling the Fleet integration toggle wires telemetry. Treat it as
credential storage until a real adapter ships.

## OTA ↔ Nodra

| Path | Status |
|---|---|
| OTA health probe against loopback `nodrad` HTTP | **Common lab pattern** — OTA `checks` may target `http://127.0.0.1:9091/healthz` |
| Nodra assigning OS releases | **Out of scope** — Zyvor OTA + Fleet OTA contract |
| Nodra agent self-update campaigns | **Roadmap** — not in v0.2.x |

## Relay

| Path | Status |
|---|---|
| `nodra-relay-bridge` Accept mapping | **Real** — webhook → Relay `/v1/events` (direct or pubsub gateway) |
| Act → Verify loop | **Relay-side** — see [RELAY.md](RELAY.md) |

## Lab host note

A shared evaluation stack on `80.79.5.173` co-located Nodra CP (`:18447`),
`nodrad`, Device Agent, Fleet, and OTA simulator. Record topology in sibling
`docs/LAB.md` files; this does not qualify HA or multi-day soak.
