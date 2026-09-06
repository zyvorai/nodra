# Operations

## Backup

Stop the single control-plane writer or take a storage snapshot. Back up the Nodra data directory, including `state.snapshot.json`, `state.wal`, `deliveries/`, `deadletters/` and optional `pki/`.

## Recovery

Restore the complete data directory and start the same or newer compatible Nodra version. WAL replay reconstructs live state and pending queues.

## Disk pressure

Monitor `nodra_delivery_queue_bytes`, `nodra_pending_deliveries` and `nodra_dead_letters`. On agents, heartbeat metrics expose `queue_bytes`, `queue_depth` and configured queue maximum.

Use `spool_policy=reject` for telemetry that must never be silently discarded.

## Upgrades

v0.2 is single-writer. Scale the control-plane Deployment to zero, update the image, then return to one replica, or use the Helm `Recreate` strategy already provided. Edge agents continue local operation and spool cloud-bound events during the interruption.

## Failure domains

A slow webhook does not block unrelated routes because delivery is concurrent and bounded. Repeatedly failing deliveries enter the DLQ after their configured retry count.
