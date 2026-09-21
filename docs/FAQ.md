---
hero:
  eyebrow: FAQ
  title: FAQ
---

Questions people evaluating Nodra actually ask, before they've decided to
adopt it. Already decided? [`docs/DEMO.md`](DEMO.md) and
[https://zyvor.dev/docs/nodra](https://zyvor.dev/docs/nodra) are better
starting points.

## Licensing & cost

**Is it really free?** Yes. Apache-2.0 — use, modify, and run it for
personal, lab, and commercial production use at no charge, subject to
preserving notices (see [`NOTICE`](https://github.com/zyvorai/nodra/blob/main/NOTICE)). See the README's
[License](https://github.com/zyvorai/nodra#license) section.

**What does "Enterprise" mean here?** Production support, SLAs, and
Zyvor's other commercial products are licensed separately from this
open-source runtime. Contact sales@zyvor.dev. Nothing in this repository
requires it.

## Support

**What if I find a bug?** Open a GitHub issue.

**What if I find a security vulnerability?** See [`SECURITY.md`](https://github.com/zyvorai/nodra/blob/main/SECURITY.md)
for private reporting — supported versions are `main` plus the latest
tagged minor release only. See [`docs/SECURITY-MODEL.md`](SECURITY-MODEL.md)
for the trust-boundary model.

## Production readiness

**Is this production-ready?** Current release is **v0.2.2**: a serious
single-control-plane product. Edge sites are offline-first. File mode is a
tested single-replica deployment. PostgreSQL fleet state is read from the
database, with revision checks so replicas cannot silently overwrite each
other, and delivery workers already claim concurrently. A cross-replica test
covers that consistency. Configured limits and a lab ingress observation are
in [`docs/SCALE.md`](SCALE.md). A four-hour lab soak passed 2026-09-21; a 24h
lab soak is in progress (not signed until `soak-check.py` passes). 72h/7d,
PITR, and full HA remain open. Helm defaults to one replica and refuses to
scale file mode.
Run `make qualify` and sign [`docs/QUALIFICATION.md`](QUALIFICATION.md) /
[`evidence/qualification/ops-checklist.md`](https://github.com/zyvorai/nodra/blob/main/evidence/qualification/ops-checklist.md)
before go-live. See [`docs/PRODUCTION.md`](PRODUCTION.md).
[`ROADMAP.md`](https://github.com/zyvorai/nodra/blob/main/ROADMAP.md)
lists what's still required before v1.0: a stable API compatibility policy,
a full HA control plane, completed multi-day soak tests (24h in progress;
72h/7d unrun), protocol conformance suites, and recovery runbooks. Upgrade and
rollback steps for v0.2.0/v0.2.1 → current are in
[`docs/UPGRADE.md`](UPGRADE.md). If your deployment needs full HA today, it
isn't there yet.

**What's the current version?** v0.2.2 — see `VERSION` and `CHANGELOG.md`.
v0.2.1 added Modbus RTU and J1939.

## Protocol support

**What protocols does it actually speak today?** Implemented: MQTT 3.1.1
(QoS 0/1/2 in both directions; optional TLS and client certificates on the
edge broker; persistent sessions replay queued QoS 1 and QoS 2;
subscription lists do not survive a broker restart), HTTP ingress,
Modbus TCP and RTU, J1939 from a Device Agent CAN-capture stream, OPC-UA
(SecurityPolicy None and anonymous sessions; Basic256Sha256 channel crypto
is not implemented), a Linux serial connector, and a NATS subscribe bridge.
Zenoh and a Kafka bridge are not implemented. See
`docs/INDUSTRIAL_PROTOCOLS.md`, `docs/NATS_BRIDGE.md`, and `ROADMAP.md`.

**How does it relate to Zyvor Device Agent?** They have a deliberate
boundary: "This increment keeps protocol semantics in Nodra while Zyvor
Device Agent owns only physical hardware discovery/capture"
(`docs/INDUSTRIAL_PROTOCOLS.md`). The J1939 connector, for example,
consumes Device Agent's read-only CAN capture SSE stream rather than
touching CAN hardware itself.

**How does it relate to Zyvor Relay?** `docs/RELAY.md` describes Nodra as
keeping the edge online while "Zyvor Relay owns the durable ops loop" —
they're bridged via a separate `nodra-relay-bridge` component, not merged
into one process.

## Cloud & offline operation

**Does this require a cloud account or connection?** No — offline-first is
the core design point, not a fallback mode. Local MQTT/HTTP ingress, local
routes, and store-and-forward all work with zero external connectivity;
the durable WAL exists specifically so a WAN outage doesn't lose data,
syncing to the control plane on reconnect.

## Deployment model

**Kubernetes or bare metal?** Both are first-class: a Helm chart and raw
Kustomize manifests for the control plane, plus systemd unit files
(`deployments/systemd/`) for bare-metal/edge agent installs. See the
README's Kubernetes section.

## Security

**What's the trust model?** See [`docs/SECURITY-MODEL.md`](SECURITY-MODEL.md)
for the six trust boundaries (device→nodrad, nodrad→control plane,
operator→API, operator→console, control plane→webhook, simulator→control
plane) and RBAC. Local HTTP requires `local_token` by default. MQTT can
require passwords, topic ACLs, connection and publish caps, and optional
broker TLS or client certificates. Control-plane HTTPS can require client
certificates. Login and enrollment return 429 after five failures from one
address in five minutes. There is no compliance certification (SOC2/ISO)
claimed in this repository as of writing.
