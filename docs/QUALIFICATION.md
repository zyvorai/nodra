---
hero:
  eyebrow: QUALIFICATION
  title: Production qualification matrix — Nodra
---

Software rows are automated by `make qualify`. Multi-hour WAN-loss / disk-pressure
soaks, HA claims, and signed backup/restore drills remain operator-recorded in
[`evidence/qualification/ops-checklist.md`](../evidence/qualification/ops-checklist.md).

## Software (host) rows — `make qualify`

| ID | Expected |
|---|---|
| `version_lockstep` | `VERSION` matches Makefile, Dockerfile, Chart `appVersion`, Helm tag, OpenAPI, `internal/version` |
| `gofmt` / `go_vet` / `unit_race` | Format, vet, race-enabled tests |
| `readyz_store_ping` | `/readyz` fails closed when the fleet store cannot be pinged |
| `openapi_yaml_parse` | `docs/openapi.yaml` parses |
| `openapi_route_coverage` | Every `internal/server` mux route appears in OpenAPI |
| `build_binaries` | `nodra-server`, `nodrad`, `nodractl`, `nodra-sim`, `nodra-relay-bridge` |
| `local_smoke` | `./scripts/smoke.sh` |
| `postgres_store_ci` | `go test -run Postgres` with `NODRA_DATABASE_URL` (CI job; skip locally without DSN) |

These prove the single-writer control plane, OpenAPI coverage, and (in CI) the
optional Postgres fleet store. They **do not** prove HA or multi-day soak.

## Operator / lab rows — signed checklist

| Test | Required outcome |
|---|---|
| Persistent volume bootstrap | State survives restart; `/readyz` returns `ready` |
| Backup and restore | `scripts/backup-state.sh` / `restore-state.sh`; restored writer serves sites/routes |
| TLS termination | Ingress or `--tls-cert`/`--tls-key`; no demo tokens on shared nets |
| Spool policy | Production chooses `reject` when silent drop is unacceptable |
| WAN loss | Edge `nodrad` spools; control plane catches up after reconnect |
| Disk pressure | Queue gauges + configured max; reject/drop policy behaves as documented |
| Single-replica discipline | No second `nodra-server` against the same WAL volume |
| Suite wiring | Device Agent MQTT / Fleet inventory boundaries documented in [INTEGRATIONS.md](INTEGRATIONS.md) |

## Maturity note

v0.2.x does not provide multi-replica delivery/DLQ HA. Production is one
control-plane writer with a tested backup. See [ROADMAP.md](https://github.com/zyvorai/nodra/blob/main/ROADMAP.md).
