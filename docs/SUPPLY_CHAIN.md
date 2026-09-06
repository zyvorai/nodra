# Supply-chain and release model

Nodra's runtime code uses only the Go standard library. There are no third-party Go runtime modules in v0.1.

The tagged GitHub release workflow:

1. cross-compiles `nodra-server`, `nodrad`, and `nodractl` for Linux amd64/arm64, macOS amd64/arm64, and Windows amd64;
2. creates SHA-256 checksums;
3. builds and pushes a multi-architecture OCI image;
4. generates a source SPDX JSON SBOM;
5. requests GitHub build provenance attestation for the pushed image;
6. publishes release archives, the SBOM, and checksums on the GitHub release.

Dependabot monitors GitHub Actions and the Docker base image. CodeQL runs on pushes, pull requests, and a weekly schedule.
