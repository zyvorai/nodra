# Zyvor OTA integration

Nodra integrates with, but does not replace, the Zyvor OTA agent.

## Responsibility boundary

- **Fleet** owns targeting, maintenance windows, canaries, batch sizes, pause/resume and fleet-wide failure policy.
- **Nodra** owns durable command delivery, edge connectivity, desired/reported state and forwarding OTA progress while sites are intermittently connected.
- **Zyvor OTA Agent** owns download, artifact verification, device compatibility checks, staging, activation, reboot, boot verification, commit and rollback.

This separation prevents hardware- and bootloader-specific update code from becoming part of Nodra's core protocol/runtime path.

## Wire protocol

OTA requests and status are carried through the same generic desired/reported Device Twin mechanism Nodra already has (`GET/PUT /api/v1/twins`), under a reserved `"ota"` key — not a separate delivery path. That gets OTA the same durable storage, nodrad polling delivery, and admin/agent auth every other twin already has, for free:

- `POST /api/v1/devices/{id}/ota` (admin) — set a device's desired OTA state. Body is a `pkg/ota.Request`; rejected with `400` if `Request.Validate()` fails (missing `update_id`, an invalid `Manifest`, etc.). Stored as `Twin.Desired["ota"]`.
- `GET /api/v1/devices/{id}/ota` (admin) — convenience read of the device's current OTA `request`/`status`, extracted from its twin (everything here is also visible via the generic `GET /api/v1/twins`).
- `POST /api/v1/agent/devices/{id}/ota/status` (agent, site-authenticated) — report OTA status. Body is `{"site_id", "status": pkg/ota.Status}`. Rejected with `400` if `Status.Validate()` fails, or `409` if the transition from the previously-reported state is illegal per `pkg/ota.ValidateTransition` (idempotent replay of the *same* state is always allowed). Stored as `Twin.Reported["ota"]`; a terminal `failed` or `rolled-back` status raises an `ota_failed`/`ota_rolled-back` alert.
- `POST /v1/devices/{id}/ota/status` (nodrad-local, for the OTA agent running on the same host) — validates transport-level fields and forwards to the control-plane endpoint above.

nodrad itself never interprets an OTA status beyond validating and forwarding it — staging, activation, health verification and rollback stay the OTA agent's job.

## Artifact contract

`pkg/ota.Manifest` supports these artifact classes:

- firmware
- BSP
- Linux kernel
- device tree
- bootloader
- root filesystem
- containers
- configuration
- MCU firmware
- AI models

Every artifact must include a SHA-256 digest and a signature reference. SBOM and bundle-format metadata are represented in the manifest. RAUC or SWUpdate bundles are preferred for system-image updates when supported by the target platform.

`policy.health_timeout` is a Go duration string on the wire (for example `"5m"` or `"90s"`), not a nanosecond integer. An empty value means no timeout was specified.

## A/B lifecycle

The Nodra-facing lifecycle is:

```text
pending
  -> downloading
  -> downloaded
  -> verifying
  -> staged
  -> activating
  -> rebooting
  -> health-check
       |-> committed
       `-> rollback-pending -> rolled-back
```

Failure and cancellation paths are explicit. Repeating the same state is allowed so status delivery remains idempotent across process restarts and WAN reconnects.

## Offline behavior

An OTA request is identified by `update_id` and must be processed idempotently. Once a device has accepted a request, temporary cloud loss must not require restarting an already staged update. Status changes should be persisted by the caller before acknowledgement so they can be replayed when connectivity returns.

## Security

Before activation the OTA agent must:

1. authenticate the update source;
2. verify the artifact signature;
3. verify the SHA-256 digest;
4. validate board/architecture compatibility;
5. enforce any anti-rollback policy;
6. verify sufficient storage and, for A/B updates, an inactive target slot.

Nodra's `pkg/ota` package validates transport-level fields only. Cryptographic verification remains in the OTA agent because it owns artifact retrieval and the platform trust store.
