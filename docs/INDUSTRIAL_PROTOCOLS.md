---
hero:
  eyebrow: INDUSTRIAL PROTOCOLS
  title: Industrial Protocols
  lead: >-
    The v0.2.1 increment adds two new industrial transports — Modbus RTU
    and J1939 — while keeping protocol semantics in Nodra and hardware
    discovery/capture in Zyvor Device Agent.
  highlights:
    - {value: "2", label: "New industrial transports added in v0.2.1 — Modbus RTU and J1939"}
    - {value: "0x03", label: "Modbus function code the RTU transport implements for polling reads"}
    - {value: "29-bit", label: "Extended CAN frame format decoded by the J1939 connector"}
    - {value: "CRC16", label: "Checked on every Modbus RTU response for transport integrity"}
---

This increment keeps protocol semantics in Nodra while Zyvor Device Agent owns only physical hardware discovery/capture.

## Take a closer look

=== "Modbus RTU"

    The existing `modbus` connector now accepts `transport: "tcp"` (default) or `transport: "rtu"`.

    ```json
    {
      "type": "modbus",
      "name": "boiler-plc",
      "config": {
        "transport": "rtu",
        "device": "/dev/ttyS1",
        "baud": 9600,
        "data_bits": 8,
        "parity": "none",
        "stop_bits": 1,
        "unit_id": 1,
        "start": 0,
        "quantity": 8,
        "topic": "factory/boiler/plc/registers",
        "interval": "2s",
        "timeout": "1s"
      }
    }
    ```

    The RTU transport implements function 0x03 reads and shares the existing connector poll/publish path. CRC16 is checked on every response. Serial configuration uses Linux termios and opens the device per transaction for deterministic unplug/replug recovery.

    The kernel/board is expected to own RS485 direction control. Nodra does not silently rewrite RS485 mode or scan unit IDs.

=== "J1939 (Device Agent)"

    Enable read-only CAN capture in Device Agent, then add:

    ```json
    {
      "type": "j1939-device-agent",
      "name": "truck-can",
      "config": {
        "url": "http://127.0.0.1:9188/api/v1/can/frames/stream",
        "topic_prefix": "vehicle/j1939",
        "reconnect": "2s"
      }
    }
    ```

    The connector accepts only extended 29-bit data frames, derives priority/PGN/source/destination, and emits Nodra events such as:

    ```text
    vehicle/j1939/pgn/00F004
    vehicle/j1939/pgn/00FF50
    ```

    Standard CAN, RTR and CAN error frames are ignored. Nodra's normal ingest/WAL/local-route/cloud path remains unchanged.

    **Raw + decoded duplication**: if `j1939-device-agent` consumes the Device Agent SSE stream, set Device Agent
    `industrial.can_capture.publish_to_nodra = false` when only decoded PGN events are desired.
    Keep it enabled only when both raw CAN archival topics and decoded J1939 topics are intentional.

