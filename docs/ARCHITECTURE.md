# Architecture

## Components

`nodra-server` is the management/control plane. `nodrad` is the edge runtime. `nodractl` is the operator CLI.

```text
                     nodra-server
         fleet · routes · twins · apps · DLQ
                         │
                     HTTPS/mTLS
                         │
        ┌────────────────┼────────────────┐
        │                │                │
      nodrad           nodrad           nodrad
        │                │                │
   MQTT / HTTP      MQTT / HTTP      MQTT / HTTP
        │
  local routes + local apps
        │
  durable edge WAL -> cloud when available
```

## Durability

### Edge spool

The v0.2 queue is a single append-only WAL per queue, not one file per message. Each mutation is appended and `fsync`ed before success. Startup replays the WAL into an in-memory index. Automatic compaction rewrites only live records after an operation threshold.

Queue limits are enforced before a put. `reject` is the default because silent data loss is unacceptable for industrial telemetry.

### Control plane state

Fleet metadata uses `state.wal` plus periodic `state.snapshot.json`. Heartbeats therefore append small WAL records instead of rewriting the complete state document.

Cloud delivery and DLQ also use durable WAL queues.

## Event ACK invariant

For an incoming edge event:

1. authenticate the site;
2. match cloud routes;
3. persist each deterministic delivery record;
4. persist the event record;
5. return HTTP 202.

A queue failure returns 503/507. `nodrad` keeps its edge event and retries. Delivery IDs are deterministic from `(event_id, route_id)`, so retrying an interrupted request does not multiply queued work.

## Time model

`event_time` is when the event was accepted by the edge runtime. `ingested_at` is when the control plane committed it. Offline periods therefore do not destroy the original telemetry timeline.

## Local autonomy

Every accepted event can match local routes before/cloud-independent of synchronization. Local HTTP deliveries have their own WAL and backoff loop. MQTT subscribers receive local fan-out directly from the embedded broker.

## MQTT boundary

The embedded broker is an edge-ingress broker, not a full general-purpose MQTT platform. v0.2 supports MQTT 3.1.1 CONNECT, SUBSCRIBE, QoS 0/1 PUBLISH, PUBACK, PINGREQ and DISCONNECT. Persistent sessions and QoS 2 are deliberately not claimed.

## Device twins

Twins have independent `desired_version` and `reported_version`. The server owns desired state; the edge/device adapter reports observed state. `nodrad` periodically caches twin state to disk so local adapters can read it during WAN loss.

## App reconciliation

With `runner=docker`, each reconciliation loop checks actual container state against the desired deployment. `running` creates/restarts a missing container; `stopped` removes it. The default distribution never mounts the Docker socket automatically.

## Scale boundary

v0.2 supports many edge agents but one control-plane writer. Embedded WAL storage avoids v0.1's rewrite bottleneck, but it is not a distributed database. A future HA mode will use an external transactional store and multiple stateless control-plane replicas.
