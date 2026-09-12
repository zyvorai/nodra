---
hero:
  eyebrow: OPERATIONS
  title: Operations
---

## Backup

Stop the single control-plane writer or take a storage snapshot. Back up the Nodra data directory, including `state.snapshot.json`, `state.wal`, `deliveries/`, `deadletters/` and optional `pki/`.

Activity / Logs ring is **in-memory only** and is not part of the durable backup set.

## Recovery

Restore the complete data directory and start the same or newer compatible Nodra version. WAL replay reconstructs live state and pending queues. Re-run `nodra-sim` (or wait for continuous sim) if you need demo activity lines again.

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
| `NODRA_STORE` | `file` (default) or `postgres` |
| `NODRA_DATABASE_URL` | Postgres DSN when store is postgres |

Viewer tokens can list fleet data; mutating methods return 403. Rotate admin/enrollment tokens after exposure. Revoke lost sites from the control plane.

## Fleet store

Default: file WAL under the data directory. For Postgres:

```bash
export NODRA_STORE=postgres
export NODRA_DATABASE_URL='postgres://user:pass@host:5432/nodra?sslmode=require'
./bin/nodra-server --listen :8080 --data ./data
```

Delivery and DLQ queues remain local WAL on the control-plane pod (single writer). Postgres holds sites/devices/twins/routes/deployments/alerts/events.

## Upgrades

v0.2 is single-writer. Scale the control-plane Deployment to zero, update the image, then return to one replica, or use the Helm `Recreate` strategy already provided. Edge agents continue local operation and spool cloud-bound events during the interruption.

## Demo / simulation

See [DEMO.md](DEMO.md). Enable Helm `simulation.enabled=true` or run `nodra-sim` against a live control plane. Continuous simulation posts heartbeats and activity; `--once` seeds and exits.

## Failure domains

A slow webhook does not block unrelated routes because delivery is concurrent and bounded. Repeatedly failing deliveries enter the DLQ after their configured retry count.

## Verification

```bash
./scripts/smoke.sh
./scripts/smoke-remote.sh --port 20059
./scripts/test-all.sh --port 20059 --skip-deploy
```
