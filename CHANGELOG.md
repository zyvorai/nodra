# Changelog

## 0.2.1 — Industrial transports + production scaffolding

- Add Modbus RTU transport to the existing Modbus connector (TCP remains the default).
- Add Linux termios serial configuration and Modbus CRC16 validation.
- Add `j1939-device-agent` connector for Device Agent RX-only CAN SSE streams.
- Decode 29-bit J1939 PGN/source/destination inside Nodra, not Device Agent.
- Add deterministic CRC, config, J1939 identifier and SSE connector tests.
- Align packaging versions (Makefile, Chart, Dockerfile, OpenAPI, k8s tags) to **0.2.1**.
- Deeper `/readyz` (fleet store ping + delivery/DLQ availability).
- `make qualify` software matrix + PRODUCTION/QUALIFICATION docs; backup/restore scripts.
- Release archives include `nodra-sim` and `nodra-relay-bridge`.

## Unreleased

- OpenAPI route coverage: document missing control-plane routes
  (`/auth/oidc/*`, `/policy-packs`, `/audit`, `/ca/crl`, site rotate,
  agent deployment rollback) and run `scripts/openapi-coverage.py` as an
  explicit CI step on every Go matrix job so qualify no longer fails on
  stale OpenAPI alone.

- Zyvor OTA integration contract (`pkg/ota`): additive Manifest/Request/Status/
  Capability types and A/B lifecycle state machine with transport-level
  validation. `policy.health_timeout` is a Go duration string on the wire
  (e.g. `"5m"`), not nanoseconds. Docs: `docs/OTA_INTEGRATION.md`.

- Zyvor OTA wiring: an OTA request/status now rides the existing Device Twin
  desired/reported mechanism under a reserved `"ota"` key — the same durable
  storage and nodrad-polling delivery every other twin already has, not a
  new path. `POST/GET /api/v1/devices/{id}/ota` (admin), `POST /api/v1/agent/
  devices/{id}/ota/status` (agent — `400` on an invalid status, `409` on an
  illegal `pkg/ota` lifecycle transition, `ota_failed`/`ota_rolled-back`
  alerts on a terminal bad state), and nodrad-local `POST /v1/devices/{id}/
  ota/status` forwarding to it. Single-device request/status only — staged
  multi-site canary campaigns remain future work on `ROADMAP.md`.

- Fleet policy packs v1 (`internal/policy`): a named, versioned,
  fleet-wide-or-per-site `allowed_images` allowlist enforced on deployment
  create/patch. `*` in a pattern matches across `/` (OCI image refs use it
  as an ordinary separator, not a path boundary). Multiple applicable packs
  are ANDed; a pack with no `allowed_images` entries is a no-op; disabled
  packs are never enforced. `GET|POST /api/v1/policy-packs`,
  `PATCH|DELETE /api/v1/policy-packs/{id}`, `nodractl policy`. No RBAC-rule
  or alert-threshold packs — see `docs/POLICY_PACKS.md`.

- OIDC console login (`internal/oidc`): "Sign in with SSO" is a third way
  to obtain the existing admin/viewer bearer tokens, not a new user
  directory and not multi-tenant orgs. ID token verification is hand-rolled
  against stdlib crypto and RS256-only (rejects `alg: none` and any `HS*`
  algorithm — the classic JWT-confusion mitigation). A configured groups
  claim maps to the existing two roles; the minted token is the exact same
  static bearer `login()` already issues for that role. `GET
  /api/v1/auth/oidc/login` and `/callback`; `oidc.login` is audited
  (denials too).

- Cosign-verify-before-pull + health-gated deployment rollback: the agent
  shells out to an externally-installed `cosign` binary (`NODRA_COSIGN_BIN`
  override, mirroring `NODRA_DOCKER_BIN`) before every `docker pull`, gated
  by `signature_mode` (`enforce`/`warn`/`skip`, default `warn` so existing
  unsigned deployments keep working). If a deployment can't reach `running`
  past `deploy_health_grace` (default `60s`) — tracked from first observed
  attempt, not just from a later break, so a deploy that never comes up
  still eventually rolls back — the agent calls the new
  `POST /api/v1/agent/deployments/{id}/rollback`, which reverts to the last
  image/version that was `running` before the change, raises a
  `deployment_rollback` alert, and audits `deployment.rollback`. Binary
  Docker-state health only, not an app-level health check; a single-
  deployment revert, not a staged/canary campaign — that remains on
  `ROADMAP.md`'s Next list.

