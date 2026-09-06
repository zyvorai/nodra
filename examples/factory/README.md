# Factory example

After starting a control plane and `nodrad`, publish machine telemetry through the local edge agent:

```bash
./publish.sh http://127.0.0.1:9091 local-secret
```

The script emits temperature, vibration, and RPM on `factory/press-07/telemetry`.
