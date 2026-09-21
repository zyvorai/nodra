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

Fleet metadata uses `state.wal` plus periodic `state.snapshot.json` by default (`NODRA_STORE=file`). Optional `NODRA_STORE=postgres` persists the same fleet entities in PostgreSQL via `store.Backend`. Postgres mode does not keep a process-local fleet cache: every read queries SQL, and updates use optimistic concurrency (`UPDATE ... WHERE revision = ?`) with a short retry. Numbered migrations live in `internal/store/migrations`; a database newer than the binary refuses to start.

In file mode, heartbeats append small records instead of rewriting the complete state document.

Cloud delivery and DLQ use durable WAL queues local to the control-plane writer in file mode. With `NODRA_STORE=postgres`, delivery/DLQ instead use `internal/queue.PostgresQueue` — readable and shared by every replica pointed at the same database — and *every* replica actively processes deliveries concurrently: `internal/queue.PostgresQueue` implements the `queue.Claimer` capability (`TryClaim`/`ReleaseClaim`), and `processDeliveries` claims each item individually via an atomic conditional `UPDATE ... WHERE claimed_by IS NULL OR claimed_at < now() - lease RETURNING id` before processing it, releasing the claim implicitly when the item is deleted (delivered, or moved to DLQ) or rescheduled (`Put` always clears `claimed_by`/`claimed_at`). This is concurrent delivery claiming, not a full HA control plane: fleet documents use the revision checks above, and multi-day soak, PITR, and the remaining v1.0 gates are still open. There is no elected leader for delivery processing, so a replica's death or connection drop only strands its in-flight claims until `Config.DeliveryClaimLease` (default 90s) elapses, at which point any other replica's next tick reclaims them — no advisory-lock handover. `internal/leader.PostgresLock` (`pg_try_advisory_lock`) is still opened in Postgres mode but is no longer consulted by delivery processing; it's retained in case a future singleton background task needs it. File mode is unaffected: it stays exactly as before, single-process by construction, no coordination needed (`leader.AlwaysLeader`).

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