- True multi-writer delivery/DLQ HA on Postgres
  (`internal/queue.PostgresQueue`): with `NODRA_STORE=postgres`, every
  replica pointed at the same database now claims and processes deliveries
  concurrently instead of routing through a single elected leader.
  `PostgresQueue[T]` implements a new `queue.Claimer` capability
  (`TryClaim`/`ReleaseClaim`) backed by an atomic conditional
  `UPDATE ... RETURNING id` against new `claimed_by`/`claimed_at` columns;
  a claim auto-expires after `Config.DeliveryClaimLease` (default `90s`) so
  a crashed or hung replica's in-flight items become reclaimable rather
  than stuck forever, and `Put` (a fresh delivery, or a reschedule after a
  failed attempt) always clears any existing claim. `internal/leader`'s
  `pg_try_advisory_lock` is still opened in Postgres mode but is no longer
  consulted by delivery processing — retained only in case a future
  singleton background task needs it. File mode (`NODRA_STORE=file`, the
  default) is completely unaffected — still single-process by construction,
  no coordination needed. `nodra_delivery_leader` on `/metrics` now reads 1
  for every actively-processing replica rather than exactly one leader.

- Multi-tenant orgs (`model.Org`): `POST /api/v1/orgs`
  (global-admin-token only) creates a named org and mints its own
  enrollment/admin/viewer bearer tokens, returned in plaintext exactly
  once — only their SHA-256 hashes are persisted, the same pattern as a
  site's agent token. Enrolling with an org's enrollment token (instead of
  the global one) sets the new site's `org_id`; from then on that org's
  admin/viewer tokens resolve to the same `"admin"`/`"viewer"` role a
  global token would, but scoped by two new helpers,
  `Server.callerCanSeeSite`/`callerCanMutateSite`, applied consistently
  across every admin-gated list and single-entity handler: sites, devices,
  twins, routes, deployments, policy packs, alerts, events, activity,
  audit, and dead letters. A fleet-wide resource (empty `site_id` — a
  global route or policy pack) stays visible to every org (it still
  applies to their sites) but is mutable only by the global admin token; a
  single-entity mutation targeting another org's resource is rejected as
  `404` (not `403`, so a guessed ID can't even confirm the target exists);
  a *create* naming another org's `site_id` (or no `site_id` at all, from
  an org-scoped token) is rejected as `403`. `overview`'s per-entity counts
  are org-scoped too, except `pending_deliveries`/`delivery_queue_bytes`/
  `dead_letters`, which come from `queue.Stats()` and have no per-site
  breakdown, so they stay fleet-wide totals. `auditList`/`auditExport`
  org-filter by post-processing each fetched page rather than the
  underlying query, so an org-scoped caller's page can come back thinner
  than its limit even though more history exists — `next_cursor` still
  pages forward correctly. `GET|POST /api/v1/orgs`, `GET|DELETE
  /api/v1/orgs/{id}` are themselves restricted to the global admin token —
  an org's own admin token cannot create, list, or delete orgs. Deleting
  an org does not delete or reassign its sites; they keep their `org_id`
  and simply become invisible to any org-scoped token from then on.
  Agent-authenticated endpoints (heartbeat, enroll, event ingestion, every
  `/agent/*` route) were never in scope — a site's own agent token already
  scopes it to itself. See `docs/ARCHITECTURE.md`'s "Multi-tenant orgs"
  section and `docs/API.md`'s "Multi-tenant orgs" section for the exact,
  still-not-absolute scope.

- MQTT persistent sessions for QoS0/1 (`internal/mqtt`): `CONNECT`'s
  `CleanSession` flag and `ClientID` are now actually parsed (previously
  silently ignored). `Broker.EnableSessions` opts a broker into durably
  queuing messages matched while no live connection holds a `CleanSession=0`
  session, replaying them with `DUP` set on reconnect (`CONNACK`'s
  session-present byte reflects whether a prior session existed);
  `CleanSession=1` discards prior state. Also fixes a pre-existing
  unsynchronized-access race on a client's subscription list, found while
  touching every read/write site for this change. One honesty gap:
  subscription lists are in-memory only and don't survive a broker restart,
  though the durable message queue itself does.

