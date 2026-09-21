# Nodra

**The open edge runtime that keeps sites running when the cloud doesn't.**

Nodra is an Apache-2.0 edge runtime and control plane from Zyvor. It gives remote sites a local MQTT/HTTP ingress, durable store-and-forward, local routes, device twins, edge application reconciliation, fleet health, replayable dead letters and a clean web control plane.

> **v0.2.2** is a serious single-control-plane release. Edge sites are offline-first. File mode is a tested single-replica deployment. PostgreSQL fleet state is read from the database, with revision checks so replicas cannot silently overwrite each other, and delivery workers already claim concurrently. A cross-replica test covers that consistency. Helm defaults to one replica and refuses to scale file mode. Configured limits and a lab ingress observation (~3.7–6.9 accepts/s) are in [`docs/SCALE.md`](docs/SCALE.md). A four-hour lab soak passed 2026-09-21; a 24h lab soak is in progress (not signed until judged). 72h/7d, Nodra-built-in PITR, and the rest of the 1.0 gates remain open. Upgrade/rollback for v0.2.0/v0.2.1 → current: [`docs/UPGRADE.md`](docs/UPGRADE.md). The lab reference host runs HTTPS (`:18447`) with signed ops drills.

## Why Nodra

Edge systems fail differently from datacenters. WAN links disappear, devices use several protocols, remote machines are hard to touch, and cloud-only automation becomes useless exactly when a site needs it most.

Nodra keeps the local path alive:

```text
PLC / sensor / app
        │
   MQTT or HTTP
        │
        ▼
      nodrad
        │
   ┌────┼─────────────┐
   │    │             │
 local  │         durable WAL
 route  │             │
   │    │             └─────► Nodra control plane when WAN returns
   ▼    ▼
 MES   local AI/app
```

## Is this for you?

Nodra is a small, open-source (Apache-2.0), offline-first edge runtime: local
MQTT/HTTP ingress, a durable store-and-forward WAL, local routes, device
twins, and edge app reconciliation, running on a single site's own hardware.
It is not a no-code automation platform, not a managed cloud IoT service,
and not a full HA platform today (file mode is one replica; Postgres sharing is described above).

| | **Nodra** | Node-RED | EMQX/HiveMQ Edge | AWS IoT Greengrass | Azure IoT Edge |
|---|---|---|---|---|---|
| Primary scope | Edge ingress + durable store-and-forward + device twins + app reconciliation | Visual flow-based automation | MQTT broker (edge-deployed) | Cloud-connected edge runtime | Cloud-connected edge runtime |
| Cloud dependency | None required — WAN-loss is a first-class operating mode, not a degraded one | None required | Usually paired with a cloud broker/console | AWS IoT Core | Azure IoT Hub |
| License | Apache-2.0 | Apache-2.0 | Apache-2.0 core (EMQX) / proprietary (HiveMQ Edge) | Proprietary (free tier) | Proprietary (free tier) |
| Industrial protocol decoding | Modbus TCP + RTU, J1939 via Device Agent capture, OPC-UA (SecurityPolicy None or Basic256Sha256, anonymous user token), Linux serial, and a NATS subscribe bridge. Zenoh and Kafka are not shipped (`docs/INDUSTRIAL_PROTOCOLS.md`, `docs/NATS_BRIDGE.md`) | Via community nodes | Not built-in | Via custom components | Via custom modules |
| HA / clustering | File mode is one replica. Postgres shares fleet state with revision checks and claims deliveries concurrently; full HA is still open (`ROADMAP.md`) | N/A (single instance) | Yes (broker clustering) | Managed by AWS | Managed by Azure |

