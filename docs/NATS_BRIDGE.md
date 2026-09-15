---
hero:
  eyebrow: MESSAGING BRIDGES
  title: NATS bridge
  lead: >-
    A dependency-free, hand-rolled NATS core client that subscribes to
    configured subjects and publishes each message into Nodra's ingest
    pipeline. Zenoh is explicitly not implemented — see below.
  highlights:
    - {value: "0", label: "New Go module dependencies — the client is hand-rolled, same as Modbus/OPC-UA"}
    - {value: "1", label: "Messaging bridge shipped in this pass — NATS; Zenoh is deferred"}
---

NATS's core wire protocol (`INFO`/`CONNECT`/`SUB`/`MSG`/`PING`/`PONG`/`-ERR`) is a
simple text-prefixed line protocol, comparable in complexity to Nodra's own
hand-rolled MQTT edge-ingress broker (`internal/mqtt`). That's why it's
hand-rolled here in `connectors/nats` rather than pulling in a client
library — `go.mod` has exactly one runtime dependency (`pgx`, for the
optional Postgres store), and this keeps it that way.

## Configuration

```json
{
  "type": "nats",
  "name": "plant-bus",
  "config": {
    "address": "nats://127.0.0.1:4222",
    "subjects": ["factory.line1.temp", "factory.line1.status"],
    "topic_prefix": "vendor/nats",
    "reconnect": "2s"
  }
}
```

- `address` — required. A `host:port` NATS server address; the `nats://`
  scheme prefix is accepted and stripped.
- `subjects` — required, at least one. Subscribed on connect.
- `topic_prefix` — optional. Received messages are published to
  `<topic_prefix>/<subject>` (or the bare `<subject>` when unset).
- `user` / `pass` / `token` — optional NATS auth credentials, sent in the
  `CONNECT` handshake.
- `reconnect` — optional, default `2s`. Backoff between reconnect attempts
  after a connection drops.

Each received message is published as one Nodra event carrying
`x-nodra-ingress: nats`, `x-nodra-connector: <name>`, and
`x-nats-subject: <subject>` headers, following the same connector
registration (`pkg/connector`) and header convention as `connectors/modbus`,
`connectors/j1939`, and `connectors/opcua`.

## v1 scope

This is a **subscribing client only** — it connects outbound to an existing
NATS server; Nodra does not run a NATS server itself. Not supported in v1:

- **TLS** — the client dials plain TCP only.
- **Queue groups, JetStream, request/reply** — subscribe-and-forward only.
- **Clustering** — no failover across multiple NATS server addresses.

## Why not Zenoh?

Zenoh's wire format is a complex custom binary protocol with no simple text
framing to hand-roll — unlike NATS, MQTT, or OPC-UA's binary framing, none
of which needed anywhere near this much protocol-state machinery to
implement safely. The only realistic paths to a Zenoh bridge are cgo-binding
`zenoh-c` or porting a large fraction of `zenoh-go` to pure Go, and both
would introduce exactly the kind of heavyweight, hard-to-audit dependency
this project's connectors have deliberately avoided so far (see `go.mod`).
Zenoh support is deferred indefinitely rather than shipped as a
half-measure; revisit only if a pure-Go implementation matures or the
project's dependency posture changes.
