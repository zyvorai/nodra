# Nodra → Relay example

Sidecar bridge that turns Nodra cloud-route webhooks into Zyvor Relay Accept events.

## Smoke (no Docker)

```bash
# from nodra repo root
make smoke-relay-bridge
```

## Compose (bridge + mock Accept)

```bash
docker compose -f examples/relay/docker-compose.bridge.yml up --build
```

- Mock Relay: `http://127.0.0.1:18095` (`POST /v1/events`)
- Bridge: `http://127.0.0.1:8095/v1/nodra/delivery`

Point a Nodra route `target_url` at the bridge, then publish an edge event.

## Live Relay

```bash
export RELAY_BASE_URL=https://your-relay:8443
export RELAY_AUTH_TOKEN=<jwt>
docker compose -f examples/relay/docker-compose.bridge.yml up bridge
```

Full docs: [docs/RELAY.md](../../docs/RELAY.md).
