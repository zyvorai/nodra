# Changelog

## Unreleased

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
