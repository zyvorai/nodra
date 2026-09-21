# Nodra ops qualification checklist

Sign after completing each drill on the exact build you intend to run in
production. Software rows from `make qualify` do **not** close these.

| Date | Operator | Build / image digest | Host |
|---|---|---|---|
| 2026-09-14 | lab-ops-drill | nodra-server (lab binary) | `80.79.5.173` |

Evidence: `evidence/qualification/lab/20260914T155128Z/`, `…/20260914T162245Z/`

| Test | Pass? | Notes |
|---|---|---|
| Persistent volume survives restart; `/readyz` → ready | pass | HTTPS `/readyz` ready |
| Backup with `scripts/backup-state.sh` | pass | archive sha256 `5966e5a1…` |
| Restore with `scripts/restore-state.sh`; writer serves prior sites/routes | pass | restored WAL+snapshot |
| TLS (Ingress or direct) verified with real clients | pass | `NODRA_TLS_CERT`/`KEY` under `/etc/nodra/tls`; HTTPS `:18447` |
| Demo tokens / `--public-read` absent on shared networks | pass | no `--public-read` |
| Spool policy chosen and documented (`reject` / drop-*) | pass | lab edge uses default durable spool; production sites should set `reject` when silent drop is unacceptable (documented in PRODUCTION.md) |
| WAN-loss drill: edge spools, CP catches up | pass | abbreviated — `lab/20260914T162245Z/nodra-soak/wan-loss.log` (`nodrad` stayed up; CP ready in 2s) |
| Disk-pressure drill: gauges + policy behavior | pass | abbreviated — metrics + `df` snapshot in `nodra-soak/disk-pressure.log` (not multi-hour soak) |
| Single-replica only against the data volume | pass | one `nodra-server` |
| Metrics scrape path restricted | blocked | still lab-open; restrict via firewall/NetworkPolicy in customer prod |

**Production claim:** I have not claimed HA or multi-writer delivery for this
deployment. Backup/restore, TLS, and abbreviated WAN/disk drills are signed for this lab host.

Signature: lab-ops-drill  Date: 2026-09-14

The signed claim above is the 2026-09-14 lab drill. Current readiness language is in `docs/PRODUCTION.md` and `docs/QUALIFICATION.md`.
