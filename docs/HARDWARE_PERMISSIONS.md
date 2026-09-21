---
hero:
  eyebrow: HARDWARE
  title: Hardware and OS permissions
---

Nodra’s control plane is software-only. Edge permissions matter when `nodrad`
talks to serial, Docker, or MQTT on a shared host. This matrix is for lab vs
production operators — it is not a board qualification checklist.

## Control plane (`nodra-server`)

| Resource | Lab | Production |
|---|---|---|
| Data directory | local `./data` | dedicated PVC / `/var/lib/nodra`, mode `0750`, owner service user |
| Listen port | high port / localhost | firewalled; TLS at Ingress or direct certs |
| `/metrics` | open on loopback | scrape-only network path |
| Postgres (optional) | optional DSN | TLS DSN (`sslmode=require`), separate credentials |

## Edge runtime (`nodrad`)

| Resource | Why | Notes |
|---|---|---|
| MQTT listen | Device Agent / sensors | Prefer loopback or site LAN. Set `mqtt_cert_file` / `mqtt_key_file` (or Helm `agent.mqtt.tls.existingSecret`) so the broker terminates TLS 1.2+. Optional `mqtt_require_client_cert` with `mqtt_client_ca_file` |
| HTTP publish | local ingress | Bearer `local_token` required unless `allow_unauthenticated_local` |
| Docker socket | app reconciliation | Mount only where intended; prefer digest-pinned images; cosign verify is opt-in via `signature_mode` |
| Serial / RS485 | Modbus RTU | Linux only; needs device node access (`dialout` or udev). Framing tests use `socat` PTYs — not board baud fidelity |
| Outbound HTTPS | cloud routes / Relay bridge | System CA or custom trust |

## Device Agent adjacency

CAN/GPIO/I2C permissions live in **zyvor-device-agent** packaging and udev rules.
Nodra’s J1939 connector consumes Device Agent SSE — it does not open SocketCAN
itself. See Device Agent `docs/HARDWARE_PERMISSIONS.md` when present.

## Integration tests without hardware

| Harness | Covers |
|---|---|
| `socat` PTY + Modbus RTU integration test | Framing/CRC (skips if `socat` missing) |
| J1939 connector unit tests | Mock SSE streams |
| `./scripts/smoke.sh` | CP + agent path without industrial buses |

Physical CAN, RS485 direction control, and baud accuracy remain operator-signed
hardware rows — see [QUALIFICATION.md](QUALIFICATION.md).
