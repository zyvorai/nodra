# Nodra ops qualification checklist

Sign after completing each drill on the exact build you intend to run in
production. Software rows from `make qualify` do **not** close these.

| Date | Operator | Build / image digest | Host |
|---|---|---|---|
| 2026-09-14 | lab-ops-drill | nodra-server `@b45423e` (lab binary) | `80.79.5.173` |

Evidence: `evidence/qualification/lab/20260914T155128Z/`

| Test | Pass? | Notes |
|---|---|---|
| Persistent volume survives restart; `/readyz` → ready | pass | live `{"status":"ready"}` on `:18447` |
| Backup with `scripts/backup-state.sh` | pass | archive sha256 `5966e5a1…` |
| Restore with `scripts/restore-state.sh`; writer serves prior sites/routes | pass | restored tree has `state.wal` + snapshot; live writer undisturbed |
| TLS (Ingress or direct) verified with real clients | blocked | lab CP is HTTP `:18447` evaluation — production must set `NODRA_TLS_CERT`/`NODRA_TLS_KEY` or Ingress TLS |
| Demo tokens / `--public-read` absent on shared networks | pass | no `--public-read`; admin token via env (not demo public-read) |
| Spool policy chosen and documented (`reject` / drop-*) | blocked | document per-site before GA edge telemetry |
| WAN-loss drill: edge spools, CP catches up | blocked | soak — not run this pass |
| Disk-pressure drill: gauges + policy behavior | blocked | soak — not run this pass |
| Single-replica only against the data volume | pass | one `nodra-server` vs `/var/lib/nodra` |
| Metrics scrape path restricted | blocked | `/metrics` still network-reachable on lab; restrict via firewall/NetworkPolicy in prod |

**Production claim:** I have not claimed HA or multi-writer delivery for this
deployment. Backup/restore + single-writer discipline are signed for this lab host.

Signature: lab-ops-drill  Date: 2026-09-14