*(General characterizations as of writing — verify current features against
each project's own docs.)*

**Maturity, stated honestly**: current release is v0.2.2. The project's own
`ROADMAP.md` lists what's still required before v1.0 — stable API
compatibility, a full HA control plane, completed multi-day soak tests
(four-hour lab passed; 24h in progress; 72h/7d unrun), protocol conformance
suites, and recovery runbooks. Upgrade/rollback for v0.2.0/v0.2.1 → current
is in `docs/UPGRADE.md`. Configured limits and a lab ingress observation are
in `docs/SCALE.md`. Postgres fleet reads are consistent across replicas and
delivery workers claim concurrently; that is not the HA bar. If you need
full HA or a stability guarantee today, this isn't there yet; if you need a
single-site, offline-resilient edge runtime, this is exactly the scope.

New here? [`docs/FAQ.md`](docs/FAQ.md) covers licensing, support,
production-readiness and protocol-support questions;
[`docs/TROUBLESHOOTING.md`](docs/TROUBLESHOOTING.md) covers real
operational issues with their documented fix.

## v0.2 highlights

- **Real MQTT 3.1.1 edge ingress**: CONNECT, SUBSCRIBE, PUBLISH QoS 0/1/2 (exactly-once in both directions), PUBACK/PUBREC/PUBREL/PUBCOMP, PINGREQ, persistent sessions (queued QoS 1 and QoS 2 are replayed; subscription lists are in-memory and do not survive a broker restart) and local subscriber fan-out.
- **HTTP ingress**: `POST /v1/publish` with optional local bearer protection.
- **Offline-first WAL spool**: append-only, fsynced, replayed after restart and compacted automatically.
- **Explicit backpressure**: cap edge spool by bytes and event count; choose `reject`, `drop-oldest` or `drop-newest` deliberately.
- **Local routes**: match MQTT-style topic filters and deliver to local HTTP services without the cloud.
- **Original event time**: preserve `event_time` separately from control-plane `ingested_at`.
- **Transactional ACK contract**: the server ACKs only after matching deliveries and the event are durably committed.
- **Concurrent durable dispatch**: bounded worker pool, per-route timeout, retry backoff and independent destinations.
- **Dead-letter queue**: failed deliveries retain payload/history and can be inspected, replayed or deleted.
- **Device twins**: desired state in the control plane, reported state from the edge, locally cached by `nodrad`.
- **Site identity**: optional CSR-based certificate enrollment; the private key is generated and kept on the edge.
- **Fleet revocation**: revoke a site identity/token centrally; a revoked site's rotation/heartbeat calls get a distinguishable `403 site_revoked`.
- **Certificate rotation + CRL**: `nodrad` self-rotates its identity certificate ahead of expiry; the control plane serves a real X.509 CRL at `GET /api/v1/ca/crl` and raises a `certificate_expiring` alert.
- **Edge app reconciliation**: Docker desired `running|stopped` state, environment, ports, volumes and command.
- **Modbus TCP client**: dependency-free function 0x03/0x06 building block for adapters.
- **Metrics**: Prometheus counters plus delivery queue/DLQ gauges.
- **Kryton-style console login** + write-capable fleet console (revoke, routes, twins, apps, DLQ). Optional **OIDC "Sign in with SSO"** maps a configured group claim to the existing admin/viewer roles.
- **Live activity Logs** and A–Z `nodra-sim`.
- **Connector SDK + Modbus poller**: registry factories; Modbus TCP poller publishes into nodrad ingest.
- **Viewer/admin RBAC**: optional viewer token; console write actions gated by role.
- **Optional Postgres fleet store**: `NODRA_STORE=postgres` reads sites, routes, twins, and the rest of the fleet from PostgreSQL on every call, with revision checks. Delivery and DLQ workers claim concurrently. File mode stays one replica. This is not a full HA claim — see `docs/ARCHITECTURE.md`.
- **Configurable ports**: `--port` / `NODRA_PORT` / Compose / Helm NodePort / smoke ports share one convention.
- **Apple-inspired Zyvor UX**: embedded, no CDN, no external fonts, orange Zyvor accent.
- **Kubernetes-ready**: raw manifests, Kustomize, Helm, Restricted Pod Security defaults, no service-account token.
- **Supply chain**: CodeQL, race tests, current-Go CI, govulncheck, multi-arch OCI, SBOM, provenance and keyless cosign signing on releases.

## Quick start

### Build

```bash
git clone https://github.com/zyvorai/nodra.git
cd nodra
make build
make help
make ci                       # gofmt, vet, race tests, build
make status                   # needs NODRA_ADMIN_TOKEN and a running server
make deploy-remote H=<host> U=sus
```

The repository has few runtime Go module dependencies (optional Postgres driver via pgx when `NODRA_STORE=postgres`).

### Start the control plane

```bash
export NODRA_ADMIN_TOKEN='change-this-admin-token'
export NODRA_ENROLLMENT_TOKEN='change-this-enrollment-token'
# optional console login (defaults: user admin, password = admin token)
export NODRA_ADMIN_USER='admin'
export NODRA_ADMIN_PASSWORD='change-this-admin-token'

./bin/nodra-server \
  --listen :8080 \
  --data ./data
```

Open `http://127.0.0.1:8080`, walk the login chapters, and **Sign in**. Binaries from `make build`: `nodra-server`, `nodrad`, `nodractl`, `nodra-sim`, `nodra-relay-bridge`.

User demo path (kind/Helm/Compose/sim/console): see [docs/DEMO.md](docs/DEMO.md).

### Configurable ports

| Surface | How |
|---|---|
| Remote systemd | `--port N` / `NODRA_PORT` / `.deploy-last` (else random 18000–28999) |
| Compose | `NODRA_PORT` (UI), `NODRA_AGENT_PORT`, `NODRA_MQTT_PORT` |
| Helm | `service.port`, optional `service.type=NodePort` + `service.nodePort` |
| kind demo PF | `./scripts/demo-k8s.sh --port 18080` |
| Local smoke | `NODRA_SMOKE_SERVER_PORT`, `NODRA_SMOKE_AGENT_PORT` |

```bash
./scripts/deploy-remote.sh 212.8.248.187 sus --port 20059
NODRA_PORT=20059 ./scripts/test-all.sh
make test-all PORT=20059 HOST=212.8.248.187
```

One command builds the image, loads it into kind, installs Helm with control plane + edge agent + live fleet simulation, and port-forwards the console:

```bash
./scripts/demo-k8s.sh
# or: make demo-k8s
```

Sign in: `admin` / `nodra-demo-admin`  
Teardown: `./scripts/demo-k8s.sh --uninstall`

Existing cluster (no kind):

```bash
./scripts/demo-k8s.sh --no-kind
```

Compose (control plane + agent + simulator):

```bash
docker compose up --build
```

### Create an edge config

```bash
./bin/nodrad init \
  --config ./nodrad.json \
  --server http://127.0.0.1:8080 \
  --site factory-west \
  --enrollment-token "$NODRA_ENROLLMENT_TOKEN" \
  --data ./edge-data \
  --listen 127.0.0.1:9091 \
  --mqtt-listen 127.0.0.1:1883 \
  --max-spool-bytes 2147483648 \
  --max-spool-events 1000000 \
  --spool-policy reject

./bin/nodrad --config ./nodrad.json
```

### Publish over HTTP

```bash
./bin/nodractl publish \
  --agent http://127.0.0.1:9091 \
  --topic factory/line-1/temperature \
  --data '{"c":31.2}'
```

### Publish over MQTT

Any MQTT 3.1.1 client can publish QoS 0/1/2. Cleartext is the default:

```bash
mosquitto_pub -h 127.0.0.1 -p 1883 \
  -t factory/line-1/temperature \
  -q 2 -m '{"c":31.2}'
```

To terminate TLS on the broker, set `mqtt_cert_file` and `mqtt_key_file` (or `nodrad init --mqtt-tls-cert` / `--mqtt-tls-key`). Clients then use MQTT over TLS (often port 8883). Optional `mqtt_client_ca_file` and `mqtt_require_client_cert` require a client certificate. Username/password and per-device `mqtt_clients` topic grants are independent of TLS.

Nodra's embedded MQTT broker is deliberately focused on edge ingress/local fan-out. Persistent sessions (`CleanSession=0`) are supported for QoS 0/1/2 subscribers — a durable per-client queue replays missed messages (with `DUP` set) on reconnect, though in-memory subscription lists don't survive a broker restart. QoS 2 works for **both** publishing clients and subscribers (full `PUBLISH`/`PUBREC`/`PUBREL`/`PUBCOMP` exactly-once handshake in each direction).

## Local routes

Local routes live in `nodrad.json` and execute at the edge even if the WAN is down:

```json
{
  "local_routes": [
    {
      "name": "Local MES",
      "topic": "factory/+/telemetry",
      "target_url": "http://127.0.0.1:7070/events",
      "method": "POST",
      "timeout": "3s",
      "filter": {
        "exists": ["c"],
        "min": {"c": 0},
        "max": {"c": 80},
        "equals": {"status": "ok"},
        "in": {"line": ["1", "2"]}
      },
      "transform": {
        "set_headers": {"x-source": "nodra"},
        "drop_fields": ["raw"],
        "set_fields": {"unit": "C"},
        "wrap_as": "reading",
        "topic_rewrite": "mes/{{topic}}"
      }
    }
  ]
}
```

`filter` and `transform` are optional and evaluated per route, deterministically, at the edge — no scripting, just data. A `filter` drops the event locally when any configured predicate fails (`exists`/`equals`/`min`/`max`/`in`); a `transform` can set headers, drop or set JSON fields, wrap the payload under a key, and rewrite the delivered topic (`{{topic}}` substitutes the original topic). Cloud forwarding and local routing are independent. A local destination failure enters the agent's durable local-delivery WAL and retries with backoff.

## Backpressure and disk protection

```json
{
  "max_spool_bytes": 2147483648,
  "max_spool_events": 1000000,
  "spool_policy": "reject"
}
```

Policies:

- `reject` — safest default. New events receive HTTP 507 / MQTT handler failure when the durable spool is full.
- `drop-oldest` — keep recent data at the cost of older data.
- `drop-newest` — preserve queued history and reject the new event.

The agent heartbeat reports queue count and bytes to the control plane.

## Cloud routes and dead letters

```bash
nodractl --server http://127.0.0.1:8080 --token "$NODRA_ADMIN_TOKEN" \
  routes create \
  --name analytics \
  --topic 'factory/+/telemetry' \
  --target https://analytics.example.com/edge
```

If delivery exhausts retries, Nodra moves it to the DLQ rather than deleting it:

```bash
nodractl --token "$NODRA_ADMIN_TOKEN" dlq list
nodractl --token "$NODRA_ADMIN_TOKEN" dlq replay dlv_...
```

## Device twins

Register a device from the edge, then set desired state centrally:

```bash
nodractl --token "$NODRA_ADMIN_TOKEN" twins desired plc-1 \
  --json '{"speed":1200,"mode":"auto"}'
```

`nodrad` caches desired twin state locally. A local adapter reports actual state using:

```http
POST /v1/twins/plc-1/reported
Authorization: Bearer <local-token>
Content-Type: application/json

{"reported":{"speed":1198,"mode":"auto"}}
```

## Certificate identity / mTLS

Nodra can sign an edge-generated CSR at enrollment:

```bash
nodra-server --pki --data /var/lib/nodra ...
nodrad init ... --request-certificate
```

The edge private key is generated locally and never sent to the control plane. Enrollment returns only the signed client certificate and CA certificate. To enforce mTLS on the server, configure HTTPS plus `--client-ca` using the Nodra CA or your enterprise CA. `--require-client-cert` / `NODRA_REQUIRE_CLIENT_CERT` refuses clients that present no certificate. Bearer credentials remain supported for bootstrap and non-mTLS deployments.

## Docker app reconciliation

Create a desired deployment:

```bash
nodractl --token "$NODRA_ADMIN_TOKEN" deployments create \
  --site site_... \
  --name vision-worker \
  --version 2.1.0 \
  --image ghcr.io/example/vision:2.1.0 \
  --desired running
```

Run the agent with `runner: "docker"`. It continuously compares desired state to the local container and converges it. Docker socket access is therefore **opt-in** and is not present in the default Kubernetes agent manifest.

Before every `docker pull`, the agent optionally verifies the image's signature via an externally-installed `cosign` binary, controlled by `signature_mode` (`enforce`/`warn`/`skip`, default `warn` so existing unsigned images keep working) plus `cosign_certificate_identity_regexp`/`cosign_certificate_oidc_issuer`. If a deployment stays unable to reach `running` past `deploy_health_grace` (default `60s`), the agent asks the control plane to roll it back to the last image/version that was healthy — binary Docker-state health, not an app-level check, and not a canary/staged rollout. See `docs/SECURITY-MODEL.md`.

## Kubernetes

### User demo (recommended)

```bash
./scripts/demo-k8s.sh
```

Or Helm with the demo profile (control plane + agent + `nodra-sim`):

```bash
helm upgrade --install nodra ./charts/nodra \
  --namespace nodra-demo --create-namespace \
  -f charts/nodra/values-demo.yaml \
  --set image.repository=ghcr.io/zyvorai/nodra --set image.tag=0.2.1
```

Sign in: `admin` / `nodra-demo-admin`. Simulation runs the full **A–Z** fleet (26 lettered sites) with live heartbeats, telemetry, twins, and a detailed **Logs** terminal in the console.

### Raw manifests

```bash
kubectl apply -f deployments/kubernetes/namespace.yaml
kubectl apply -f deployments/kubernetes/serviceaccount.yaml
kubectl apply -f deployments/kubernetes/secret.demo.yaml
kubectl apply -f deployments/kubernetes/deployment.yaml
kubectl apply -f deployments/kubernetes/service.yaml
kubectl apply -f deployments/kubernetes/simulation.yaml
# optional: pvc/pdb/networkpolicy for harder production posture
```

Demo kustomize overlay (emptyDir, secret + sim):

```bash
kubectl kustomize --load-restrictor LoadRestrictionsNone deployments/kustomize/demo | kubectl apply -f -
```

### Helm (production-shaped)

```bash
helm upgrade --install nodra ./charts/nodra \
  --namespace nodra --create-namespace \
  -f charts/nodra/values-production.yaml
```

Create Secret `nodra-production` first (see [docs/DEPLOYMENT.md](docs/DEPLOYMENT.md)). The production values file stays at one replica, uses Postgres, cert-manager TLS, and a restricted NetworkPolicy. It is not HA and does not configure PITR. Demo installs can omit that file and pass `--set secrets.adminToken=... --set secrets.enrollmentToken=...`; if tokens are omitted on a chart-managed Secret, Helm creates long random values and preserves them across upgrades.

Helm defaults to one replica. File mode fails the render when `replicaCount` is above 1, because the data volume is `ReadWriteOnce`. Postgres mode may set `replicaCount` above 1 and then uses a rolling update; that shares fleet state, and it is not a full HA claim. The PDB allows a single pod to be drained instead of blocking node maintenance.

For MQTT TLS on the optional in-cluster agent, set `agent.mqtt.tls.existingSecret` to a Secret with `tls.crt` and `tls.key` (`ca.crt` when `requireClientCert` is true).

## Web console

Embedded static UI (no CDN). After login the shell exposes:

| Tab | Contents |
|---|---|
| Overview | Fleet counts + live activity preview |
| Sites | Enrolled sites |
| Devices | Devices and twins |
| Streams | Cloud routes (+ seed demo route) |
| Apps | Deployments and alerts |
| Dead letters | Replayable DLQ |
| Logs | Terminal-style activity stream |

Auth uses the admin bearer as the session token returned by login. See [docs/DEMO.md](docs/DEMO.md) and [docs/API.md](docs/API.md).

## CLI

```text
nodractl status
nodractl status json
nodractl preflight
nodractl doctor
nodractl support-bundle --out DIR
nodractl overview
nodractl sites list
nodractl sites revoke SITE_ID
nodractl devices
nodractl twins list
nodractl twins desired DEVICE_ID --json '{}'
nodractl routes list|create|delete
nodractl deployments list|create
nodractl alerts list|resolve
nodractl dlq list|replay|delete
nodractl events
nodractl publish ...
```

`preflight` checks `/healthz`, `/readyz`, and `/api/v1/version`. `doctor` also probes overview when a token is set. `support-bundle` writes those responses under a directory and does not write tokens. Use `--insecure` for lab self-signed HTTPS.

## Architecture

See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md). File mode uses append-only fsynced WALs with in-memory indexes and periodic compaction. Postgres mode reads fleet state from the database. There is no file-per-event spool in v0.2.

