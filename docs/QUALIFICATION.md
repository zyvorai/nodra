---
hero:
  eyebrow: QUALIFICATION
  title: Production qualification matrix — Nodra
---

Software rows are automated by `make qualify`. Lab ops for host `80.79.5.173`
are **signed** in
[`evidence/qualification/ops-checklist.md`](https://github.com/zyvorai/nodra/blob/main/evidence/qualification/ops-checklist.md)
(backup/TLS/HTTPS `:18447`/abbreviated WAN+disk). Multi-hour soak and HA remain open.

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

## Operator / lab rows — checklist status

Evidence: `ops-checklist.md`, `lab/20260914T162245Z/nodra-soak/`.

| Test | Lab status |
|---|---|
| Persistent volume bootstrap | **pass** (signed) |
| Backup and restore | **pass** (signed) |
| TLS termination (`NODRA_TLS_*` on `:18447`) | **pass** (signed) |
| Spool policy documented | **pass** (signed) |
| WAN loss (abbreviated) | **pass** — `nodra-soak/wan-loss.log` |
| Disk pressure (abbreviated) | **pass** — `nodra-soak/disk-pressure.log` |
| Single-replica discipline | **pass** (signed) |
| Suite wiring | see [INTEGRATIONS.md](INTEGRATIONS.md) |
| Multi-hour WAN / disk soak | **open** |
| HA / multi-writer | **not available** in v0.2.x |

## Maturity note

v0.2.x does not provide multi-replica delivery/DLQ HA. Production is one
control-plane writer with a tested backup. See [ROADMAP.md](https://github.com/zyvorai/nodra/blob/main/ROADMAP.md)
and [PRODUCTION.md](PRODUCTION.md).

## GitHub CI (lab substitute)

CI runs relay-bridge smoke, compose stack, compose relay-bridge, and backup/restore.
These complement the signed lab ops checklist. CI still does **not** claim HA
or multi-hour soak.
