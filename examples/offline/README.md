# Offline demonstration

1. Start the control plane and one edge agent.
2. Stop the control plane.
3. POST several events to the agent's `/v1/publish` endpoint.
4. Check `GET /healthz` on port 9091: `pending` increases.
5. Restart the control plane. The queue drains automatically.

The automated agent test suite reproduces this persistence behavior without depending on timing or an external network.
