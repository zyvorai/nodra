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

- OPC-UA connector (`connectors/opcua`): dependency-free UA-TCP binary
  client and poller. SecurityPolicy None, anonymous session; Read polled by
  the connector, plus Write, GetEndpoints/FindServers and Browse as
  `Client`/package-level Go API calls (not poller config — mirrors how
  Modbus's own Write isn't wired into its poller either). Basic256Sha256
  security and Subscribe/MonitoredItems are tracked as v3 follow-ups in
  `ROADMAP.md` — the former needs real asymmetric-crypto protocol work, the
  latter a persistent-connection async-push architecture, neither an
  incremental extension of the current ticker-driven poller.

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