- Inbound MQTT QoS2 (`internal/mqtt`): a publishing client now gets the full
  exactly-once `PUBLISH`/`PUBREC`/`PUBREL`/`PUBCOMP` handshake — delivery to
  the local `Handler`/subscribers is deferred to `PUBREL`, so a retransmitted
  `PUBLISH` (lost `PUBREC`) never double-delivers, and a retransmitted
  `PUBREL` (lost `PUBCOMP`) is idempotent. Outbound QoS2 (broker → subscriber)
  remains unsupported — `SUBSCRIBE` still caps at QoS1, since
  `Broker.Publish`'s fanout has no ack-tracking even for QoS1 today; that's
  a separate, larger piece of work tracked in `ROADMAP.md`.

- OPC-UA connector (`connectors/opcua`): dependency-free UA-TCP binary
  client and poller. SecurityPolicy None, anonymous session; Write,
  GetEndpoints/FindServers and Browse as `Client`/package-level Go API calls
  (not poller config — mirrors how Modbus's own Write isn't wired into its
  poller either). Basic256Sha256 security remains a follow-up in
  `ROADMAP.md` — real asymmetric-crypto protocol work, deliberately not
  attempted without a real server to verify against.

- OPC-UA Subscribe/MonitoredItems (`connectors/opcua`): poller
  `"mode": "subscribe"` opens one long-lived CreateSubscription +
  CreateMonitoredItems + Publish session per node set instead of ticking
  Read, emitting one event per server-pushed value change; reconnects with
  an `interval` backoff on any error. `"mode": "poll"` (the previous, and
  still default, Read-on-a-ticker behavior) is unchanged. This is the
  least-verified part of the package: its multi-step protocol has more
  surface for a subtle wire-format mistake to hide than Read/Write/Browse's
  single request/response did — smoke-test against a real server before
  production use.

- NATS bridge connector (`connectors/nats`): dependency-free, hand-rolled
  NATS core client (INFO/CONNECT/SUB/MSG/PING/PONG/-ERR) that subscribes to
  configured subjects and publishes into the ingest pipeline. No TLS,
  clustering, queue groups, or JetStream in v1. Zenoh is explicitly not
  implemented — its binary wire format has no small hand-rollable path;
  see `docs/NATS_BRIDGE.md`.

- Generic serial/USB connector (`connectors/serial`): a protocol-agnostic
  passthrough poller — opens a serial device and publishes each delimited
  (default `\n`) or idle-gap-framed chunk of bytes as a Nodra event, with no
  application-protocol decoding. Linux-only, same build-tag-stub pattern as
  Modbus RTU. Termios configuration (baud/parity/stop-bits ioctls, the
  non-blocking EAGAIN/EWOULDBLOCK/spurious-EOF-tolerant read loop) was
  extracted out of `connectors/modbus/rtu.go` into a new shared
  `internal/serialport` package — **a refactor, not a behavior change**;
  `connectors/modbus`'s existing RTU unit/integration tests
  (`rtu_test.go`/`rtu_integration_test.go`) were re-verified against it.

- Local route filter/transform rules (`internal/transform`): per-route `filter`
  (`exists`/`equals`/`min`/`max`/`in`) drops events locally before delivery;
  `transform` sets headers, drops/sets JSON fields, wraps the payload, and
  rewrites the delivered topic — deterministic, data-only, no scripting.

- Durable, exportable audit log (`internal/audit`): admin console actions
  (login, site revoke, route/deployment create/patch/delete, alert resolve)
  and agent-side actions (enroll, local auth denials, device-register) are
  now written to daily-rotated NDJSON under `<data-dir>/audit/` (or the
  `nodra_audit_log` Postgres table with `NODRA_STORE=postgres`) — durable and
  captured by `backup-state.sh`, unlike the in-memory Activity/Logs ring.
  Query with `GET /api/v1/audit` (control plane) / `GET /v1/audit` (agent),
  or export the full history as NDJSON via `GET /api/v1/audit/export` and
  `nodractl audit list|export`.

- Certificate rotation + CRL distribution (`internal/pki`): nodrad now
  self-rotates its identity certificate ahead of expiry (`cert_rotate_before`,
  default 30 days) via `POST /api/v1/sites/{id}/rotate` — the edge generates
  a fresh keypair and CSR, the old identity stays valid until the new one is
  durably written. The control plane serves a real X.509 CRL at
  `GET /api/v1/ca/crl` (unauthenticated, same trust tier as the CA cert
  already returned from `/api/v1/enroll`), regenerated on every revoke and
  periodically, and raises a `certificate_expiring` alert as a site's
  certificate approaches expiry. A revoked site gets a distinguishable
  `403 site_revoked` (vs. a generic 401) from heartbeat and rotate, so nodrad
  stops trying to rotate a certificate the server will never re-sign.

