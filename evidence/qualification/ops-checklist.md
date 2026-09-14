# Nodra ops qualification checklist

Sign after completing each drill on the exact build you intend to run in
production. Software rows from `make qualify` do **not** close these.

| Date | Operator | Build / image digest | Host |
|---|---|---|---|
| | | | |

| Test | Pass? | Notes |
|---|---|---|
| Persistent volume survives restart; `/readyz` → ready | | |
| Backup with `scripts/backup-state.sh` | | |
| Restore with `scripts/restore-state.sh`; writer serves prior sites/routes | | |
| TLS (Ingress or direct) verified with real clients | | |
| Demo tokens / `--public-read` absent on shared networks | | |
| Spool policy chosen and documented (`reject` / drop-*) | | |
| WAN-loss drill: edge spools, CP catches up | | |
| Disk-pressure drill: gauges + policy behavior | | |
| Single-replica only against the data volume | | |
| Metrics scrape path restricted | | |

**Production claim:** I have not claimed HA or multi-writer delivery for this
deployment.

Signature: ______________________  Date: __________
