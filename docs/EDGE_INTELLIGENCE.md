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

## 4. Offline rule engine

Local alarm rules evaluated at the edge with no control-plane connection.
Set `"rules_file": "/etc/nodra/rules.json"` in `nodrad.json`. The file is
validated strictly at startup; an invalid file stops `nodrad` with
line-numbered errors such as `line 7: rule "hot": clear: for op "above" clear must be <= raise`.

```json
{
  "version": 1,
  "rules": [{
    "name": "motor-hot",
    "topic": "plant/+/temp",
    "field": "c",
    "op": "above",
    "raise": 90,
    "clear": 80,
    "debounce": "10s",
    "clear_debounce": "30s",
    "rate_limit": {"max": 3, "per": "1h"},
    "window": {"days": ["mon","tue","wed","thu","fri"], "start": "06:00", "end": "22:00", "timezone": "+05:30"},
    "max_keys": 256,
    "on_fire":  [{"type": "alert", "severity": "critical", "message": "motor too hot"}],
    "on_clear": [{"type": "emit", "topic": "plant/alarms/cleared", "transform": {"set_fields": {"ack": false}}}]
  }]
}
```

| Concept | Behaviour |
|---------|-----------|
| Matching | MQTT topic filter on `topic`; state is kept per concrete topic. `field` is a dotted path to a finite JSON number; anything else (missing, string, NaN, oversize or invalid payload) is counted as `skipped` and never changes state |
| Hysteresis | `op: above`: raise when value > `raise`, clear when value < `clear` (`clear <= raise`). `op: below` mirrors it (`clear >= raise`). Values in the band keep the current state |
| Debounce | The raise (or clear) condition must hold continuously for `debounce` / `clear_debounce` (max 24 h). It is evaluated when a message arrives, so a sensor that goes silent does not clear or fire |
| Window | `days` / `start` / `end` in site-local time: `timezone` is `Local` (default, the device zone), `UTC`, a fixed offset (`+05:30`) or an IANA name when the host has tzdata (nodrad does not embed tzdata, to stay small). `start > end` wraps midnight and belongs to the day it started. Outside the window a rule cannot raise, but an alarm that is already firing can still clear |
| Rate limit | At most `max` (<= 60) firings per sliding `per`; extra firings still change state but run no actions (`rate_limited`). Clears are never suppressed |
| Actions | Only `emit` (event on a fixed topic, optionally shaped by the existing `transform` fields set_fields/drop_fields/wrap_as/set_headers) and `alert` (event on `nodra/rules/alert/<rule>` with severity and message). Events go through the normal ingest path, so existing `local_routes` and the uplink handle them; there is no code, shell or new egress. Events a rule emits are never evaluated by rules |
| Bounds | <= 256 rules, <= 8 actions per list, <= 4096 keys per rule and 16 384 in total (an idle key is evicted to admit a new topic; when every key is active the new topic is rejected and counted as `key_overflow`), payloads over 64 KiB are not parsed |
| Clock | Time comes from the device clock when the message is ingested. If the clock steps back, pending debounce and rate-limit history are clamped so a rule never wedges |

State persistence: firing, pending and recent-fire history is written to
`<data_dir>/rules-state.json` (atomic write + fsync, the same mechanism as
`twins.json`) on every fire/clear, every 5 s for pending changes, and at
shutdown. State is persisted **before** actions run, so a crash between the
two loses an action (at most once) but a restart never re-fires an alarm that
is already firing and never forgets that it is firing. A pending debounce is
resumed with its original start time. A corrupt or wrong-version state file
is moved to `rules-state.json.corrupt` and the engine starts clean; state for
rules that no longer exist is dropped, state is kept across threshold edits.
Idle keys are not persisted, and the file is bounded by the key caps.

Endpoints (local API, bearer `local_token`):

- `GET /v1/rules` - per-rule keys and state, counters, currently firing alarms. The heartbeat also carries `rules_firing`.
- `POST /v1/rules/evaluate` - dry run. Body `{"rules": <rules object or a string holding the rules text>, "start": "<RFC3339>", "messages": [{"topic":..., "payload":{...}, "time":"<RFC3339>"}]}`. `rules` is optional (defaults to the loaded set); pass it as a string to get line-numbered errors for your own text (`400` with `errors: [{line, rule, field, message}]`). A message without `time` is one second after the previous one. Up to 10 000 messages; the report lists every fire/clear (first 1000), the events the actions would ingest, skip/out-of-window/rate-limit counters and the alarms still firing at the end. A dry run uses a fresh engine: nothing is persisted, ingested or executed, and live state is untouched.

