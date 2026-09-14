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
    dependency-free philosophy as the Modbus TCP building block. v1 is
    deliberately narrow:

    - **SecurityPolicy `None` only** — no channel encryption or signing.
    - **Anonymous session only** — no username/password or certificate-based
      user tokens.
    - **Read-only** — no Write service.
    - **Polling only** — no Subscribe/MonitoredItems (event-driven push).
    - **No endpoint discovery** — no `GetEndpoints`/`FindServers`/`Browse`;
      the configured `endpoint` URL is dialed directly.
    - Values must decode as a scalar Boolean/Int16/UInt16/Int32/UInt32/
      Int64/UInt64/Float/Double/String/DateTime; an array or unsupported
      Variant type fails that poll cycle.

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

    Each poll opens a fresh connection (Hello/Acknowledge, OpenSecureChannel,
    CreateSession, ActivateSession, Read, Close) rather than holding a
    session open across polls — the same per-call-dial pattern the Modbus
    TCP client uses. Security policies beyond `None`, the Write service,
    Subscribe/MonitoredItems, and endpoint discovery/Browse are tracked as
    v2 follow-ups in `ROADMAP.md`.
