---
hero:
  eyebrow: SECURITY MODEL
  title: Security model
---

## Trust boundaries

1. **Local device/app -> nodrad**: protect HTTP ingress with `local_token`; use a network boundary/TLS termination for MQTT where required.
2. **nodrad -> control plane**: bearer site credentials are supported; optional client certificates provide mTLS identity.
3. **operator -> management API**: admin bearer token (`NODRA_ADMIN_TOKEN`) or optional viewer bearer (`NODRA_VIEWER_TOKEN`, GET-only).
4. **operator -> web console**: username/password for admin or viewer. Successful login returns the matching bearer; the token and role are held in `sessionStorage`.
5. **control plane -> webhook destination**: HTTPS is recommended. Route headers may carry destination credentials; protect the control-plane data volume.
6. **simulator -> control plane**: `nodra-sim` uses the same admin and enrollment secrets as operators; treat it as a privileged demo client, not a public surface.

## Enrollment

The enrollment token is a bootstrap secret. Site bearer tokens are random and only their SHA-256 hash is persisted centrally.

When PKI enrollment is enabled, the edge generates an ECDSA P-256 private key and CSR. Only the CSR is sent. The control plane signs it; the private key never leaves the site.

## SSO / OIDC login

`NODRA_OIDC_ISSUER_URL` + `NODRA_OIDC_CLIENT_ID` add "Sign in with SSO" as a third way to obtain the console's existing admin/viewer bearer tokens. Scope, stated precisely:

- ID token verification is **RS256 only** — `alg: none` and any `HS*` algorithm are explicitly rejected (the classic JWT algorithm-confusion mitigation). Verification is hand-rolled against stdlib `crypto/rsa`, not a third-party JWT library.
- A configured groups claim (`NODRA_OIDC_GROUPS_CLAIM`, default `groups`) maps to the SAME two roles admin/viewer already have — `NODRA_OIDC_ADMIN_GROUP` / `NODRA_OIDC_VIEWER_GROUP` decide which. There is no third role and no per-permission granularity beyond what admin/viewer already grant.
- A successful OIDC login mints the exact same static bearer token `/auth/login` issues for that role — there is no separate OIDC session, no cookie, no token expiry distinct from the admin/viewer token's own lifetime.
- Explicit non-goals: no per-user account directory (the audit actor is `oidc:<sub>`, not a stored user record), no multi-tenant organizations, no per-org site scoping. Those would need a real user/org data model and are tracked separately in `ROADMAP.md`.
- The OIDC login `state`/`nonce` pair is held in-memory only (10-minute TTL, single-use) — it does not survive a control-plane restart mid-flow, and is not shared across replicas.

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