The embedded broker is an edge-ingress broker, not a full general-purpose MQTT platform. v0.2 supports MQTT 3.1.1 CONNECT, SUBSCRIBE, PUBLISH, PUBACK/PUBREC/PUBREL/PUBCOMP, PINGREQ and DISCONNECT. Persistent sessions (`CleanSession=0`) are supported for QoS 0/1/2 subscribers via `Broker.EnableSessions`: a durable per-`ClientID` queue captures messages matched while no live connection holds the session. QoS 1 is replayed with `DUP` set; QoS 2 is replayed through `PUBLISH`/`PUBREC`/`PUBREL`/`PUBCOMP`. `CleanSession=1` discards that state. Subscription lists are in-memory only and do not survive a broker restart — the durable message queue itself does (it's a WAL, replayed on next open), but a client must re-subscribe after a restart before new messages resume being captured for it.

QoS 2 is supported in **both** directions. **Inbound** (publisher → broker): a publishing client gets the full exactly-once `PUBLISH`/`PUBREC`/`PUBREL`/`PUBCOMP` handshake, with delivery to the local `Handler`/subscribers deferred until `PUBREL` so a retransmitted `PUBLISH` (e.g. after a lost `PUBREC`) never double-delivers; a retransmitted `PUBREL` (after a lost `PUBCOMP`) is likewise idempotent. **Outbound** (broker → subscriber): `SUBSCRIBE` may request QoS 2; `Broker.Publish` and persistent-session replay send `PUBLISH` QoS 2 with a broker-allocated packet id, track in-flight state, and complete `PUBREC` → `PUBREL` → `PUBCOMP` (including `PUBREL` retransmit if the subscriber's `PUBREC` is replayed). QoS 1 outbound remains fire-and-forget (no `PUBACK` wait).

## Device twins

Twins have independent `desired_version` and `reported_version`. The server owns desired state; the edge/device adapter reports observed state. `nodrad` periodically caches twin state to disk so local adapters can read it during WAN loss.

## App reconciliation

With `runner=docker`, each reconciliation loop checks actual container state against the desired deployment. `running` creates/restarts a missing container; `stopped` removes it. The default distribution never mounts the Docker socket automatically.

## Simulation boundary

`nodra-sim` exercises the public management and agent APIs only. It is not in the production data path. Demo Helm charts and Compose optionally run it as a sidecar/workload alongside the control plane.

## Connectors

`pkg/connector` provides a registry (`Register` / `New`). `nodrad` loads `connectors[]` from config and starts each factory. The Modbus TCP poller (`connectors/modbus`) publishes into the same ingest path as MQTT with `x-nodra-ingress: modbus`.

## Scale boundary

v0.2 supports many edge agents. In file mode (`NODRA_STORE=file`, the default) there is exactly one control-plane process — no coordination, no failover; Helm refuses `replicaCount` above 1 because the data volume is `ReadWriteOnce`. With `NODRA_STORE=postgres`, fleet state is read from PostgreSQL on every call and updated with a `revision` check, and every replica pointed at the same database claims and processes deliveries concurrently (see "Control plane state" above). A cross-replica test covers fleet consistency. That does **not** satisfy the v1.0 HA criterion: multi-day soak, PITR, and a published scale envelope are still open. Helm defaults to one replica.

## Multi-tenant orgs

`model.Org` (`internal/store`'s `AddOrg`/`Orgs`/`Org`/`UpdateOrg`/`DeleteOrg`, backed by both the file WAL and Postgres store) is an additive, optional tenant boundary layered on top of the existing single flat global admin/viewer token model — nothing changes for a deployment that never calls `POST /api/v1/orgs`. An org has its own enrollment token and its own admin/viewer bearer tokens (only their SHA-256 hashes are persisted, via `internal/auth.Hash`/`EqualHash` — the same pattern as a site's agent token); `Server.roleForBearer` falls back to checking these hashes after the configured global tokens, so an org token resolves to the same `"admin"`/`"viewer"` role a global token would.

`Server.orgForBearer` resolves which org (if any) a bearer token belongs to. Two small helpers built on it, `callerCanSeeSite(r, siteID)` and `callerCanMutateSite(r, siteID)`, are the single mechanism every org-scoped handler consults:

- **Read** (`callerCanSeeSite`): a global (unscoped) caller sees everything, unchanged from before orgs existed. An org-scoped caller sees a resource whose `site_id` belongs to their org, *and* any fleet-wide resource (`site_id == ""`, e.g. a global route or policy pack) — hiding fleet-wide config from an org would be misleading, since it still applies to that org's sites too.
- **Write** (`callerCanMutateSite`): a global caller can mutate anything. An org-scoped caller can only mutate a resource scoped to one of their *own* sites — never a fleet-wide resource (unlike the read side) and never another org's site. A single-entity mutation whose target belongs to a different org is rejected as `404` (not `403`), so a guessed/enumerated ID doesn't even confirm the target exists; a *create* naming a `site_id` outside the caller's org is rejected as `403` instead, since there's no existing entity whose existence could leak.

This pair is applied consistently across every admin-gated list and single-entity handler: `sites()`/`siteRevoke()`, `devices()`/`twinDesired()`, `twins()`, `routesList()`/`routeCreate()`/`routeDelete()`, `deployments()`/`deploymentCreate()`/`deploymentPatch()`/`deploymentDelete()`, `policyPacks()`/`policyPackCreate()`/`policyPackPatch()`/`policyPackDelete()`, `alerts()`/`alertResolve()`, `eventsList()`, `activityList()`, `deadletters()`/`deadletterReplay()`/`deadletterDelete()`, `auditList()`/`auditExport()`, `otaDeviceRequest()`/`otaDeviceGet()`, `otaCampaigns()`/`otaCampaignCreate()`/`otaCampaignGet()`/`otaCampaignStart()`/`otaCampaignPromote()`/`otaCampaignAbort()`, and `overview()`'s per-entity counts. Org management itself (`POST/GET/DELETE /api/v1/orgs(/{id})`) is gated to the global admin token specifically (`requireGlobalAdmin`) — an org's own admin token cannot create, list, or delete orgs, including itself.

Two residual gaps keep this from being an absolute boundary, both deliberate trade-offs rather than oversights:

- `overview()`'s `pending_deliveries`/`delivery_queue_bytes`/`dead_letters` come from `queue.Stats()`, which has no per-site breakdown, so they stay fleet-wide totals even for an org-scoped caller — an org admin can infer roughly how busy the *whole* fleet's delivery queue is, not just their own.
- `auditList()`/`auditExport()` filter by post-processing each fetched page (`audit.Filter` has no "any site in this org" concept), so a page can come back with fewer than its requested limit for an org-scoped caller even though more matching entries exist further in — `next_cursor` still lets them page forward, this only affects how full one page looks.

Agent-authenticated endpoints (heartbeat, enroll, events ingestion, `agentTwins`/`agentTwinReported`, `agentDeployments`/`agentDeploymentStatus`/`agentDeploymentRollback`, `agentOTAStatus`) were never in scope for this: a site's own agent token already scopes it to itself, org or not.
