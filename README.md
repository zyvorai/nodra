# Nodra

**The open edge runtime that keeps sites running when the cloud doesn't.**

Nodra is an Apache-2.0 edge runtime and control plane from Zyvor. It gives remote sites a local MQTT/HTTP ingress, durable store-and-forward, local routes, device twins, edge application reconciliation, fleet health, replayable dead letters and a clean web control plane.

> **v0.2.0** is a serious single-control-plane release. Edge sites are offline-first. The control plane uses an embedded append-only WAL and intentionally runs as one writer/replica. Horizontal HA is a future storage mode, not a claim in this release.

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

## v0.2 highlights

- **Real MQTT 3.1.1 edge ingress**: CONNECT, SUBSCRIBE, QoS 0/1 PUBLISH, PUBACK, PINGREQ and local subscriber fan-out.
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
- **Fleet revocation**: revoke a site identity/token centrally.
- **Edge app reconciliation**: Docker desired `running|stopped` state, environment, ports, volumes and command.
- **Modbus TCP client**: dependency-free function 0x03/0x06 building block for adapters.
- **Connector SDK**: stable Go interface for MQTT/Modbus/OPC-UA/NATS/Zenoh/etc adapters without coupling them to the core.
- **Metrics**: Prometheus counters plus delivery queue/DLQ gauges.
- **Apple-inspired Zyvor UX**: embedded, no CDN, no external fonts, orange Zyvor accent.
- **Kubernetes-ready**: raw manifests, Kustomize, Helm, Restricted Pod Security defaults, no service-account token.
- **Supply chain**: CodeQL, race tests, current-Go CI, govulncheck, multi-arch OCI, SBOM, provenance and keyless cosign signing on releases.

## Quick start

### Build

```bash
git clone https://github.com/zyvorai/nodra.git
cd nodra
make build
```

The repository has no runtime Go module dependencies.

### Start the control plane

```bash
export NODRA_ADMIN_TOKEN='change-this-admin-token'
export NODRA_ENROLLMENT_TOKEN='change-this-enrollment-token'

./bin/nodra-server \
  --listen :8080 \
  --data ./data
```

Open `http://127.0.0.1:8080` and **Sign in** (`admin` / your admin token unless `NODRA_ADMIN_PASSWORD` is set).

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

Any MQTT 3.1.1 client can publish QoS 0/1:

```bash
mosquitto_pub -h 127.0.0.1 -p 1883 \
  -t factory/line-1/temperature \
  -q 1 -m '{"c":31.2}'
```

Nodra's embedded MQTT broker is deliberately focused on edge ingress/local fan-out. Persistent sessions and QoS 2 are not claimed in v0.2.

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
      "timeout": "3s"
    }
  ]
}
```

Cloud forwarding and local routing are independent. A local destination failure enters the agent's durable local-delivery WAL and retries with backoff.

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

The edge private key is generated locally and never sent to the control plane. Enrollment returns only the signed client certificate and CA certificate. To enforce mTLS on the server, configure HTTPS plus `--client-ca` using the Nodra CA or your enterprise CA. Bearer credentials remain supported for bootstrap and non-mTLS deployments.

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

## Kubernetes

### Customer demo (recommended)

```bash
./scripts/demo-k8s.sh
```

Or Helm with the demo profile (control plane + agent + `nodra-sim`):

```bash
helm upgrade --install nodra ./charts/nodra \
  --namespace nodra-demo --create-namespace \
  -f charts/nodra/values-demo.yaml \
  --set image.repository=ghcr.io/zyvorai/nodra --set image.tag=0.2.0
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
  --namespace nodra-system --create-namespace \
  --set secrets.adminToken=... --set secrets.enrollmentToken=...
```

If tokens are omitted, Helm creates long random values and preserves them across upgrades. Enable live fleet simulation with `--set simulation.enabled=true`.

The v0.2 control plane is intentionally **one replica** because its embedded WAL is single-writer. The PDB allows the single pod to be drained instead of blocking node maintenance.

## CLI

```text
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

## Architecture

See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md). Core persistence uses append-only fsynced WALs with in-memory indexes and periodic compaction. There is no file-per-event spool in v0.2.

## Protocol strategy

Nodra does not try to invent a new universal wire protocol. The core provides local runtime, durability, fleet state, twins, deployment reconciliation and policy surfaces. Protocol adapters plug into that runtime.

Included:

- HTTP ingress
- MQTT 3.1.1 QoS 0/1 ingress/local subscription
- Modbus TCP client building block
- Connector SDK

Planned/community adapters: OPC-UA, serial, NATS, Zenoh and Kafka bridge. See `pkg/connector` and [ROADMAP.md](ROADMAP.md).

## Security defaults

- management API requires an admin bearer token
- per-site random credentials are stored as SHA-256 hashes centrally
- optional CSR certificate enrollment
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
./scripts/release-check.sh
```

The release check runs formatting, vet, race tests, static builds, YAML/OpenAPI parsing, a live control-plane + agent smoke test and licensing checks.

GitHub CI additionally runs:

- current Go and minimum-compatible Go
- `govulncheck`
- container build
- Helm lint/render
- kubectl/Kustomize client validation
- kind Kubernetes E2E
- CodeQL

## License

Apache License 2.0. See [LICENSE](LICENSE).
