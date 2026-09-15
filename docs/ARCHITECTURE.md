---
hero:
  eyebrow: ARCHITECTURE
  title: Architecture
---

## Components

`nodra-server` is the management/control plane. `nodrad` is the edge runtime. `nodractl` is the operator CLI. `nodra-sim` is the optional A–Z fleet simulator for demos.

```text
                     nodra-server
    fleet · routes · twins · apps · DLQ · activity · console
                         │
                     HTTPS/mTLS
                         │
        ┌────────────────┼────────────────┐
        │                │                │
      nodrad           nodrad           nodra-sim (demo)
        │                │                │
   MQTT / HTTP      MQTT / HTTP      enroll + heartbeat + events
        │
  local routes + local apps
        │
  durable edge WAL -> cloud when available
```

The embedded web console is served from the control plane binary (`web/`). Login is a chaptered gate; after auth the shell tabs map to management APIs plus the activity ring for **Logs**.

## Durability

### Edge spool

The v0.2 queue is a single append-only WAL per queue, not one file per message. Each mutation is appended and `fsync`ed before success. Startup replays the WAL into an in-memory index. Automatic compaction rewrites only live records after an operation threshold.

Queue limits are enforced before a put. `reject` is the default because silent data loss is unacceptable for industrial telemetry.

### Control plane state

Fleet metadata uses `state.wal` plus periodic `state.snapshot.json` by default (`NODRA_STORE=file`). Optional `NODRA_STORE=postgres` persists the same fleet entities in PostgreSQL via `store.Backend`.

Heartbeats therefore append small records instead of rewriting the complete state document.

Cloud delivery and DLQ use durable WAL queues local to the control-plane writer in file mode. With `NODRA_STORE=postgres`, delivery/DLQ instead use `internal/queue.PostgresQueue` — readable and shared by every replica pointed at the same database — and *every* replica actively processes deliveries concurrently: `internal/queue.PostgresQueue` implements the `queue.Claimer` capability (`TryClaim`/`ReleaseClaim`), and `processDeliveries` claims each item individually via an atomic conditional `UPDATE ... WHERE claimed_by IS NULL OR claimed_at < now() - lease RETURNING id` before processing it, releasing the claim implicitly when the item is deleted (delivered, or moved to DLQ) or rescheduled (`Put` always clears `claimed_by`/`claimed_at`). This is **true multi-writer HA**, not single-active-writer failover: there is no elected leader for delivery processing, so a replica's death or connection drop only strands its in-flight claims until `Config.DeliveryClaimLease` (default 90s) elapses, at which point any other replica's next tick reclaims them — no advisory-lock handover, no window where two replicas both believe they're sole leader. `internal/leader.PostgresLock` (`pg_try_advisory_lock`) is still opened in Postgres mode but is no longer consulted by delivery processing; it's retained in case a future singleton background task needs it. File mode is unaffected: it stays exactly as before, single-process by construction, no coordination needed (`leader.AlwaysLeader`).

### Activity log

Console activity is an in-memory ring (cap 2000). It is intentionally non-durable so demo/ops noise does not inflate the WAL. Sources include control-plane notes and `nodra-sim` chapter posts via `POST /api/v1/activity`.

## Event ACK invariant

For an incoming edge event:

1. authenticate the site;
2. match cloud routes;
3. persist each deterministic delivery record;
4. persist the event record;
5. return HTTP 202.

A queue failure returns 503/507. `nodrad` keeps its edge event and retries. Delivery IDs are deterministic from `(event_id, route_id)`, so retrying an interrupted request does not multiply queued work.

## Time model

`event_time` is when the event was accepted by the edge runtime. `ingested_at` is when the control plane committed it. Offline periods therefore do not destroy the original telemetry timeline.

## Local autonomy

Every accepted event can match local routes before/cloud-independent of synchronization. Local HTTP deliveries have their own WAL and backoff loop. MQTT subscribers receive local fan-out directly from the embedded broker.

## MQTT boundary

The embedded broker is an edge-ingress broker, not a full general-purpose MQTT platform. v0.2 supports MQTT 3.1.1 CONNECT, SUBSCRIBE, PUBLISH, PUBACK/PUBREC/PUBREL/PUBCOMP, PINGREQ and DISCONNECT. Persistent sessions (`CleanSession=0`) are now supported for QoS 0/1 subscribers via `Broker.EnableSessions`: a durable per-`ClientID` queue captures messages matched while no live connection holds the session, replayed (with `DUP` set) on reconnect, and `CleanSession=1` discards that state. One honesty gap: subscription lists are in-memory only and don't survive a broker/process restart — the durable message queue itself does (it's a WAL, replayed on next open), but a client must re-subscribe after a restart before new messages resume being captured for it.