## Protocol strategy

Nodra does not try to invent a new universal wire protocol. The core provides local runtime, durability, fleet state, twins, deployment reconciliation and policy surfaces. Protocol adapters plug into that runtime.

Included:

- HTTP ingress
- MQTT 3.1.1 ingress/local subscription (QoS 0/1/2 in both directions; optional TLS and client certificates; persistent sessions replay queued QoS 1 and QoS 2; subscription lists do not survive a broker restart)
- Modbus TCP/RTU, J1939, OPC-UA, NATS, generic serial/USB connectors
- Connector SDK

Planned/community adapters: Zenoh, Kafka bridge. See `pkg/connector` and [ROADMAP.md](ROADMAP.md).

## Security defaults

- management API requires an admin bearer token (optional viewer token is GET-only)
- console login uses admin or viewer credentials (`NODRA_ADMIN_*` / `NODRA_VIEWER_*`); five failed logins or enrollments from one address in five minutes return 429
- local HTTP ingest requires `local_token` unless `allow_unauthenticated_local`
- MQTT may require username/password or per-device `mqtt_clients` grants; optional broker TLS (`mqtt_cert_file` / `mqtt_key_file`) and client certificates
- per-site random credentials are stored as SHA-256 hashes centrally
- optional CSR certificate enrollment; `NODRA_REQUIRE_CLIENT_CERT` for control-plane mTLS
- site revocation
- no Kubernetes service-account token
- non-root UID/GID 65532
- read-only root filesystem
- all Linux capabilities dropped
- RuntimeDefault seccomp
- request body limits and HTTP timeouts
- CSP / anti-framing / no-sniff browser headers
- no external JS/fonts/CDN in the dashboard

