---
hero:
  eyebrow: OPERATIONS
  title: Operations
---

## Backup

Stop the single control-plane writer or take a storage snapshot. Back up the Nodra data directory, including `state.snapshot.json`, `state.wal`, `deliveries/`, `deadletters/`, `audit/` and optional `pki/`.

```bash
./scripts/backup-state.sh /var/lib/nodra /var/backups/nodra-$(date -u +%Y%m%d).tar.gz
```

For PostgreSQL, use the logical dump scripts instead of the file tarball:

```bash
./scripts/backup-postgres.sh /var/backups/nodra-$(date -u +%Y%m%d).dump
# writer stopped
NODRA_RESTORE_FORCE=1 ./scripts/restore-postgres.sh /var/backups/nodra-….dump
```

Neither path is point-in-time recovery. See [RECOVERY.md](RECOVERY.md).

The live console's Activity/Logs tail is an **in-memory ring** (cap 2000) and
is not part of the durable backup set — but every action it shows that has
an identifiable actor is also durably written to `audit/` (daily-rotated
NDJSON, or the `nodra_audit_log` Postgres table with `NODRA_STORE=postgres`)
and **is** captured by `backup-state.sh` since it lives under the data
directory. Query it live via `GET /api/v1/audit` or export it with
`nodractl audit export --out FILE`.

## Recovery

Restore the complete data directory and start the same or newer compatible Nodra version. WAL replay reconstructs live state and pending queues. Re-run `nodra-sim` (or wait for continuous sim) if you need demo activity lines again.

```bash
# writer stopped; empty target or NODRA_RESTORE_FORCE=1
./scripts/restore-state.sh /var/backups/nodra-….tar.gz /var/lib/nodra
curl -fsS http://127.0.0.1:PORT/readyz   # must report store ready
```

Never dual-mount the same WAL on two live control-plane processes. See [PRODUCTION.md](PRODUCTION.md).

## Readiness

`GET /readyz` pings the fleet store (file WAL open / Postgres `Ping`) and confirms delivery + DLQ queues are available. Prefer it over `/healthz` for load balancer gates.

## Disk pressure

Monitor `nodra_delivery_queue_bytes`, `nodra_pending_deliveries` and `nodra_dead_letters`. On agents, heartbeat metrics expose `queue_bytes`, `queue_depth` and configured queue maximum.

Use `spool_policy=reject` for telemetry that must never be silently discarded.

## Ports

Shared resolution: CLI `--port` → `NODRA_PORT` → `.deploy-last` → random high port for remote deploy.

| Surface | Variables / flags |
|---|---|
| Control plane listen | `nodra-server --listen` |
| Remote systemd | `./scripts/deploy-remote.sh HOST USER --port N` |
| Compose | `NODRA_PORT`, `NODRA_AGENT_PORT`, `NODRA_MQTT_PORT` |
| Helm | `service.port`; optional `service.type=NodePort` + `service.nodePort` |
| kind demo PF | `./scripts/demo-k8s.sh --port N` |
| Local smoke | `NODRA_SMOKE_SERVER_PORT`, `NODRA_SMOKE_AGENT_PORT` |

## Credentials

| Env | Role |
|---|---|
| `NODRA_ADMIN_TOKEN` | Management API bearer (also default console password) |
| `NODRA_ADMIN_USER` | Console username (default `admin`) |
| `NODRA_ADMIN_PASSWORD` | Console password (default = admin token) |
| `NODRA_VIEWER_TOKEN` | Optional read-only bearer |
| `NODRA_VIEWER_USER` / `NODRA_VIEWER_PASSWORD` | Optional viewer console login (password defaults to viewer token) |
| `NODRA_ENROLLMENT_TOKEN` | Site bootstrap |
| `NODRA_TLS_CERT` / `NODRA_TLS_KEY` | Control-plane HTTPS |
| `NODRA_CLIENT_CA` | Optional client CA for mTLS |
| `NODRA_REQUIRE_CLIENT_CERT` | Refuse clients without a certificate when a client CA is set |
| `NODRA_STORE` | `file` (default) or `postgres` |
| `NODRA_DATABASE_URL` | Postgres DSN when store is postgres |

Viewer tokens can list fleet data; mutating methods return 403. Rotate admin/enrollment tokens after exposure. Revoke lost sites from the control plane.

## Edge MQTT TLS

Agent config fields `mqtt_cert_file` and `mqtt_key_file` wrap the MQTT listener in TLS 1.2 or newer. `mqtt_client_ca_file` verifies a presented client certificate; `mqtt_require_client_cert` refuses a client that presents none. Empty certificate fields leave cleartext MQTT. Helm mounts those files from `agent.mqtt.tls.existingSecret` when set.

## Operator checks

```bash
nodractl --url https://… --insecure preflight
nodractl --url https://… --token "$NODRA_ADMIN_TOKEN" --insecure doctor
nodractl --url https://… --insecure support-bundle --out /tmp/nodra-bundle
```

`preflight` checks health, readiness, and version. `doctor` also tries overview when a token is set. The bundle does not write tokens.

## Fleet store

Default: file WAL under the data directory. For Postgres:

```bash
export NODRA_STORE=postgres
export NODRA_DATABASE_URL='postgres://user:pass@host:5432/nodra?sslmode=require'
./bin/nodra-server --listen :8080 --data ./data
```

Delivery and DLQ queues stay on the local WAL when `NODRA_STORE=file`. With `NODRA_STORE=postgres` they live in Postgres and every replica claims work concurrently. Fleet documents (sites, devices, twins, routes, deployments, alerts, events) are read from Postgres on every call and updated only when `revision` matches. Numbered migrations run at startup and refuse a database newer than the binary. Caps, spool policies, and the soak that checks them are in [SCALE.md](SCALE.md). The soak does not start `nodra-sim`.

CI covers the Postgres path with `go test ./internal/store/ -run Postgres` against a
Postgres 16 service (`NODRA_DATABASE_URL`). Locally the same test skips unless the
DSN is set.

## Upgrades

File mode and the default Helm install stay at one replica and use `Recreate`, because the data volume is `ReadWriteOnce`. Postgres mode may set `replicaCount` above 1, which selects `RollingUpdate`. Edge agents continue local operation and spool cloud-bound events during a control-plane interruption. Schema changes apply as numbered migrations at startup; a database newer than the binary refuses to start.

## Demo / simulation

See [DEMO.md](DEMO.md). Enable Helm `simulation.enabled=true` or run `nodra-sim` against a live control plane. Continuous simulation posts heartbeats and activity; `--once` seeds and exits.

## Failure domains

A slow webhook does not block unrelated routes because delivery is concurrent and bounded. Repeatedly failing deliveries enter the DLQ after their configured retry count.

## Verification

```bash
./scripts/smoke.sh
./scripts/smoke-remote.sh --port 20059
./scripts/test-all.sh --port 20059 --skip-deploy
make qualify   # software matrix → evidence/qualification/software-matrix.json
```

See [QUALIFICATION.md](QUALIFICATION.md) and [INTEGRATIONS.md](INTEGRATIONS.md).