QoS 2 is supported on the **inbound** (publisher → broker) side only: a publishing client gets the full exactly-once `PUBLISH`/`PUBREC`/`PUBREL`/`PUBCOMP` handshake, with delivery to the local `Handler`/subscribers deferred until `PUBREL` so a retransmitted `PUBLISH` (e.g. after a lost `PUBREC`) never double-delivers; a retransmitted `PUBREL` (after a lost `PUBCOMP`) is likewise idempotent. **Outbound** QoS 2 (broker → subscriber) remains unsupported — `SUBSCRIBE` still caps at QoS 1, since `Broker.Publish`'s fanout is fire-and-forget with no ack-tracking even for QoS 1 today; building outbound exactly-once delivery is a separate, materially larger piece of work than the inbound half.

## Device twins

Twins have independent `desired_version` and `reported_version`. The server owns desired state; the edge/device adapter reports observed state. `nodrad` periodically caches twin state to disk so local adapters can read it during WAN loss.

## App reconciliation

With `runner=docker`, each reconciliation loop checks actual container state against the desired deployment. `running` creates/restarts a missing container; `stopped` removes it. The default distribution never mounts the Docker socket automatically.

## Simulation boundary

`nodra-sim` exercises the public management and agent APIs only. It is not in the production data path. Demo Helm charts and Compose optionally run it as a sidecar/workload alongside the control plane.

## Connectors

`pkg/connector` provides a registry (`Register` / `New`). `nodrad` loads `connectors[]` from config and starts each factory. The Modbus TCP poller (`connectors/modbus`) publishes into the same ingest path as MQTT with `x-nodra-ingress: modbus`.

## Scale boundary

v0.2 supports many edge agents. In file mode (`NODRA_STORE=file`, the default) there is exactly one control-plane writer — no coordination, no failover; this is inherent to a local WAL, not a gap to close. With `NODRA_STORE=postgres`, delivery/DLQ state is externalized to Postgres and every replica pointed at the same database actively claims and processes deliveries concurrently — true multi-writer HA, not single-active-writer failover (see "Control plane state" above). This satisfies the v1.0 "HA control plane including delivery workers" criterion for Postgres-mode deployments. What Postgres mode does *not* yet give you: full per-org isolation (see "Multi-tenant orgs" below — v1 only org-filters the sites list and single-site revoke, every other endpoint stays fleet-wide regardless of caller) or HA for `internal/store`'s own connection handling beyond what `database/sql`'s pooling/retry already provides.

## Multi-tenant orgs

`model.Org` (`internal/store`'s `AddOrg`/`Orgs`/`Org`/`UpdateOrg`/`DeleteOrg`, backed by both the file WAL and Postgres store) is an additive, optional tenant boundary layered on top of the existing single flat global admin/viewer token model — nothing changes for a deployment that never calls `POST /api/v1/orgs`. An org has its own enrollment token and its own admin/viewer bearer tokens (only their SHA-256 hashes are persisted, via `internal/auth.Hash`/`EqualHash` — the same pattern as a site's agent token); `Server.roleForBearer` falls back to checking these hashes after the configured global tokens, so an org token resolves to the same `"admin"`/`"viewer"` role a global token would.

The isolation boundary itself is intentionally narrow in v1: `Server.orgForBearer` resolves which org (if any) a bearer token belongs to, and only two call sites use it — `sites()` (`GET /api/v1/sites` filters to the caller's own `org_id`) and `siteRevoke()` (rejects, as `404`, a target site outside the caller's org). Every other admin-gated handler — devices, twins, routes, deployments, alerts, policy-packs, audit, deliveries/DLQ — has no org filtering at all: an org's admin/viewer token can read and mutate the entire fleet's data through any of those endpoints, exactly like the global token. Org management (`POST/GET/DELETE /api/v1/orgs(/{id})`) is gated to the global admin token specifically (`requireGlobalAdmin`) — an org's own admin token cannot create, list, or delete orgs, including itself.

This is enough to demonstrate the mechanism and to keep one tenant's *sites* from seeing another's, but it is not full multi-tenant security isolation — anyone reaching for org tokens as a hard security boundary beyond site visibility needs to know the other entities aren't scoped yet.
