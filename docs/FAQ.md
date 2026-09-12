# FAQ

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

**Is this production-ready?** The README is explicit: "v0.2.0 is a serious
single-control-plane release... The control plane uses an embedded
append-only WAL and intentionally runs as one writer/replica. Horizontal HA
is a future storage mode, not a claim in this release." [`ROADMAP.md`](https://github.com/zyvorai/nodra/blob/main/ROADMAP.md)
lists what's still required before v1.0: a stable API compatibility policy,
an HA control plane including delivery workers, upgrade/migration
guarantees, multi-day soak tests under WAN loss and disk pressure, protocol
conformance suites, and published recovery runbooks/scale envelope. If your
deployment needs HA today, it isn't there yet.

**What's the current version?** v0.2.1 (adds Modbus RTU and J1939 industrial
transports) — see `CHANGELOG.md`.

## Protocol support

**What protocols does it actually speak today?** Implemented: MQTT 3.1.1
(QoS 0/1 — persistent sessions and QoS 2 are explicitly not claimed in
v0.2), HTTP ingress, Modbus TCP and RTU. **Roadmap, not shipped**: OPC-UA,
serial, NATS, Zenoh, a Kafka bridge — the connector registry
(`pkg/connector`) has scaffolding for these but they are not built. Check
`docs/INDUSTRIAL_PROTOCOLS.md` and `ROADMAP.md` before assuming a protocol
is supported.

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
for the three trust boundaries (device→nodrad, nodrad→control plane,
operator→API/console) and RBAC. There is no compliance certification
(SOC2/ISO) claimed in this repository as of writing.
