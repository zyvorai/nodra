# Backup and restore

Two Nodra-managed backups exist. Point-in-time recovery is an operator
PostgreSQL concern described below. This repository does not publish a
measured RPO or RTO.

## File store

`scripts/backup-state.sh` archives the data directory. `scripts/restore-state.sh` unpacks it into a stopped writer's directory. The recovery point is the last archive you kept. Activity logs are not in the archive.

## PostgreSQL logical dump

`scripts/backup-postgres.sh` runs `pg_dump` in custom format and writes a sha256 file. `scripts/restore-postgres.sh` runs `pg_restore --clean` and refuses to start unless `NODRA_RESTORE_FORCE=1`. Stop `nodra-server` first.

The Helm CronJob (`backup.enabled`) is off by default. Turning it on requires `backup.host`, `backup.existingClaim`, and a Secret with the database user and password. The dump file is a logical backup on that claim. It is not WAL archiving.

## PostgreSQL point-in-time recovery (operator)

Nodra does not archive WAL itself. When `NODRA_STORE=postgres`, enable continuous
archiving on the database you run:

1. Set `wal_level=replica` (or higher) and `archive_mode=on`.
2. Set `archive_command` to copy completed WAL segments to durable object storage
   (for example `aws s3 cp %p s3://…/%f` or `cp %p /wal-archive/%f`).
3. Take a base backup (`pg_basebackup` or your provider’s snapshot API) on the
   same schedule you need for restore.
4. Restore by restoring the base backup, placing archived WAL next to it, and
   creating `recovery.signal` with a `recovery_target_time` (or LSN) when you
   need a point before the latest segment.

RPO is the age of the newest WAL segment that reached durable storage when the
failure happened. RTO is the wall time to restore the base backup and replay
WAL on your hardware. Measure both on your cluster before quoting them to
anyone. Do not invent numbers from this document.

Managed Postgres (Cloud SQL, RDS, Azure, etc.) usually turns this into a
provider toggle (“PITR” / “continuous backups”). Prefer that when you do not
operate PostgreSQL yourself.

## Secrets in backups

The control-plane CA under the data directory (file mode) or in Postgres is inside these backups. Treat the archive as secret material. Do not commit it.
