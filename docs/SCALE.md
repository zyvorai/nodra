# Scale envelope

These are the limits the current build enforces, and the soak that checks them. They are not a measured events-per-second rating. A published throughput number waits on a soak that runs to completion with the integrity gate below.

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

## Local ingress observation

`scripts/bench-ingress.py` posts to a running edge agent's `/v1/publish` for a short window and prints `accepted_per_sec`. Record the host, duration, commit, and that JSON before treating the number as evidence. It is a local observation, not a product rating. Docker was required for the compose soak; when Docker is unavailable, run the bench against a local `nodrad` instead.

## How to run it

```bash
# PR-length
NODRA_SOAK_DURATION=10m ./scripts/ci/soak.sh /tmp/nodra-soak
python3 scripts/ci/soak-check.py /tmp/nodra-soak/summary.json

# Scheduled job (GitHub-hosted runners stop near 6h)
NODRA_SOAK_DURATION=4h NODRA_SOAK_WAN_CYCLE=15m NODRA_SOAK_WAN_DOWNTIME=20s \
  NODRA_SOAK_DISK_CYCLE=10m NODRA_SOAK_SAMPLE_INTERVAL=15s \
  ./scripts/ci/soak.sh evidence/qualification/ci/soak-long
```

`workflow_dispatch` on the Soak workflow accepts a duration. `24h`, `72h`, and `168h` (seven days) are the targets. A GitHub-hosted job cannot run them: the workflow timeout is 330 minutes. Those durations need a self-hosted runner and a timeout longer than the soak. They have not been executed in CI.
