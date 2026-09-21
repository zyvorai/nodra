---
hero:
  eyebrow: QUALIFICATION
  title: Production qualification matrix — Nodra
---

Software rows are automated by `make qualify`. Lab ops for host `80.79.5.173`
are **signed** in
[`evidence/qualification/ops-checklist.md`](https://github.com/zyvorai/nodra/blob/main/evidence/qualification/ops-checklist.md)
(backup/TLS/HTTPS `:18447`/abbreviated WAN+disk). Multi-hour WAN/disk soak is
now CI-automated. The soak publishes through the edge over HTTP and MQTT for the
whole run, including WAN loss, and a skipped data-loss check is a failure.
Multi-day soak (24h, 72h, seven days) and full HA remain open; those durations
need a self-hosted runner. Postgres fleet consistency across replicas is covered
by `TestPostgresCrossReplicaConsistency`. See [SCALE.md](SCALE.md).

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

These prove the file-mode control plane, OpenAPI coverage, and (in CI) Postgres
fleet reads, revision checks, and cross-replica consistency. They **do not**
prove full HA or multi-day soak.

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
| Single-replica discipline (file mode) | **pass** (signed) — `NODRA_STORE=file` remains single-process by construction |
| Concurrent delivery claiming and fleet revisions (postgres mode) | **pass** — `internal/queue.PostgresQueue` claims deliveries concurrently; `store.PostgresStore` reads SQL and updates with `revision` (`TestPostgresCrossReplicaConsistency`) |
| Suite wiring | see [INTEGRATIONS.md](INTEGRATIONS.md) |
| Multi-hour WAN / disk soak | **pass** — CI-automated (see below), no longer operator-only |
| Multi-day WAN / disk soak | **open** — needs a self-hosted runner on the lab host |
| Full HA | **open** — concurrent delivery claiming and revision-checked fleet state are not multi-day soak, PITR, or a published scale envelope |

## Maturity note

File mode is a tested single-replica deployment. PostgreSQL fleet state is read
from the database, with revision checks so replicas cannot silently overwrite
each other, and delivery workers already claim concurrently. A cross-replica
test covers that consistency. Multi-day soak, PITR, and the rest of the 1.0
gates are still open. Helm defaults to one replica and refuses to scale file
mode. See [ROADMAP.md](https://github.com/zyvorai/nodra/blob/main/ROADMAP.md)
and [PRODUCTION.md](PRODUCTION.md).

## GitHub CI (lab substitute)

CI runs relay-bridge smoke, compose stack, compose relay-bridge, backup/restore,
and a WAN-loss + disk-pressure soak (`scripts/ci/soak.sh`, judged by
`scripts/ci/soak-check.py`): a ~10-minute `soak-short` job on every PR
(`.github/workflows/ci.yml`) and a scheduled multi-hour `soak-long` job
(`.github/workflows/soak.yml`, nightly, ~4h against a hosted runner). These
complement the signed lab ops checklist. CI still does **not** claim full HA or a
true multi-day soak — that tier stays tracked as open until it runs on a
self-hosted runner against the lab host, since GitHub-hosted runners cap job
time at roughly 6 hours.
