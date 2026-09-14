# Roadmap

## Current (main)

Offline WAL, backpressure, local routes, MQTT QoS0/1, transactional cloud ACK, concurrent webhook dispatch, replayable DLQ, original event time, device twins, CSR site certificates, site revocation, Docker reconciliation, Modbus TCP building block + **poller connector**, connector registry, orange embedded dashboard with **console write actions**, chaptered login, activity Logs, A–Z `nodra-sim`, demo Helm/Compose, configurable ports, **viewer/admin RBAC**, optional **Postgres fleet store** (`NODRA_STORE=postgres`), zyvor.dev product/docs pages, **Nodra→Relay Accept bridge** (`nodra-relay-bridge`), **local route filter/transform rules** (`internal/transform`), **OPC-UA adapter** (`connectors/opcua`, v1: SecurityPolicy None, anonymous, Read-only, polling only), **durable audit log + export** (`internal/audit`, file or Postgres, `GET /api/v1/audit(/export)`, `nodractl audit list|export`).

## Next

- multi-replica delivery/DLQ HA on top of Postgres (or external queue)
- OIDC/SSO and multi-tenant orgs
- OPC-UA v2: Basic256Sha256 security, Subscribe/MonitoredItems, Write service, endpoint discovery/Browse
- NATS and Zenoh bridges
- signed staged agent OTA campaigns with canary/rollback
- certificate rotation/expiry automation and CRL distribution
- fleet policy packs
- application artifact signatures and health-gated rollback
- serial/USB connector runtime
- MQTT QoS 2 / persistent sessions

## v1.0 criteria

- stable API compatibility policy
- HA control plane including delivery workers
- upgrade/migration guarantees
- multi-day soak tests under WAN loss and disk pressure
- protocol conformance suites
- recovery runbooks and published scale envelope
