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

Postgres mode may run more than one control-plane process against the same database: fleet reads come from SQL, updates check `revision`, and delivery workers claim concurrently. That is still not full HA. A four-hour lab soak passed 2026-09-21; a 24h soak is in progress (not signed until judged). 72h/7d, PITR, and full HA remain open. Configured limits and lab ingress observations are in [`docs/SCALE.md`](SCALE.md). Upgrade/rollback: [`docs/UPGRADE.md`](UPGRADE.md). See the README maturity banner and `ROADMAP.md`.

## A device using QoS 2 or persistent MQTT sessions doesn't behave as expected

QoS 2 is implemented in both directions, and persistent sessions replay queued QoS 1 (with `DUP`) and QoS 2. Subscription lists are in-memory and do not survive a broker restart: after a restart the client must subscribe again before new messages are queued for it. QoS 1 publishes from the broker do not wait for `PUBACK`. See `internal/mqtt` and `ROADMAP.md`.

## Cleartext MQTT CONNECT fails after enabling TLS

When `mqtt_cert_file` and `mqtt_key_file` are set, the broker listens with TLS only. Point the client at the same host and port with TLS (for example `mosquitto_pub --cafile …`), or remove those fields to keep cleartext. A missing client certificate fails when `mqtt_require_client_cert` is set.

## Login or enrollment returns 429

Five failed console logins or five failed enrollment tokens from the same address within five minutes return 429. Wait for the window to expire, or use a different source address. The buckets are separate (`login` vs `enroll`).

## Local HTTP publish returns 401 without a token

`local_token` is required unless `allow_unauthenticated_local` is set. Send `Authorization: Bearer …` on `/v1/publish`.

## J1939/CAN data isn't showing up in Nodra

Nodra's J1939 connector consumes Zyvor Device Agent's read-only CAN capture
SSE stream — it does not talk to CAN hardware directly. Confirm Device
Agent's `industrial.can_capture` is enabled and streaming first (see Device
Agent's own `docs/CAN_CAPTURE.md`), then check
[`docs/INDUSTRIAL_PROTOCOLS.md`](INDUSTRIAL_PROTOCOLS.md) (see the
"J1939 (Device Agent)" tab) for the exact `j1939-device-agent` configuration
this depends on.

## An OPC-UA, serial, or NATS connector isn't publishing

OPC-UA, the Linux serial connector, and the NATS subscribe bridge are in this
tree. OPC-UA runs SecurityPolicy None or Basic256Sha256 (Sign or
SignAndEncrypt); the user identity token is always anonymous, so a server
that demands username/password or an X.509 user token will refuse the
session no matter how the channel is configured. Serial framing
runs on Linux; other platforms get the build-tag stub. Confirm the connector
block in the agent config against
[`docs/INDUSTRIAL_PROTOCOLS.md`](INDUSTRIAL_PROTOCOLS.md) and
[`docs/NATS_BRIDGE.md`](NATS_BRIDGE.md). Zenoh and Kafka are not implemented.

## Nothing here matches

Check [`docs/OPERATIONS.md`](OPERATIONS.md) and
[`docs/SECURITY-MODEL.md`](SECURITY-MODEL.md) for the full runbook and
trust model, then
[open an issue](https://github.com/zyvorai/nodra/issues) with relevant
metrics/logs (redact credentials).