See [SECURITY.md](SECURITY.md) and [docs/SECURITY-MODEL.md](docs/SECURITY-MODEL.md).

## Testing

```bash
make test
make race
make vet
make smoke
make qualify
make test-all PORT=20059 HOST=212.8.248.187   # local + remote when HOST reachable
./scripts/release-check.sh
```

| Target / script | What it covers |
|---|---|
| `make smoke` | Local control plane + agent + login + list APIs |
| `make qualify` | Software matrix → `evidence/qualification/software-matrix.json` |
| `make demo-client` | Client demo against live URL / `.deploy-last` |
| `make demo-k8s` | kind + Helm + sim |
| `make test-all` | Build, local smoke, optional remote deploy/smoke + short sim |
| `./scripts/smoke-remote.sh` | Health, login, activity, array list endpoints |

The release check runs formatting, vet, race tests, static builds, YAML/OpenAPI parsing, a live control-plane + agent smoke test and licensing checks.

GitHub CI additionally runs:

- current Go and minimum-compatible Go
- `govulncheck`
- container build
- Helm lint/render
- kubectl/Kustomize client validation
- kind Kubernetes E2E (including sim + activity assertions)
- CodeQL

## Docs

| Doc | Topic |
|---|---|
| [https://zyvor.dev/docs/nodra](https://zyvor.dev/docs/nodra) | Product docs on zyvor.dev |
| [docs/FAQ.md](docs/FAQ.md) | Licensing, support, production-readiness, protocol support |
| [docs/TROUBLESHOOTING.md](docs/TROUBLESHOOTING.md) | Real operational issues, with the fix |
| [docs/DEMO.md](docs/DEMO.md) | User demo, console, `nodra-sim`, lab scripts |
| [docs/RELAY.md](docs/RELAY.md) | Nodra → Zyvor Relay Accept bridge |
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | Components and durability |
| [docs/API.md](docs/API.md) | HTTP API summary |
| [docs/openapi.yaml](docs/openapi.yaml) | OpenAPI schemas |
| [docs/OPERATIONS.md](docs/OPERATIONS.md) | Backup, upgrades, ports, stores, disk pressure |
| [docs/PRODUCTION.md](docs/PRODUCTION.md) | Production runbook and current maturity |
| [docs/QUALIFICATION.md](docs/QUALIFICATION.md) | Software matrix and lab checklist |
| [docs/SCALE.md](docs/SCALE.md) | Configured limits and how to run the soak |
| [docs/DEPLOYMENT.md](docs/DEPLOYMENT.md) | Production Helm profile and GitOps |
| [docs/RECOVERY.md](docs/RECOVERY.md) | File and PostgreSQL backup; PITR is not shipped |
| [docs/SECURITY-MODEL.md](docs/SECURITY-MODEL.md) | Trust boundaries + RBAC |
| [docs/SUPPLY_CHAIN.md](docs/SUPPLY_CHAIN.md) | Release / SBOM / signing |
| [docs/POLICY_PACKS.md](docs/POLICY_PACKS.md) | Fleet image allowlists |
| [docs/INDUSTRIAL_PROTOCOLS.md](docs/INDUSTRIAL_PROTOCOLS.md) | Modbus, J1939, OPC-UA, serial |
| [docs/NATS_BRIDGE.md](docs/NATS_BRIDGE.md) | NATS subscribe bridge |
| [ROADMAP.md](ROADMAP.md) | Next milestones |

Product page: [https://zyvor.dev/nodra](https://zyvor.dev/nodra).

## License

### Open source (Apache-2.0)

This repository is licensed under the [Apache License, Version 2.0](LICENSE).
You may use, modify, and run it for personal, lab, and commercial production
use at no charge, subject to Apache-2.0 (preserve notices / NOTICE where required).

### Enterprise

Production support, SLAs, and Zyvor Enterprise products are licensed separately.
Contact [sales@zyvor.dev](mailto:sales@zyvor.dev) or see [zyvor.dev](https://zyvor.dev).
