---
hero:
  eyebrow: PRODUCTION
  title: Production operations runbook — Nodra
---

Companion to [OPERATIONS.md](OPERATIONS.md) and [SECURITY-MODEL.md](SECURITY-MODEL.md).
For a multi-product evaluation host (Fleet + OTA + Device Agent + Nodra), see
sibling repos' `docs/LAB.md` — that path is not a multi-replica production
control plane.

## Current maturity (2026-09-15)

| Claim | Status |
|---|---|
| Software matrix + CI lab substitutes | green (`make qualify`, bridge/compose/backup CI) |
| Ops checklist (backup/restore/single-replica) | **signed** — [ops-checklist.md](https://github.com/zyvorai/nodra/blob/main/evidence/qualification/ops-checklist.md) |
| TLS on lab CP | **done** — `NODRA_TLS_CERT`/`KEY` HTTPS on `:18447` |
| Abbreviated WAN + disk drills | **signed** — `lab/20260914T162245Z/nodra-soak/` |
| HA / multi-writer | **partial** — Postgres fleet state is read from the database with revision checks, and delivery workers claim concurrently. Full HA (multi-day soak, PITR, remaining 1.0 gates) is **open**. File mode stays one replica |
| Multi-hour WAN / disk soak | **pass** — CI-automated, `.github/workflows/soak.yml` + `.github/workflows/ci.yml`'s `soak-short` job (`scripts/ci/soak.sh`, judged by `scripts/ci/soak-check.py`) |
| Multi-day WAN / disk soak | **open** — needs a self-hosted runner against the lab host; hosted GitHub runners cap out around 6h |

**Verdict:** file mode is **production-capable** as a tested single-replica deployment when TLS + spool policy are set
per this runbook and the ops checklist is signed on the target host. PostgreSQL shares fleet state and lets delivery workers claim concurrently; that is not a full HA claim. Lab
reference host already runs HTTPS `:18447`.

## Preconditions

1. Software matrix green: `make qualify` → `evidence/qualification/software-matrix.json`.
2. Ops checklist signed: `evidence/qualification/ops-checklist.md`.
3. One control-plane replica for file mode. Helm refuses `replicaCount` above 1 in that mode. Postgres may run more than one replica; the default remains 1 until full HA is qualified.
4. Strong `NODRA_ADMIN_TOKEN` / passwords; rotate `NODRA_ENROLLMENT_TOKEN` after bootstrap.
5. TLS at Ingress or direct `--tls-cert`/`--tls-key`. Never expose `--public-read` or demo tokens on shared networks.
6. Choose spool policy deliberately (`reject` for loss-sensitive telemetry).

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

- File mode has no multi-replica delivery plane — failover is restore-from-backup. Postgres delivery workers claim concurrently, and fleet updates use `revision`; soak, PITR, and the rest of the 1.0 gates are still open.
- The console's live Activity/Logs tail is in-memory (cap 2000) and is **not**
  in backups — but every actor-attributable action it shows is durably
  written to `audit/` (or Postgres) under the data directory, **is** in
  backups, and is queryable/exportable via `GET /api/v1/audit` and
  `nodractl audit export`.
- MQTT QoS 0/1/2 works in both directions. Persistent sessions replay queued QoS 1 and QoS 2. Subscription lists are in-memory and do not survive a broker restart.
- Compose / Helm demo values are evaluation-only.

## Release artifacts

Tag `v*` → GitHub Release binaries (incl. `nodra-sim`, `nodra-relay-bridge`) +
GHCR multi-arch image with SBOM/provenance/cosign. Pin digests in production.
See [SUPPLY_CHAIN.md](SUPPLY_CHAIN.md).
