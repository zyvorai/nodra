---
hero:
  eyebrow: TROUBLESHOOTING
  title: Troubleshooting
---

Real operational issues, with the documented fix — not a generic checklist.
If your symptom isn't here, check [`docs/OPERATIONS.md`](OPERATIONS.md) in
full, then [open an issue](https://github.com/zyvorai/nodra/issues).

## Disk usage keeps growing / queue backing up

Monitor `nodra_delivery_queue_bytes`, `nodra_pending_deliveries`, and
`nodra_dead_letters` (control plane), plus `queue_bytes`/`queue_depth`
(agent heartbeats) — this is expected during an extended WAN outage, since
store-and-forward is designed to hold data rather than drop it. If
telemetry must never be silently discarded even under pressure, set
`spool_policy=reject` explicitly rather than assuming the default behavior.
See [`docs/OPERATIONS.md`](OPERATIONS.md#disk-pressure).

## Restored from backup and the live Activity/Logs tail looks empty

Expected — the console's Activity/Logs tail is an in-memory ring (cap 2000)
and is **not** part of the durable backup set, so it starts empty after any
restart or restore. The durable audit trail is unaffected: every
actor-attributable action is written to `audit/` under the data directory
(or the `nodra_audit_log` Postgres table), **is** captured by
`backup-state.sh`, and survives a restore — query it with `GET /api/v1/audit`
or `nodractl audit list`/`audit export`. A restore brings back
`state.snapshot.json`, `state.wal`, `deliveries/`, `deadletters/`, `audit/`,
and `pki/` if present.

## After restoring a backup, demo/activity data looks stale

WAL replay reconstructs live state and pending queues correctly, but
`nodra-sim`'s continuous demo activity needs to be re-run (or you wait for
the continuous simulator) to generate fresh activity lines — this is
expected, not a sign the restore failed.

## Control plane won't start a second replica

File mode will not. The data directory is a single-writer WAL on a `ReadWriteOnce` volume, and Helm fails the render when `replicaCount` is above 1 in that mode. Do not point two processes at the same data directory.

Postgres mode may run more than one control-plane process against the same database: fleet reads come from SQL, updates check `revision`, and delivery workers claim concurrently. That is still not full HA. Multi-day soak, PITR, and the rest of the 1.0 gates are open. See the README maturity banner and `ROADMAP.md`.

## A device using QoS 2 or persistent MQTT sessions doesn't behave as expected

QoS 2 is implemented in both directions, and persistent sessions replay queued QoS 1 (with `DUP`) and QoS 2. Subscription lists are in-memory and do not survive a broker restart: after a restart the client must subscribe again before new messages are queued for it. QoS 1 publishes from the broker do not wait for `PUBACK`. See `internal/mqtt` and `ROADMAP.md`.

## J1939/CAN data isn't showing up in Nodra

Nodra's J1939 connector consumes Zyvor Device Agent's read-only CAN capture
SSE stream — it does not talk to CAN hardware directly. Confirm Device
Agent's `industrial.can_capture` is enabled and streaming first (see Device
Agent's own `docs/CAN_CAPTURE.md`), then check
[`docs/INDUSTRIAL_PROTOCOLS.md`](INDUSTRIAL_PROTOCOLS.md) (see the
"J1939 (Device Agent)" tab) for the exact `j1939-device-agent` configuration
this depends on.

## An OPC-UA/serial/NATS/Zenoh/Kafka connector isn't available

These are roadmap items, not shipped features — `pkg/connector` has
scaffolding for a registry, but only Modbus (TCP/RTU) and the J1939
CAN-capture bridge are actually implemented today. Check `ROADMAP.md`
before assuming a protocol is supported.

## Nothing here matches

Check [`docs/OPERATIONS.md`](OPERATIONS.md) and
[`docs/SECURITY-MODEL.md`](SECURITY-MODEL.md) for the full runbook and
trust model, then
[open an issue](https://github.com/zyvorai/nodra/issues) with relevant
metrics/logs (redact credentials).
