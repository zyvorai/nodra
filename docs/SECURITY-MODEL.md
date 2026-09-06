# Security model

## Trust boundaries

1. **Local device/app -> nodrad**: protect HTTP ingress with `local_token`; use a network boundary/TLS termination for MQTT where required.
2. **nodrad -> control plane**: bearer site credentials are supported; optional client certificates provide mTLS identity.
3. **operator -> management API**: admin bearer token.
4. **control plane -> webhook destination**: HTTPS is recommended. Route headers may carry destination credentials; protect the control-plane data volume.

## Enrollment

The enrollment token is a bootstrap secret. Site bearer tokens are random and only their SHA-256 hash is persisted centrally.

When PKI enrollment is enabled, the edge generates an ECDSA P-256 private key and CSR. Only the CSR is sent. The control plane signs it; the private key never leaves the site.

## Kubernetes

The default manifests enforce Restricted Pod Security, run as UID/GID 65532, drop all capabilities, use RuntimeDefault seccomp, use read-only root filesystems and disable service-account token mounting.

## Operational guidance

- terminate public traffic with TLS;
- rotate admin/enrollment tokens after exposure;
- revoke a lost site from the control plane;
- keep agent data volumes encrypted where device telemetry is sensitive;
- do not enable Docker reconciliation unless the node is intentionally permitted to manage containers;
- prefer `spool_policy=reject` for loss-sensitive data.
