---
hero:
  eyebrow: SECURITY MODEL
  title: Security model
---

## Trust boundaries

1. **Local device/app -> nodrad**: local HTTP ingest requires `local_token` unless `allow_unauthenticated_local` is set. MQTT is anonymous unless `mqtt_username` and `mqtt_password` are set, or `mqtt_clients` lists per-device usernames. Each client in that list may publish and subscribe only to its granted topic filters; an empty list denies that direction. `mqtt_max_clients` and `mqtt_max_publish_per_minute` close extra connections and disconnect a client that publishes too fast. Zero means no cap. Set `mqtt_cert_file` and `mqtt_key_file` to terminate TLS 1.2 or newer on the broker. `mqtt_client_ca_file` verifies a presented client certificate; `mqtt_require_client_cert` refuses a client that presents none. With those fields empty the listener stays cleartext.
2. **nodrad -> control plane**: bearer site credentials are supported; optional client certificates provide mTLS identity. `NODRA_REQUIRE_CLIENT_CERT` switches that listener to `RequireAndVerifyClientCert` when a client CA is configured. Console login and OIDC mint short-lived sessions (`NODRA_SESSION_TTL`, default 1h) via `/api/v1/auth/login` and `/api/v1/auth/oidc/*`; refresh with `POST /api/v1/auth/refresh` and revoke with `POST /api/v1/auth/logout`. The embedded console stores `expires_at` and refreshes before expiry. Sessions persist under `<data-dir>/sessions.json` across control-plane restarts (still not shared across live replicas). Static `NODRA_ADMIN_TOKEN` / `NODRA_VIEWER_TOKEN` remain valid for automation. Custom roles (`POST /api/v1/roles`) mint additional write or read-only bearers and persist under the data directory as `roles.json`. `POST /api/v1/enrollment-token/rotate` replaces the bootstrap enrollment token; `NODRA_ENROLLMENT_TOKEN_TTL` can expire it. `POST /api/v1/ztp/bootstrap` and `nodrad ztp` enroll a site and write `nodrad.json` without a prior agent config. Console login and site enrollment each return 429 after five failures from the same address in five minutes.
3. **operator -> management API**: admin bearer token (`NODRA_ADMIN_TOKEN`) or optional viewer bearer (`NODRA_VIEWER_TOKEN`, GET-only).
4. **operator -> web console**: username/password for admin or viewer. Successful login returns the matching bearer; the token and role are held in `sessionStorage`.
5. **control plane -> webhook destination**: HTTPS is recommended. Route headers may carry destination credentials; protect the control-plane data volume.
6. **simulator -> control plane**: `nodra-sim` uses the same admin and enrollment secrets as operators; treat it as a privileged demo client, not a public surface.

## Enrollment

The enrollment token is a bootstrap secret. Site bearer tokens are random and only their SHA-256 hash is persisted centrally. Five wrong enrollment tokens from the same address within five minutes return 429.

When PKI enrollment is enabled, the edge generates an ECDSA P-256 private key and CSR. Only the CSR is sent. The control plane signs it; the private key never leaves the site.

## SSO / OIDC login

`NODRA_OIDC_ISSUER_URL` + `NODRA_OIDC_CLIENT_ID` add "Sign in with SSO" as a third way to obtain the console's existing admin/viewer bearer tokens. Scope, stated precisely:

- ID token verification is **RS256 only** — `alg: none` and any `HS*` algorithm are explicitly rejected (the classic JWT algorithm-confusion mitigation). Verification is hand-rolled against stdlib `crypto/rsa`, not a third-party JWT library.
- A configured groups claim (`NODRA_OIDC_GROUPS_CLAIM`, default `groups`) maps to the SAME two roles admin/viewer already have — `NODRA_OIDC_ADMIN_GROUP` / `NODRA_OIDC_VIEWER_GROUP` decide which. There is no third role and no per-permission granularity beyond what admin/viewer already grant.
- A successful OIDC login mints a short-lived console session for admin or viewer (same roles as password login). Static admin/viewer bearer tokens remain for automation. There is no separate OIDC cookie jar beyond that session store.
- Explicit non-goals: no per-user account directory (the audit actor is `oidc:<sub>`, not a stored user record), no multi-tenant organizations, no per-org site scoping. Those would need a real user/org data model and are tracked separately in `ROADMAP.md`.
- The OIDC login `state`/`nonce` pair is held in-memory only (10-minute TTL, single-use) — it does not survive a control-plane restart mid-flow, and is not shared across replicas.

## Docker image signature verification (cosign)

`nodrad` can shell out to an externally-installed `cosign` CLI before every `docker pull`, gated by `signature_mode` (agent default) or a per-deployment override:

- `enforce` refuses to pull an image that fails `cosign verify` — the deployment is marked `failed` and never runs.
- `warn` (the default, so existing unsigned deployments keep working unchanged) logs the failure, audits it, and pulls anyway.
- `skip` disables verification entirely.

What this actually guarantees: nodrad trusts whatever `cosign` binary and version happen to be on the agent host's `PATH` — the same trust model this project already has for `docker` itself, not a new kind of dependency. `cosign_certificate_identity_regexp`/`cosign_certificate_oidc_issuer` are passed straight through to `cosign verify --certificate-identity-regexp`/`--certificate-oidc-issuer`; nodrad does not parse, cache, or independently validate the signature/attestation itself. A compromised or absent `cosign` binary defeats this control entirely — it is a deployment-time gate on the agent host, not a control-plane-enforced invariant.

## Health-gated deployment rollback

After a `docker pull`/`run`, if a deployment stays unable to reach the `running` state past a configurable grace period (`deploy_health_grace`, default 60s), nodrad asks the control plane to revert it to the last image/version that was observed `running` before the change (`POST /agent/deployments/{id}/rollback`). This is binary health only — Docker's own running/not-running state via `docker inspect` — not a real application-level health check (no such contract exists yet), and not a staged or canary rollout; a rollback is an all-or-nothing revert of one deployment, audited (`deployment.rollback`) and raised as a `deployment_rollback` alert.

## Kubernetes

The default manifests enforce Restricted Pod Security, run as UID/GID 65532, drop all capabilities, use RuntimeDefault seccomp, use read-only root filesystems and disable service-account token mounting.

Demo values inject admin user/password into the control-plane Secret for console login. Optional viewer token and Postgres URL keys are supported. Change defaults before any shared or internet-facing lab.

## Operational guidance

- terminate public traffic with TLS;
- rotate admin/enrollment/viewer tokens after exposure;
- revoke a lost site from the control plane;
- keep agent data volumes encrypted where device telemetry is sensitive;
- do not enable Docker reconciliation unless the node is intentionally permitted to manage containers;
- prefer `spool_policy=reject` for loss-sensitive data;
- do not expose continuous `nodra-sim` against production fleets;
- do not treat `--public-read` as a substitute for the viewer role.
