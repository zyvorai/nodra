# Edge intelligence

Three features that work offline at the edge, chosen from the field survey in
[RESEARCH.md](RESEARCH.md).

## 1. Sparkplug B ingress

Set `"sparkplug_enabled": true` in `nodrad.json`. MQTT messages on the
`spBv1.0/...` namespace are then decoded (standard library protobuf reader, no
new dependency) instead of being wrapped as an opaque string.

| Topic | Handling |
|-------|----------|
| `spBv1.0/<group>/<NBIRTH\|NDEATH\|DBIRTH\|DDEATH\|NDATA\|DDATA\|NCMD\|DCMD>/<node>[/<device>]` | Payload decoded to JSON and ingested on the **same topic**, so existing routes such as `spBv1.0/#` keep working |
| `spBv1.0/STATE/<host>` | JSON `{"online":...}` or legacy `ONLINE`/`OFFLINE` recorded as primary-host state |

Decoded payload shape:

```json
{"namespace":"spBv1.0","group":"plant","type":"NDATA","node":"n1","seq":2,"timestamp":1700000001000,
 "metrics":[{"name":"Temp","alias":1,"datatype":3,"value":21}],
 "anomalies":["seq_gap"],"state":{"online":true,"needs_rebirth":true,...}}
```

What the tracker does, per the Sparkplug lifecycle:

- **Aliases**: names announced in `NBIRTH`/`DBIRTH` are resolved in later alias-only `NDATA`/`DDATA`; an alias that was never born is flagged `unknown_alias`.
- **Birth/death state**: `NBIRTH` brings a node online and marks its devices offline until they re-announce; `NDEATH` takes node and devices offline. An `NDEATH` whose `bdSeq` does not match the live session (a stale last-will from a previous connection) is ignored and flagged `stale_ndeath`.
- **Sequence numbers**: the node-wide 0-255 counter is checked; a gap flags `seq_gap` and sets `needs_rebirth`. Other anomalies: `birth_seq_nonzero`, `missing_seq`, `data_without_nbirth`, `data_without_dbirth`, `dbirth_without_nbirth`, `tracker_full`.
- **State events**: when a node or device flips online/offline, an extra event is ingested on `nodra/sparkplug/state/<group>/<node>[/<device>]` carrying the entity state, so a route can alert on it.
- **Event time** is taken from the payload timestamp (UTC ms) unless it is before 2000 or in the future.

Hostile or malformed input (bad topic, truncated or overlong varints, oversized
lengths, more than 4096 metrics, payloads over 1 MiB) is rejected, never
spooled, and counted in `invalid_total`. At most 10 000 nodes and 1000 devices
per node are tracked. NaN/Inf floats become `null` so the JSON stays valid.
Dataset, template, and byte-array metric values are flagged `unsupported`
rather than expanded.

`GET /v1/sparkplug` (local API, bearer `local_token`) lists tracked
entities, host states, `invalid_total` and `anomaly_total`.

Not done: Nodra does not publish `NCMD` rebirth requests or act as a Sparkplug
primary host, and does not push Sparkplug entities into the cloud device/twin
registry (Sparkplug ids are not Nodra device ids). The tracker is in memory and
rebuilt from births after a restart.

## 2. Spool capacity forecast

The agent samples its WAL-backed spool every 10 s and fits a least-squares
trend of net growth over the last 15 minutes. While the uplink is down the
slope is the ingest rate, so the forecast answers "how long can this site stay
offline?".

`GET /v1/spool/forecast` (also `spool_forecast` in `GET /healthz`):

| Field | Meaning |
|-------|---------|
| `growth_bytes_per_sec`, `growth_items_per_sec` | Net growth (negative while draining) |
| `time_to_full_seconds` | Time until the byte **or** item cap is hit; `-1` if not growing or unbounded |
| `drain_seconds` | Time to empty at the current drain rate, else `-1` |
| `outage_seconds` | How long the control plane has been unreachable (no successful heartbeat for over two intervals) |
| `outage_capacity_seconds` | Outage so far plus time to full: the total WAN-down duration the spool absorbs |
| `fill_ratio` | max(bytes/max_bytes, items/max_items) |
| `risk` | `critical` if full ratio >= 0.90 or time to full < 1 h; `watch` if >= 0.70 or < 6 h; else `ok` |
| `data_loss` | What happens at full: `rejects_new`, `drops_oldest`, or `drops_newest` (from `spool_policy`) |
| `confidence` | `low` until 10 samples are in the window |

The heartbeat also carries `spool_time_to_full_seconds`,
`spool_growth_bytes_per_sec`, `spool_fill_ratio`, `spool_risk` and
`uplink_outage_seconds`, and the server exports
`nodra_site_spool_time_to_full_seconds{site_id}` and
`nodra_site_spool_fill_ratio{site_id}` on `/metrics`. Example alert:
`nodra_site_spool_time_to_full_seconds > 0 and nodra_site_spool_time_to_full_seconds < 3600`.

Caveats: the estimate is linear and short-window; bursty ingest (shift
changes) is smoothed, not predicted. Statistics reset when the agent restarts.

## 3. Twin schema validation and drift

Constrain what operators may put in a twin's desired state, and see which
devices have not converged.

```bash
curl -X PUT $SERVER/api/v1/twins/plc-1/schema -H "Authorization: Bearer $ADMIN" \
  -d '{"schema":{"type":"object","required":["speed"],"additionalProperties":false,
       "properties":{"speed":{"type":"integer","minimum":0,"maximum":3000}}}}'

# preview a change without storing it
curl -X PUT "$SERVER/api/v1/twins/plc-1/desired?dry_run=true" -H "Authorization: Bearer $ADMIN" \
  -d '{"desired":{"speed":1500}}'
# {"dry_run":true,"valid":true,"changes":[{"path":"/speed","kind":"change","from":1200,"to":1500}],...}

curl $SERVER/api/v1/twins/drift -H "Authorization: Bearer $ADMIN"
```

- Supported schema keywords: `type` (object, string, number, integer, boolean, array, null), `properties`, `required`, `additionalProperties` (boolean), `enum`, `minimum`, `maximum`, `minLength`, `maxLength`, `items`, `minItems`, `maxItems`, `title`, `description`. Anything else (`$ref`, `oneOf`, `pattern`, ...) is rejected with `400` when the schema is registered, so a schema never silently enforces less than it says. Depth and size are bounded.
- A desired update that violates the schema gets `422` with per-path `violations`; nothing is stored.
- **Drift** lists desired properties the device has not reported (`missing`) or reports differently (`mismatch`). Properties only the device reports are not drift. The reserved `ota` key is skipped (OTA has its own lifecycle). Reported state is always accepted (it is the device's truth) but schema violations in it are listed as `reported_violations` and keep the twin flagged.
- `/metrics` exports `nodra_twins_drifted` and `nodra_twins_total`.
- Org scoping follows the other twin endpoints.
