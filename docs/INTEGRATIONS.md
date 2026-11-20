---
hero:
  eyebrow: INTEGRATIONS
  title: Suite boundaries — Nodra, Fleet, Device Agent, OTA, Yard
---

Nodra is the **edge data plane** (ingress, WAL, routes, twins, apps, staged OTA
campaigns). Adjacent Zyvor products own other planes. For the full suite
diagram and CI proof, see
[edge-stack docs/HOW_THEY_FIT.md](https://github.com/zyvorai/edge-stack/blob/main/docs/HOW_THEY_FIT.md).

## Device Agent → Nodra

| Path | Status |
|---|---|
| MQTT publish into Nodra (inventory/status/sensors/events) | **Real** — Device Agent `nodra` integration; lab hosts set `nodra_enabled` |
| J1939 via Device Agent SSE connector | **Real** — `connectors/j1939` |
| Shared industrial protocol decoding in Device Agent | **Out of scope** — Nodra owns Modbus/J1939 meaning |

Point Device Agent at the edge MQTT listener, not the control-plane HTTP port,
unless you are using an HTTP ingest path intentionally.

## Fleet ↔ Nodra

| Path | Status |
|---|---|
| Fleet UI “integrations” card storing Nodra URL/API key | **Config stub only** — no outbound Fleet→Nodra caller in v0.3 |
| Site inventory merge from Device Agent | **Fleet agent path** — not via Nodra |
| Shared desired-state / rollout of Nodra itself | **Not implemented** — use Fleet runtime adapters or packaging |

Do not assume enabling the Fleet integration toggle wires telemetry.

## OTA ↔ Nodra

| Path | Status |
|---|---|
| OTA health probe against loopback Nodra HTTP | **Common lab pattern** |
| Nodra-side OTA contract (`pkg/ota`) + twin desired/reported | **Real** |
| Staged multi-site canary campaigns (`/api/v1/ota/campaigns`) | **Real** — create/list/get/start/promote/abort |
| Nodra assigning OS images on Minewing silicon | **Out of scope** — Zyvor OTA HIL (QEMU lab signed; Minewing unsigned) |

## Yard ↔ Nodra

| Path | Status |
|---|---|
| Yard connector `telemetry.receive` / `sync` | **Real** — pulls `/api/v1/devices` + `/api/v1/twins` into Yard ingest |
| Yard OTA connector `campaign.list` with `config.source=nodra` | **Real** |

## Relay

| Path | Status |
|---|---|
| `nodra-relay-bridge` Accept mapping | **Real** — webhook → Relay `/v1/events` (direct or pubsub gateway) |
| Act → Verify loop | **Relay-side** |

## Suite CI

Cross-product contract smoke (stubs + docs) lives in
[zyvorai/edge-stack](https://github.com/zyvorai/edge-stack) workflow **suite-ci**.
Product qualify matrices remain in each repo.
