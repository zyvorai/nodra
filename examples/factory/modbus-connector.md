# Modbus connector example

Add a poller to `nodrad.json`:

```json
{
  "connectors": [
    {
      "type": "modbus",
      "name": "press-07",
      "config": {
        "address": "127.0.0.1:5020",
        "unit_id": 1,
        "start": 0,
        "quantity": 4,
        "topic": "factory/press-07/modbus",
        "interval": "5s",
        "timeout": "2s"
      }
    }
  ]
}
```

The poller reads holding registers and ingests JSON events through the same durable spool path as MQTT/HTTP (`x-nodra-ingress: modbus`).
