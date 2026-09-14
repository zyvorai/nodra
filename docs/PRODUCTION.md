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
| HA / multi-writer | **not available** in v0.2.x |
| Multi-hour WAN / disk soak | **open** |

**Verdict:** single-writer Nodra is **production-ready** when TLS + spool policy are set
per this runbook and the ops checklist is signed on the target host. Lab
reference host already runs HTTPS `:18447`.

## Preconditions

1. Software matrix green: `make qualify` → `evidence/qualification/software-matrix.json`.
2. Ops checklist signed: `evidence/qualification/ops-checklist.md`.
3. One control-plane replica only (embedded WAL / single writer).
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

- No HA / multi-writer delivery plane — failover is restore-from-backup.
- Activity/Logs ring is in-memory (cap 2000) and is **not** in backups.
- MQTT is a documented subset (no QoS 2 / persistent sessions).
- Compose / Helm demo values are evaluation-only.

## Release artifacts

Tag `v*` → GitHub Release binaries (incl. `nodra-sim`, `nodra-relay-bridge`) +
GHCR multi-arch image with SBOM/provenance/cosign. Pin digests in production.
See [SUPPLY_CHAIN.md](SUPPLY_CHAIN.md).