=== "OPC-UA"

    A dependency-free, hand-rolled UA-TCP (binary) client, following the same
    dependency-free philosophy as the Modbus TCP building block. Scope is
    deliberately narrow:

    - **SecurityPolicy `None` by default** — anonymous session, no channel
      encryption or signing. **`Basic256Sha256` scaffolding** is wired:
      set `security_policy` to `Basic256Sha256`, `security_mode` to `Sign`
      or `SignAndEncrypt` (default), and `client_cert_path` /
      `client_key_path` / `server_cert_path`. Without those cert paths the
      connector fails fast with an actionable error. Full secure-channel
      crypto (RSA-OAEP, PKCS#1/PSS signing, HMAC-SHA256 key derivation) is
      not shipped in this build yet — even with readable certs you get a
      clear "channel crypto is not available" error rather than a subtly
      wrong encrypted channel. Tracked in `ROADMAP.md`.
    - **Anonymous session only** — no username/password or certificate-based
      user tokens (independent of channel security policy).
    - **Read and Subscribe/MonitoredItems** — `mode: "poll"` (default) ticks
      Read on `interval`; `mode: "subscribe"` instead opens one long-lived
      Subscribe session and reconnects (waiting `interval` between attempts)
      on any error. This is the least-verified part of the package: unlike
      Read/Write/Browse's single-request-response shape, Subscribe's
      multi-step CreateSubscription/CreateMonitoredItems/Publish protocol has
      more surface for a subtle wire-format mistake to hide — smoke-test
      against a real server (e.g. open62541) before production use.
    - Values must decode as a scalar Boolean/Int16/UInt16/Int32/UInt32/
      Int64/UInt64/Float/Double/String/DateTime; an array or unsupported
      Variant type fails that poll cycle (Read) or is rejected before
      sending (Write).

    ```json
    {
      "type": "opcua",
      "name": "boiler-plc",
      "config": {
        "endpoint": "opc.tcp://boiler-plc.local:4840",
        "node_ids": ["ns=2;i=1001", "ns=2;s=Temperature"],
        "topic": "factory/boiler/opcua",
        "interval": "5s",
        "timeout": "5s"
      }
    }
    ```

    Optional Basic256Sha256 config (fails until channel crypto lands; cert
    paths are required when this policy is selected):

    ```json
    {
      "type": "opcua",
      "name": "boiler-plc-secure",
      "config": {
        "endpoint": "opc.tcp://boiler-plc.local:4840",
        "node_ids": ["ns=2;i=1001"],
        "topic": "factory/boiler/opcua",
        "security_policy": "Basic256Sha256",
        "security_mode": "SignAndEncrypt",
        "client_cert_path": "/etc/nodra/opcua/client.crt",
        "client_key_path": "/etc/nodra/opcua/client.key",
        "server_cert_path": "/etc/nodra/opcua/server.crt"
      }
    }
    ```

    Each poll opens a fresh connection (Hello/Acknowledge, OpenSecureChannel,
    CreateSession, ActivateSession, Read, Close) rather than holding a
    session open across polls — the same per-call-dial pattern the Modbus
    TCP client uses.

    Set `"mode": "subscribe"` instead of polling Read on an interval:

    ```json
    {
      "type": "opcua",
      "name": "boiler-plc-push",
      "config": {
        "endpoint": "opc.tcp://boiler-plc.local:4840",
        "node_ids": ["ns=2;i=1001", "ns=2;s=Temperature"],
        "topic": "factory/boiler/opcua",
        "mode": "subscribe",
        "interval": "1s",
        "timeout": "5s"
      }
    }
    ```

    This holds one connection open (CreateSubscription + CreateMonitoredItems
    for every configured node, then a continuous Publish loop) instead of
    dialing per tick — `interval` becomes the requested publishing interval
    and, separately, the reconnect backoff after an error. Each server-
    pushed value is emitted as its own event, one node per event, unlike
    `mode: "poll"`'s single event batching every configured node.

    Only the Value attribute's data changes are monitored (no Events,
    filters, or non-Value attributes); QueueSize is 1 with DiscardOldest, so
    a burst of rapid changes only ever surfaces the latest value, not a
    backlog.

    **Write, discovery and Browse** are Go `Client`/package-level API calls,
    not poller configuration — there's no `nodrad.json` config surface for
    them, mirroring how Modbus's own `WriteSingleRegister` isn't wired into
    its poller either:

    ```go
    cli := &opcua.Client{Endpoint: "opc.tcp://boiler-plc.local:4840", Timeout: 5 * time.Second}

    // Write a single node's Value attribute.
    status, err := cli.Write(ctx, opcua.NodeID{Namespace: 2, Numeric: 1001}, int32(72))

    // Browse an address-space node's references.
    refs, err := cli.Browse(ctx, opcua.NodeID{Namespace: 0, Numeric: 85}, 0, nil)

    // Discovery services run over an unsecured pre-session channel, so
    // they're package-level functions, not methods on an authenticated Client.
    endpoints, err := opcua.GetEndpoints(ctx, "opc.tcp://boiler-plc.local:4840", 5*time.Second)
    servers, err := opcua.FindServers(ctx, "opc.tcp://boiler-plc.local:4840", 5*time.Second)
    ```

    Basic256Sha256 config scaffolding is in place (`security_policy`,
    `security_mode`, cert paths on the poller and `Client.Security`); full
    asymmetric secure-channel crypto remains a follow-up in `ROADMAP.md` —
    shipping a subtly-wrong "encrypted" channel would be worse than failing
    closed with a clear error when certs or crypto support are missing.

=== "Serial (generic)"

    A protocol-agnostic passthrough connector for devices that don't speak
    Modbus/OPC-UA/J1939 — it opens a serial device and publishes each
    delimited (or idle-gap-framed) chunk of bytes as a Nodra event without
    decoding any application protocol. It shares Linux termios
    configuration with the Modbus RTU transport via `internal/serialport`
    rather than duplicating it, and is Linux-only for the same reason (a
    non-Linux build still compiles and registers the `serial` connector
    type; it only fails, with a clear error, when actually started).

    ```json
    {
      "type": "serial",
      "name": "sensor-line",
      "config": {
        "device": "/dev/ttyUSB0",
        "baud": 9600,
        "data_bits": 8,
        "parity": "none",
        "stop_bits": 1,
        "framing": "delimiter",
        "delimiter": "\n",
        "max_frame": 65536,
        "topic": "factory/sensor/raw",
        "timeout": "2s"
      }
    }
    ```

    `framing` is `"delimiter"` (default, split on the one-byte `delimiter`,
    itself defaulting to `"\n"`) or `"idle"` (flush whatever's been read
    after `idle_timeout` of silence on the line, default `100ms`). `timeout`
    is the reconnect backoff after an I/O error (e.g. device unplugged),
    default `2s` — it is not a per-transaction deadline like Modbus's
    `timeout`, since this connector is a continuous read loop, not a
    request/response client. If no frame boundary shows up within
    `max_frame` bytes, the buffer is dropped (logged, reflected in
    connector health) rather than grown unboundedly — almost always a sign
    of a misconfigured delimiter or baud rate, not a legitimately huge
    frame.

    The kernel/board is expected to own RS485 direction control, same as
    Modbus RTU — Nodra does not silently rewrite RS485 mode.
