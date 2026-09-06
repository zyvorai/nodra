# Offline demonstration

1. Start the control plane and one edge agent (`make build` then `nodra-server` / `nodrad`).
2. Stop the control plane.
3. POST several events to the agent's `/v1/publish` endpoint (or `nodractl publish`).
4. Check `GET /healthz` on the agent listen port: `pending` increases.
5. Restart the control plane. The queue drains automatically.

The automated agent test suite reproduces this persistence behavior without depending on timing or an external network.

Optional: with the control plane up, open the console **Logs** tab — durable spool recovery does not rely on activity lines; those are demo/ops telemetry only.