Not done: solar (sunrise/sunset) offsets, windowed aggregations (averages over
time), rules over several fields or topics, and hot reload (restart `nodrad`
to change rules).

## 5. Fleet-wide config dry-run

`POST /api/v1/twins/plan` (admin) validates and diffs a desired fleet
configuration against every affected twin without storing anything:

```bash
curl -X POST $SERVER/api/v1/twins/plan -H "Authorization: Bearer $ADMIN" -d '{
  "defaults": {"mode": "auto"},
  "site_ids": ["site_1"],
  "devices": {"plc-1": {"speed": 1500}, "plc-2": {"speed": 9000}}
}'
```

The desired document for a device is `defaults` (applied to every listed device
and every device of `site_ids`) overlaid key by key by its own entry. Per
device the response gives `status` (`change`, `unchanged`, `invalid` when it
violates the twin schema or uses the reserved `ota` key, `unknown_device`),
the `changes` diff, `violations` and the twin `desired_version` it was
computed against, plus a `summary`, `valid` (no invalid or unknown devices)
and a `plan_hash`.

`plan_hash` is `sha256:` over the sorted list of (device, status, base
desired_version, resulting desired document). It does not depend on request
key order or time, so the same request against the same fleet state always
gives the same hash, and it changes if any twin was modified in between: an
apply step can require the hash to match and refuse a stale plan. The apply
endpoint itself is not built yet. An in-flight `desired.ota` request is carried
over unchanged. Org-scoped tokens only see their own devices (others come back
as `unknown_device`). At most 5000 devices per plan and the usual body limit.

Fix shipped with this: `PUT /twins/{id}/desired` used to replace the whole
desired document and so silently erased an in-flight `desired.ota` OTA
request; it is now preserved unless the request supplies its own `ota`.

## 6. Signed, staged OTA safety (pkg/ota)

Library logic with unit tests (fake clocks, fake devices); it is not yet wired
into the campaign endpoints or the device agent, and nothing is downloaded or
executed. Standard library crypto only.

- **Signed manifest**: `Manifest` gained `key_id`, `counter`, `issued_at`, `expires_at`. `Signature` is a base64 Ed25519 signature over `nodra-ota-manifest-v1\n` plus the JSON of every other field, so tampering with any field (including counter, key id and validity) invalidates it. The device holds the trust root (`Verifier.Keys`, key id to public key); an unknown key id is refused.
- **Artifact check**: `VerifyArtifact` streams at most `size_bytes+1` bytes and requires the exact size (`ErrTruncated`, `ErrOversized`) and SHA-256 (`ErrDigestMismatch`).
- **Anti-rollback**: a per-lineage (type/architecture/board) monotonic `counter`. A counter below the committed floor is a downgrade, equal is a replay; the floor only advances in `Commit` after the health check passes, so a failed update can be retried. `FileCounters` persists the floor with an atomic write and fails closed (a corrupt file is an error, never "no floor").
- **Clock skew**: validity is checked with a tolerance (`MaxSkew`, default 5 min); a device clock that is behind an already-committed manifest is refused (`ErrClockRegression`). Counters, not clocks, are the rollback defence.
- **Staged rollout**: `Rollout` releases cumulative waves (for example 5 %, 25 %, 100 %) in a stable per-plan hash order, waits for every released device to commit and a per-wave soak, then releases the next wave. A device is unhealthy when its OTA state is failed/rollback/cancelled, its heartbeat is stale, or the metrics the agent already sends in its heartbeat (`goroutines`, `heap_bytes`, `spool_risk`) breach the policy. More unhealthy devices than `max_failure_percent` halts the rollout (nothing further is released) and, with `auto_rollback`, returns the list of devices to roll back until none remain. The controller only returns decisions; a caller applies them. Its state is JSON-serialisable and resumes after a restart.

Not done: key rotation/threshold signing (Uptane-style two repositories),
wiring the verifier into `nodrad`/the Zyvor OTA agent, and replacing the
existing `OTACampaign` controller with `Rollout`.

## 7. MQTT 5

Not built. `internal/mqtt` accepts only protocol level 4 (MQTT 3.1.1)
(`parseConnect` rejects anything else) and every packet codec assumes the
3.1.1 variable header. Reason codes, user properties and session expiry need a
property codec and changes to CONNECT/CONNACK/PUBLISH/PUBACK/SUBSCRIBE/
DISCONNECT handling, i.e. a rewrite of the broker, which this change did not
take on.
