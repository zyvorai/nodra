# Nodra v0.2.0 — Test Results

Release validation date: 2026-09-06

Post-release main line (docs as of later same cycle) also exercises console login, activity Logs, `nodra-sim`, and remote smoke; see Unreleased in [CHANGELOG.md](CHANGELOG.md) and [docs/DEMO.md](docs/DEMO.md).

## Result

**PASS** for the locally executable release gate.

The release candidate was validated from source and is also revalidated after extraction of the final ZIP (see the final verification section below).

## Local environment

- Go: `go1.23.2 linux/amd64`
- Node.js: `v22.16.0`
- Runtime dependencies: Go standard library only
- CI/release target toolchain: Go 1.27.x

## Checks executed locally

- `gofmt` cleanliness: PASS
- `go vet ./...`: PASS
- `go test -race ./...`: PASS
- 36 named Go test functions: PASS
- static binaries build (`nodra-server`, `nodrad`, `nodractl`; `nodra-sim` on current main): PASS
- live control-plane + edge-agent smoke flow: PASS
- site enrollment and authenticated heartbeat: PASS
- durable HTTP edge publish and cloud flush: PASS
- original `event_time` preservation: PASS
- transactional server ACK behavior on durable queue failure: PASS
- event deduplication: PASS
- durable retry queue and DLQ replay: PASS
- bounded edge spool and HTTP 507 backpressure behavior: PASS
- offline local-route execution: PASS
- MQTT 3.1.1 CONNECT/SUBSCRIBE/QoS0-1 PUBLISH/PUBACK test: PASS
- Modbus TCP read/write connector test: PASS
- Device Twin desired/reported synchronization: PASS
- CSR-based certificate enrollment and certificate verification: PASS
- control-plane/queue WAL replay and compaction tests: PASS
- route wildcard matching: PASS
- dashboard HTTP/security-header smoke: PASS
- dashboard JavaScript `node --check`: PASS
- Prometheus metrics smoke: PASS
- GitHub workflow, Kubernetes raw manifest and OpenAPI YAML parse: PASS (14 YAML files in release-check)
- README/license release assertions: PASS

## Coverage snapshot

`go test -coverprofile` aggregate statement coverage: **45.3%**.

Notable packages:

- `internal/router`: 100.0%
- `internal/auth`: 92.9%
- `internal/pki`: 73.6%
- `internal/durable`: 66.7%
- `internal/server`: 64.5%
- `internal/queue`: 62.2%
- `connectors/modbus`: 59.7%
- `internal/mqtt`: 46.5%
- `internal/agent`: 35.5%
- `internal/store`: 33.1%

CLI `main` packages are exercised by the live smoke test but are not directly instrumented by unit coverage.

## Tooling not available in this build environment

The following binaries are not installed locally, so their native commands were **not** claimed as locally executed:

- Docker
- Helm
- kubectl
- Kustomize
- kind

The repository includes GitHub Actions jobs for container build, Helm/Kustomize/Kubernetes validation and kind end-to-end deployment. Tagged releases also wire SBOM/provenance and keyless signing. These jobs must pass in GitHub CI before calling a tagged artifact production-validated.

## Current boundaries (main)

The checks above are the 2026-09-06 v0.2.0 gate. Current main also has:

- MQTT QoS 0/1/2 in both directions. Persistent sessions replay queued QoS 1 and QoS 2. Subscription lists are in-memory and do not survive a broker restart.
- File mode is a tested single-replica deployment. Postgres fleet state is read from the database with revision checks, and delivery workers claim concurrently. Full HA is not claimed.
- Modbus TCP and RTU, J1939 via Device Agent capture, OPC-UA (SecurityPolicy None or Basic256Sha256 Sign/SignAndEncrypt, anonymous user token), Linux serial, and a NATS subscribe bridge. Zenoh and Kafka are not implemented.
- Docker workload reconciliation requires a Docker runtime on the edge node and is opt-in.
- Console activity Logs are in-memory only (not part of durable backup).
- A four-hour lab soak passed 2026-09-21; a 24h lab soak is in progress (not signed until judged). Configured limits and lab ingress observations are in [docs/SCALE.md](docs/SCALE.md).

## Exact ZIP verification

The final source ZIP is extracted into a clean directory, `MANIFEST.sha256` is verified, and `./scripts/release-check.sh` is rerun from that extracted copy before publication. The external checksum file records the immutable ZIP SHA-256.
