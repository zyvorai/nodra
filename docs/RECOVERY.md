# Backup and restore

Two backups exist. Neither is point-in-time recovery.

## File store

`scripts/backup-state.sh` archives the data directory. `scripts/restore-state.sh` unpacks it into a stopped writer's directory. The recovery point is the last archive you kept. Activity logs are not in the archive.

## PostgreSQL

`scripts/backup-postgres.sh` runs `pg_dump` in custom format and writes a sha256 file. `scripts/restore-postgres.sh` runs `pg_restore --clean` and refuses to start unless `NODRA_RESTORE_FORCE=1`. Stop `nodra-server` first.

The Helm CronJob (`backup.enabled`) is off by default. Turning it on requires `backup.host`, `backup.existingClaim`, and a Secret with the database user and password. The dump file is a logical backup on that claim. It is not WAL archiving.

Point-in-time recovery is a property of the PostgreSQL service (continuous WAL archiving and a base backup). Nodra does not ship that, and this repository does not state an RPO or RTO. Those numbers are whatever backup schedule you actually run and the time it takes to restore it.

The control-plane CA under the data directory (file mode) or in Postgres is inside these backups. Treat the archive as secret material. Do not commit it.
