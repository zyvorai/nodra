# Retail store example

Use Nodra as a store-local buffer for POS/stock events. Suggested topics:

- `retail/store-042/pos`
- `retail/store-042/inventory`
- `retail/store-042/fridge/telemetry`

Route `retail/+/inventory` to a central inventory webhook while the site continues accepting events during WAN loss.
