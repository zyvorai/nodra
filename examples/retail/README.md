# Retail store example

Use Nodra as a store-local buffer for POS/stock events. Suggested topics:

- `retail/store-042/pos`
- `retail/store-042/inventory`
- `retail/store-042/fridge/telemetry`

Route `retail/+/inventory` to a central inventory webhook while the site continues accepting events during WAN loss.

Sign in to the control-plane console (**Streams**) to create the cloud route, or:

```bash
nodractl --server http://127.0.0.1:8080 --token "$NODRA_ADMIN_TOKEN" \
  routes create \
  --name inventory \
  --topic 'retail/+/inventory' \
  --target https://inventory.example.com/edge
```

See [docs/DEMO.md](../../docs/DEMO.md) for console and simulator walkthroughs.
