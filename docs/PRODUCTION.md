---
hero:
  eyebrow: PRODUCTION
  title: Production operations runbook — Nodra
---

Companion to [OPERATIONS.md](OPERATIONS.md) and [SECURITY-MODEL.md](SECURITY-MODEL.md).
For a multi-product evaluation host (Fleet + OTA + Device Agent + Nodra), see
sibling repos' `docs/LAB.md` — that path is not a multi-replica production
control plane.

## Current maturity (2026-09-21)

| Claim | Status |
|---|---|
| Software matrix + CI lab substitutes | green (`make qualify`, bridge/compose/backup CI, postgres dump/restore, production Helm kubeconform) |
| Ops checklist (backup/restore/single-replica) | **signed** — [ops-checklist.md](https://github.com/zyvorai/nodra/blob/main/evidence/qualification/ops-checklist.md) |
| TLS on lab CP | **done** — `NODRA_TLS_CERT`/`KEY` HTTPS on `:18447` |
| MQTT broker TLS | **done** — optional `mqtt_cert_file` / `mqtt_key_file`; optional client certificates |
| Abbreviated WAN + disk drills | **signed** — `lab/20260914T162245Z/nodra-soak/` |
| HA / multi-writer | **partial** — Postgres fleet state is read from the database with revision checks, and delivery workers claim concurrently. Full HA (a passing multi-day soak, PITR, remaining 1.0 gates) is **open**. File mode stays one replica. Configured limits: [SCALE.md](SCALE.md) |
| Multi-hour WAN / disk soak | **signed** — four-hour lab run 2026-09-21 on `80.79.5.173` (`NODRA_SOAK_LAB=1`); `soak-check.py` passed. Evidence: `evidence/qualification/lab/soak-4h-20260921T184846Z-*` |
| Multi-day WAN / disk soak | **open** — 24h, 72h, and 168h need a self-hosted runner. The hosted job timeout is 330 minutes |

**Verdict:** file mode is **production-capable** as a tested single-replica deployment when TLS + spool policy are set
per this runbook and the ops checklist is signed on the target host. PostgreSQL shares fleet state and lets delivery workers claim concurrently; that is not a full HA claim. Lab
reference host already runs HTTPS `:18447`. Use [values-production.yaml](../charts/nodra/values-production.yaml) for a single-replica Postgres install ([DEPLOYMENT.md](DEPLOYMENT.md)).

## Preconditions

1. Software matrix green: `make qualify` → `evidence/qualification/software-matrix.json`.
2. Ops checklist signed: `evidence/qualification/ops-checklist.md`.
3. One control-plane replica for file mode. Helm refuses `replicaCount` above 1 in that mode. Postgres may run more than one replica; the default remains 1 until full HA is qualified.
4. Strong `NODRA_ADMIN_TOKEN` / passwords; rotate `NODRA_ENROLLMENT_TOKEN` after bootstrap.
5. TLS at Ingress or direct `--tls-cert`/`--tls-key`. On the edge, set MQTT TLS when the broker is reachable beyond a trusted LAN. Never expose `--public-read` or demo tokens on shared networks.
6. Choose spool policy deliberately (`reject` for loss-sensitive telemetry).
7. Prefer `charts/nodra/values-production.yaml` for Kubernetes installs that need Postgres, existing Secret, cert-manager, and egress NetworkPolicy.

## Day-2 monitoring

- Scrape `/metrics` only from a restricted network path (unauthenticated).
- Alert on rising `nodra_pending_deliveries`, `nodra_delivery_queue_bytes`, `nodra_dead_letters`, and offline sites.
- Treat `/readyz` ≠ 200 as a page — store ping or queue unavailability means the writer must not receive traffic.

## Backup and restore

```bash
# Prefer stopping the single writer (or filesystem snapshot) first.
./scripts/backup-state.sh /var/lib/nodra /var/backups/nodra-$(date -u +%Y%m%d).tar.gz

# On recovery host — writer stopped, empty or forced target:
NODRA_RESTORE_FORCE=1 ./scripts/restore-state.sh /var/backups/nodra-….tar.gz /var/lib/nodra
# start one replica; curl -fsS http://127.0.0.1:PORT/readyz
```

Do **not** dual-mount the same WAL directory on two live processes.

## Needs attention (known v0.2.x limits)

- File mode has no multi-replica delivery plane — failover is restore-from-backup. Postgres delivery workers claim concurrently, and fleet updates use `revision`. The repaired four-hour soak has not passed. PITR and the rest of the 1.0 gates are still open. Limits are in [SCALE.md](SCALE.md).
- The console's live Activity/Logs tail is in-memory (cap 2000) and is **not**
  in backups — but every actor-attributable action it shows is durably
  written to `audit/` (or Postgres) under the data directory, **is** in
  backups, and is queryable/exportable via `GET /api/v1/audit` and
  `nodractl audit export`.
- MQTT QoS 0/1/2 works in both directions. Persistent sessions replay queued QoS 1 and QoS 2. Subscription lists are in-memory and do not survive a broker restart.
- Compose / Helm demo values are evaluation-only. `charts/nodra/values-production.yaml` is the single-replica PostgreSQL profile (existing Secret, cert-manager, egress policy, startup probe). It is not HA and it does not configure PITR. See [DEPLOYMENT.md](DEPLOYMENT.md).

## Release artifacts

Tag `v*` → GitHub Release binaries (incl. `nodra-sim`, `nodra-relay-bridge`) +
GHCR multi-arch image with SBOM/provenance/cosign. Pin digests in production.
See [SUPPLY_CHAIN.md](SUPPLY_CHAIN.md).
