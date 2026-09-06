# Nodra v0.2.0 — Test Results

Release validation date: 2026-09-06

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
- all three static binaries build (`nodra-server`, `nodrad`, `nodractl`): PASS
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

## v0.2.0 intentional boundaries

- MQTT supports the documented v0.2 subset; QoS 2 and persistent MQTT sessions are not implemented yet.
- The control plane remains intentionally single-writer; horizontal HA is not claimed for the embedded WAL state engine.
- OPC-UA, NATS and Zenoh are roadmap connectors, not v0.2 claims.
- Docker workload reconciliation requires a Docker runtime on the edge node and is opt-in.

## Exact ZIP verification

The final source ZIP is extracted into a clean directory, `MANIFEST.sha256` is verified, and `./scripts/release-check.sh` is rerun from that extracted copy before publication. The external checksum file records the immutable ZIP SHA-256.
