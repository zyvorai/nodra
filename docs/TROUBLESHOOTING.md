# Troubleshooting

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

## Restored from backup and activity/log history is missing

Expected — the Activity/Logs ring is explicitly in-memory only and is
**not** part of the durable backup set. A restore brings back
`state.snapshot.json`, `state.wal`, `deliveries/`, `deadletters/`, and
`pki/` if present; recent log lines are not preserved across a restore by
design.

## After restoring a backup, demo/activity data looks stale

WAL replay reconstructs live state and pending queues correctly, but
`nodra-sim`'s continuous demo activity needs to be re-run (or you wait for
the continuous simulator) to generate fresh activity lines — this is
expected, not a sign the restore failed.

## Control plane won't start a second replica

By design — v0.2's control plane intentionally runs as a single
writer/replica because its embedded WAL is single-writer (see the README's
maturity banner and `ROADMAP.md`'s v1.0 criteria). There is no supported HA
mode yet; running a second instance against the same data directory is not
a configuration you should attempt.

## A device using QoS 2 or persistent MQTT sessions doesn't behave as expected

Expected — v0.2 explicitly does not claim QoS 2 or persistent-session
support. Use QoS 0/1 and design your device logic accordingly, or check
`ROADMAP.md`/`CHANGELOG.md` for whether a later release has added it.

## J1939/CAN data isn't showing up in Nodra

Nodra's J1939 connector consumes Zyvor Device Agent's read-only CAN capture
SSE stream — it does not talk to CAN hardware directly. Confirm Device
Agent's `industrial.can_capture` is enabled and streaming first (see Device
Agent's own `docs/CAN_CAPTURE.md`), then check
[`docs/INDUSTRIAL_PROTOCOLS.md`](INDUSTRIAL_PROTOCOLS.md#j1939-from-device-agent)
for the exact `j1939-device-agent` configuration this depends on.

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
