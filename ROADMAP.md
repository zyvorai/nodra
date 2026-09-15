# Roadmap

## Current (main)

Offline WAL, backpressure, local routes, MQTT QoS0/1, transactional cloud ACK, concurrent webhook dispatch, replayable DLQ, original event time, device twins, CSR site certificates, site revocation, Docker reconciliation, Modbus TCP building block + **poller connector**, connector registry, orange embedded dashboard with **console write actions**, chaptered login, activity Logs, A–Z `nodra-sim`, demo Helm/Compose, configurable ports, **viewer/admin RBAC**, optional **Postgres fleet store** (`NODRA_STORE=postgres`), zyvor.dev product/docs pages, **Nodra→Relay Accept bridge** (`nodra-relay-bridge`), **local route filter/transform rules** (`internal/transform`), **OPC-UA adapter** (`connectors/opcua`, SecurityPolicy None, anonymous session, polling Read + Write, plus GetEndpoints/FindServers/Browse), **durable audit log + export** (`internal/audit`, file or Postgres, `GET /api/v1/audit(/export)`, `nodractl audit list|export`), **certificate rotation + CRL** (`internal/pki`, nodrad-initiated re-key via `POST /api/v1/sites/{id}/rotate`, unauthenticated `GET /api/v1/ca/crl`, `certificate_expiring` alerts), **NATS bridge** (`connectors/nats`, dependency-free subscribing client — Zenoh explicitly deferred, see `docs/NATS_BRIDGE.md`), **generic serial/USB connector** (`connectors/serial`, delimiter- or idle-gap-framed passthrough poller sharing termios config via `internal/serialport` with Modbus RTU, Linux-only + build-tag stub), **fleet policy packs v1** (`internal/policy`, `allowed_images` allowlist only — no RBAC-rule or alert-threshold packs — enforced at deployment create/patch, see `docs/POLICY_PACKS.md`), **OIDC console login** (`internal/oidc`, RS256-only, a configured group claim maps to the existing admin/viewer roles — no user directory, no multi-tenant orgs), **cosign-verify-before-pull + health-gated rollback** (agent shells out to an externally-installed `cosign` binary before `docker pull`; `signature_mode` enforce/warn/skip, default warn; `POST /api/v1/agent/deployments/{id}/rollback` reverts to the last known-good image/version on binary Docker-health failure past `deploy_health_grace` — not staged/canary campaigns), **single-active-writer delivery/DLQ failover on Postgres** (`NODRA_STORE=postgres`, `internal/queue.PostgresQueue` + `internal/leader`, non-blocking `pg_try_advisory_lock`; automatic failover, not multi-writer HA — file mode is unaffected).

## Next

- true multi-writer delivery/DLQ HA (conflict-resolved, not just single-active-writer failover)
- multi-tenant orgs / per-org site scoping
- OPC-UA v3: Basic256Sha256 security, Subscribe/MonitoredItems
- staged, multi-site agent OTA campaigns with canary rollout (single-deployment cosign-verify + health-gated rollback already shipped; this is the remaining multi-site/staged-rollout piece)
- MQTT QoS 2 / persistent sessions

## v1.0 criteria

- stable API compatibility policy
- HA control plane including delivery workers
- upgrade/migration guarantees
- multi-day soak tests under WAN loss and disk pressure
- protocol conformance suites
- recovery runbooks and published scale envelope
