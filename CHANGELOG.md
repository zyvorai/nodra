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

- OPC-UA connector (`connectors/opcua`): dependency-free UA-TCP binary client
  and poller. v1 scope is deliberately narrow — SecurityPolicy None,
  anonymous session, Read service only, polling only, no endpoint
  discovery/Browse. Basic256Sha256, Write and Subscribe/MonitoredItems are
  tracked as v2 follow-ups in `ROADMAP.md`.

- Local route filter/transform rules (`internal/transform`): per-route `filter`
  (`exists`/`equals`/`min`/`max`/`in`) drops events locally before delivery;
  `transform` sets headers, drops/sets JSON fields, wraps the payload, and
  rewrites the delivered topic — deterministic, data-only, no scripting.

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
