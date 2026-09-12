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
