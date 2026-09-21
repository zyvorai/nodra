# Scale envelope

These are the limits the current build enforces, and the soak that checks them.
A lab ingress observation is recorded below; it is a measured local accept rate
on one host, not a certified product rating across topologies.

## Configured limits

| Limit | Value | Where |
|---|---|---|
| File-mode control plane | 1 replica | Helm rejects `replicaCount` above 1 when `server.store` is `file` |
| Delivery queue | 250000 items, `reject` when full | `server.Config.DeliveryMaxItems` |
| Edge spool default | 1000000 events or 2 GiB, policy `reject` | `nodrad --max-spool-events`, `--max-spool-bytes`, `--spool-policy` |
| Spool policies | `reject`, `drop-oldest`, `drop-newest` | `internal/queue` file WAL. Postgres delivery queues implement `reject` only |
| In-memory fleet events (file store) | last 2000 | `internal/store` event trim |
| Seen event ids (file store) | last 20000 | `internal/store` |
| Console activity ring | 2000 | `internal/server/activity.go` |
| Postgres event/alert reads | last 2000 rows | SQL `LIMIT` on `Alerts` and `EventsSince` |

`reject` leaves the queue unchanged and returns an error. `drop-oldest` deletes the oldest item until the new one fits. `drop-newest` refuses the new item and keeps what is already stored. Those three behaviors are covered by `TestQueueRejectQuota`, `TestQueueDropOldest`, and `TestQueueDropNewest`.

## What the long soak measures

`scripts/ci/soak.sh` starts the control plane, the edge agent, and an HTTP sink. It does not start `nodra-sim`. The simulator posts to routes aimed at `127.0.0.1:9`, and those failed deliveries stay in the in-memory delivery index until the 250000 cap. That backlog is what took a four-hour run from about 32 MB to about 107 MB. The soak now publishes `soak/ingest` through the agent over HTTP and MQTT, including while the control plane is stopped, and the control plane delivers that topic to the sink.

Control-plane counters live in process memory, so a WAN restart sets them back to zero. The soak samples them and adds the increase across each restart. `scripts/ci/soak-check.py` fails `no_data_loss` when nothing was accepted, or when accepted HTTP and MQTT publishes are not accounted for as persisted events, duplicate replays, or messages still in the edge spool.

Heap profiles are written to the evidence directory as `heap-start.pprof`, `heap-mid.pprof`, and `heap-end.pprof` from `NODRA_PPROF` (loopback port 6060, soak overlay only).

`NODRA_SOAK_LAB=1` drives the same WAN/disk/ingest loops against a systemd
`nodra-server` / `nodrad` install (HTTPS control plane supported).

## Published ingress observation

`scripts/bench-ingress.py` posts to a running edge agent's `/v1/publish` for a short window and prints `accepted_per_sec`. Record the host, duration, commit, workers, and that JSON before treating the number as evidence. It is a local observation, not a certified multi-site rating.

| Host | Commit | Seconds | Workers | Accepted | Failed | accepted/sec | Notes |
|---|---|---|---|---|---|---|---|
| `80.79.5.173` (lab nodrad loopback) | `5bda337` | 30.3 | 1 | 59 | 0 | 1.95 | Sequential HTTP; edge spool already ~54k deep from a prior HTTP→HTTPS CP misconfig |
| `80.79.5.173` (lab nodrad loopback) | `f7c49d4` | 30.0 | 1 | 187 | 0 | 6.23 | CP running; spool ~38k deep draining batch flush |
| `80.79.5.173` (lab nodrad loopback) | `f7c49d4` | 30.1 | 1 | 112 | 0 | 3.72 | CP stopped; local spool only |
| `80.79.5.173` (lab nodrad loopback) | `f7c49d4` | 31.1 | 8 | 215 | 0 | 6.91 | CP stopped; 8 concurrent HTTP clients |

**Envelope used in docs:** on this lab host at `f7c49d4`, loopback HTTP ingest accepted about **3.7–6.9 events/s** depending on concurrency and whether the control plane was flushing a deep spool. Do not extrapolate to WAN or multi-site topologies.

Agents flush cloud spool via `POST /api/v1/events/batch` (up to 100 events). Older control planes without that route still get one-at-a-time flush.

## How to run it

```bash
# PR-length
NODRA_SOAK_DURATION=10m ./scripts/ci/soak.sh /tmp/nodra-soak
python3 scripts/ci/soak-check.py /tmp/nodra-soak/summary.json

# Lab bare-metal (HTTPS CP)
NODRA_SOAK_LAB=1 NODRA_SOAK_DURATION=4h \
  NODRA_SOAK_CP_BASE=https://127.0.0.1:18447 \
  NODRA_SOAK_AGENT_BASE=http://127.0.0.1:9091 \
  NODRA_ADMIN_TOKEN=… NODRA_LOCAL_TOKEN=… \
  ./scripts/ci/soak.sh /tmp/nodra-soak-lab

# Scheduled job (GitHub-hosted runners stop near 6h)
NODRA_SOAK_DURATION=4h NODRA_SOAK_WAN_CYCLE=15m NODRA_SOAK_WAN_DOWNTIME=20s \
  NODRA_SOAK_DISK_CYCLE=10m NODRA_SOAK_SAMPLE_INTERVAL=15s \
  ./scripts/ci/soak.sh evidence/qualification/ci/soak-long
```

`workflow_dispatch` on the Soak workflow accepts a duration. `24h`, `72h`, and `168h` (seven days) are the targets. A GitHub-hosted job cannot run them: the workflow timeout is 330 minutes. Those durations need a self-hosted runner and a timeout longer than the soak.
