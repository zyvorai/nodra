# Upgrade and rollback

This is the operator procedure for moving between Nodra control-plane
releases. It covers the two previous public lines (**v0.2.0** and **v0.2.1**)
up to the current **v0.2.2** (and `main` builds ahead of the tag), including
Postgres schema migrations, config compatibility, and how to roll back.

Edge agents (`nodrad`) are forward-compatible within the v0.2.x API: an older
agent may keep talking to a newer control plane. Prefer upgrading the control
plane first, then the edge.

## Supported upgrade paths

| From | To | Notes |
|---|---|---|
| v0.2.0 | v0.2.2 / current | File store: replace binary, keep data dir. Postgres: apply numbered migrations on first start. |
| v0.2.1 | v0.2.2 / current | Same as above. |
| v0.2.2 | newer `main` | Same; refuse to start if the database schema is *newer* than the binary. |

Skipping a major line outside this table is not covered. Always take a backup
before upgrading.

## Pre-upgrade checklist

1. Confirm the software matrix is green on the build you are installing
   (`make qualify` when building from source).
2. Backup:
   - File mode: `scripts/backup-state.sh` (data directory archive).
   - Postgres: `scripts/backup-postgres.sh` (`pg_dump` custom format) **and**
     keep a recent base backup if you rely on WAL / PITR ([RECOVERY.md](RECOVERY.md)).
3. Note the running version: `GET /api/v1/version` or `nodra-server -version`.
4. Schedule a short maintenance window. File mode is single-replica; Postgres
   multi-replica still benefits from stopping writers briefly around a dump.

## Control-plane upgrade (systemd / bare metal)

1. Stop the writer: `systemctl stop nodra-server`.
2. Install the new `nodra-server` / `nodractl` binaries (for example
   `./scripts/deploy-remote.sh <host> <user>`).
3. Keep `/etc/nodra/nodra.env` and the data directory (or Postgres URL) unchanged
   unless a release note says otherwise.
4. Start: `systemctl start nodra-server`.
5. Verify: `GET /healthz`, `GET /readyz`, `GET /api/v1/version`, then a smoke
   login (`scripts/smoke-remote.sh`).

On first start with `NODRA_STORE=postgres`, the process applies any pending
files under `internal/store/migrations/` (advisory-locked) and records them in
`nodra_schema_migrations`. A database whose max version is **newer** than the
binary refuses to start (downgrade protection).

## Helm / Kubernetes

1. `helm history nodra` (or your release name) and keep the previous revision.
2. Apply the chart for the target appVersion. Production values stay at one
   replica by default (`charts/nodra/values-production.yaml`).
3. Wait for the rollout: `kubectl rollout status deploy/…`.
4. Run the chart test / smoke against the Service.

Rollback: `helm rollback <release> <previous-revision>`. If a Postgres migration
already applied and the older binary does not understand that schema version,
restore the database from the pre-upgrade dump **before** or **with** the
rollback (see below). Do not point an older binary at a newer schema.

## Config migration (v0.2.0 → current)

Environment and agent JSON stay compatible across v0.2.x. Notable additive
settings (all optional; defaults preserve prior behaviour):

| Setting | Since | Default behaviour when unset |
|---|---|---|
| `NODRA_SESSION_TTL` | sessions work | 1h console sessions; static admin/viewer tokens unchanged |
| `NODRA_ENROLLMENT_TOKEN_TTL` | enrollment rotate | enrollment token does not expire |
| `NODRA_OTLP_ENDPOINT` | OTLP metrics | no export |
| `NODRA_STORE=postgres` + `NODRA_DATABASE_URL` | Postgres fleet | file store under the data directory |
| MQTT TLS fields on the agent (`mqtt_cert_file` / `mqtt_key_file`) | broker TLS | cleartext MQTT listener |
| OPC-UA `security_policy: Basic256Sha256` + cert paths | channel crypto | SecurityPolicy None |

No compulsory rewrite of `nodrad.json` is required for a plain upgrade. New
features activate only when configured.

## Postgres schema versions

| Version | Migration | Contents |
|---|---|---|
| 1 | `001_baseline.sql` | Fleet documents, deliveries, dead letters, audit |
| 2 | `002_fleet_columns.sql` | `revision` / `site_id` / `org_id` columns + indexes |
| 3 | `003_console_sessions_roles.sql` | Shared `nodra_console_sessions` and `nodra_custom_roles` |

Upgrading from a database that never ran migrations (older binaries that
applied DDL with `CREATE IF NOT EXISTS` at open) still adopts version 1 safely
because baseline statements are idempotent.

## Rollback procedure

### File mode

1. `systemctl stop nodra-server`.
2. Restore the data-directory archive: `scripts/restore-state.sh <archive> /var/lib/nodra`.
3. Reinstall the previous `nodra-server` binary (or `helm rollback`).
4. Start and smoke-test.

### Postgres mode

1. Stop every control-plane replica.
2. Restore the pre-upgrade `pg_dump` with `NODRA_RESTORE_FORCE=1 ./scripts/restore-postgres.sh …`
   (or restore a base backup + WAL to a time before the upgrade — [RECOVERY.md](RECOVERY.md)).
3. Install the previous binary / `helm rollback` so the binary’s max migration
   version matches the restored schema.
4. Start replicas and smoke-test.

Rolling back the binary **without** restoring the database fails closed when
migration 3 (or any newer version) is already recorded: the older binary
exits on schema-too-new. That is intentional.

## Edge agent (`nodrad`)

1. Upgrade control plane and confirm `/readyz`.
2. Replace the `nodrad` binary; keep `nodrad.json` (site token, spool path).
3. `systemctl restart nodrad`.
4. Confirm `GET http://127.0.0.1:9091/healthz` and that spool depth drains.

ZTP (`nodrad ztp`) is only needed for new sites, not for upgrades.

## Verification after upgrade or rollback

- `GET /api/v1/version` matches the expected build.
- Console login (or static admin token) can read `/api/v1/overview`.
- One publish through the edge agent is accepted and delivered (or visible in
  the spool while the control plane is intentionally stopped).
- For Postgres: `SELECT max(version) FROM nodra_schema_migrations` equals the
  migration set shipped with that binary.

## Honesty limits

- This document is the upgrade/rollback **procedure**. It is not a measured
  RPO/RTO and not a multi-day soak result.
- Downgrading across a schema version always requires a database restore; there
  is no automatic down-migration SQL.
- Config keys removed in a future major release will be listed in `CHANGELOG.md`
  with a migration note; none are removed in the v0.2.0→current path.
