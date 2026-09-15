---
hero:
  eyebrow: SUPPLY CHAIN
  title: Supply-chain and release model
---

Nodra's core edge runtime stays dependency-light. The **optional** Postgres fleet store pulls `github.com/jackc/pgx/v5` (and its transitive modules) as the first intentional runtime Go dependency. File-store deployments do not require a live Postgres connection.

The tagged GitHub release workflow:

1. cross-compiles `nodra-server`, `nodrad`, `nodractl`, `nodra-sim`, and `nodra-relay-bridge` for Linux amd64/arm64, macOS amd64/arm64, and Windows amd64;
2. creates SHA-256 checksums;
3. builds and pushes a multi-architecture OCI image (includes all five binaries);
4. generates a source SPDX JSON SBOM;
5. requests GitHub build provenance attestation for the pushed image;
6. publishes release archives, the SBOM, and checksums on the GitHub release;
7. keyless cosign signing on tagged releases.

Dependabot monitors GitHub Actions and the Docker base image. CodeQL runs on pushes, pull requests, and a weekly schedule. CI also runs `make qualify` (with `NODRA_QUALIFY_FAST=1` after `release-check`) and uploads `evidence/qualification/software-matrix.json`.

`nodrad` can optionally shell out to an externally-installed `cosign` binary (`NODRA_COSIGN_BIN` override, mirroring the existing `NODRA_DOCKER_BIN` pattern) to verify a deployment image's signature before `docker pull` — see `docs/SECURITY-MODEL.md`. This is an optional external dependency on the deployment host, the same category as `docker` itself; it adds nothing to `go.mod`, since `cosign` is already a first-class part of this project's own release toolchain (installed via `sigstore/cosign-installer` in CI, step 7 above).