- Docs refresh: QUALIFICATION/PRODUCTION mark abbreviated WAN/disk + HTTPS
  `:18447` signed; multi-hour soak still open. Remove stale “TLS still blocked
  on lab HTTP” wording.

- Lab production hardening + signed ops/WAN/disk drills (2026-09-14); TLS
  unblocked via `NODRA_TLS_*` on lab HTTPS `:18447`.

- Signed lab ops checklist (backup/restore/TLS); abbreviated WAN/disk drills
  signed (not multi-hour soak).

- GitHub CI lab substitutes: relay-bridge smoke, compose stack, compose
  relay-bridge example, backup/restore drill, suite-ci qualify markers.

## 0.2.2 — 2026-09-14

Production scaffolding follow-ups on top of 0.2.1: OpenAPI coverage, Postgres CI, Docker reconcile tests, console/RBAC/sim/bridge surfaces already on main.

- OpenAPI covers agent/admin mutation routes + ops probes; `scripts/openapi-coverage.py` gates qualify.
- CI `postgres` job runs `TestPostgresRoundTrip` against Postgres 16.
- Docker reconcile unit tests via `NODRA_DOCKER_BIN` fake CLI shim.
- Nodra → Zyvor Relay Accept bridge (`nodra-relay-bridge`): map cloud-route webhooks to `POST /v1/events` (direct or relay-pubsub gateway).
- Console write actions: site revoke, route create/delete, twin desired, deployment create/start-stop/delete, alert resolve, DLQ delete.
- Deployment `PATCH` / `DELETE` APIs and `nodractl deployments patch|delete`.
- Viewer/admin RBAC: `NODRA_VIEWER_TOKEN` (+ optional viewer user/password); GETs for both roles, mutations admin-only; console hides write controls for viewers.
- Optional Postgres fleet store: `NODRA_STORE=postgres` + `NODRA_DATABASE_URL` (`store.Backend`); file WAL remains default. Delivery/DLQ stay local WAL.
- Connector registry + Modbus TCP poller wired into `nodrad` ingest (`connectors` in agent config).
- First intentional Go module dependency: `github.com/jackc/pgx/v5` (Postgres driver only).
- Console login: Kryton-style chaptered gate; `POST /api/v1/auth/login`, `GET /api/v1/auth/me`; `NODRA_ADMIN_USER` / `NODRA_ADMIN_PASSWORD`.
- Live **Logs** tab and overview activity preview via `GET/POST /api/v1/activity`.
- `nodra-sim` A–Z fleet simulator (seed + continuous heartbeats/telemetry/twins/activity).
- Helm `values-demo.yaml`, Compose simulator service, `scripts/demo-k8s.sh`, `scripts/demo-client.sh`.
- Configurable ports across deploy, Compose, Helm NodePort, smoke, and `scripts/test-all.sh`.
- Empty list APIs always return `[]` (never `null`) for safe console rendering.
- Remote smoke (`scripts/smoke-remote.sh`) covers login + activity.
- Marketing/docs on zyvor.dev: `/nodra` and `/docs/nodra`.

## 0.2.0

- Replace file-per-event spool with fsynced append-only WAL queues.
- Replace whole-state rewrites with state WAL + snapshots.
- Add byte/event quotas and explicit backpressure policy.
- Preserve edge `event_time` and separate control-plane `ingested_at`.
- ACK cloud ingress only after durable delivery + event persistence.
- Add deterministic delivery IDs, bounded concurrent dispatch and per-route timeouts.
- Add replayable dead-letter queue APIs/CLI/UI.
- Add MQTT 3.1.1 CONNECT/SUBSCRIBE/PUBLISH QoS0/1 edge broker.
- Add offline local HTTP routes with durable local retry queue.
- Add Device Twin desired/reported synchronization and local cache.
- Add CSR-based optional site certificates and site revocation.
- Add desired-state Docker reconciliation.
- Add connector SDK and dependency-free Modbus TCP client.
- Expand Prometheus queue metrics.
- Replace dashboard with multi-page orange Zyvor-inspired control center and safe DOM rendering.
- Update Kubernetes/Helm packaging and release CI/SBOM/provenance/signing.

## 0.1.0

Initial public edge runtime MVP.
