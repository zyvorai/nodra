---
hero:
  eyebrow: API
  title: API guide
---

Base path: `/api/v1`.

Authentication:

- management endpoints: `Authorization: Bearer <admin-token>`
- console session: `POST /auth/login` returns the same admin bearer for use as `Authorization`
- agent endpoints: `Authorization: Bearer <site-token>` or a verified mTLS client certificate whose Common Name equals the site ID
- enrollment: bootstrap token in the JSON body

Empty list endpoints return JSON arrays (`[]`), never `null`.

## Console auth

`POST /auth/login`

```json
{"username":"admin","password":"<NODRA_ADMIN_PASSWORD or admin token>"}
```

```json
{
  "token": "<admin-token>",
  "user": {"username":"admin","role":"admin"}
}
```

`GET /auth/me` — requires bearer; returns the current admin user descriptor.

Defaults: `NODRA_ADMIN_USER=admin`, `NODRA_ADMIN_PASSWORD` falls back to `NODRA_ADMIN_TOKEN`.

### OIDC console login

`GET /auth/oidc/login` — redirects to the configured IdP's authorization endpoint. `503` if OIDC isn't configured (`NODRA_OIDC_ISSUER_URL` + `NODRA_OIDC_CLIENT_ID`).

`GET /auth/oidc/callback` — completes the flow: verifies the ID token (RS256 only), resolves the caller's role from `NODRA_OIDC_GROUPS_CLAIM` against `NODRA_OIDC_ADMIN_GROUP`/`NODRA_OIDC_VIEWER_GROUP`, and redirects to `/#oidc_token=<token>&role=<role>` with the SAME static admin/viewer bearer `/auth/login` already issues. `403` if no configured group matched. This is a third way to obtain the existing two roles — not a user directory, not multi-tenant orgs.

## Edge ingress

`POST /events`

```json
{
  "event_id": "edge_...",
  "site_id": "site_...",
  "topic": "factory/line1/temperature",
  "payload": {"c": 31.2},
  "headers": {"x-source": "plc-7"},
  "event_time": "2026-09-06T02:03:04Z"
}
```

A 202 means matching deliveries and the event were durably committed. 503/507 means the edge agent must retain and retry.

## Multi-tenant orgs

`POST /orgs` (global admin token only) creates a named org and mints its own enrollment/admin/viewer bearer tokens, each returned in plaintext exactly once (only their hashes are persisted, like a site's agent token):

```json
{
  "org": {"id": "org_...", "name": "acme", "created_at": "2026-09-15T..."},
  "enrollment_token": "...", "admin_token": "...", "viewer_token": "..."
}
```

Enroll a site into that org by passing its `enrollment_token` to `POST /enroll` instead of the global one — the resulting site's `org_id` is set accordingly. From then on, that org's `admin_token`/`viewer_token` behave exactly like the global admin/viewer tokens, but scoped:

- Every list endpoint (`GET /sites`, `/devices`, `/twins`, `/routes`, `/deployments`, `/policy-packs`, `/ota/campaigns`, `/alerts`, `/events`, `/activity`, `/deadletters`, `/audit`) returns only entries scoped to the caller's own org, plus any fleet-wide entry (empty `site_id` — a global route/policy pack still applies to every org's sites, so it stays visible). OTA campaigns are visible only when the caller can see every site involved.
- Every single-entity admin mutation (`POST /sites/{id}/revoke`, `PUT /twins/{id}/desired`, `DELETE /routes/{id}`, `PATCH|DELETE /deployments/{id}`, `PATCH|DELETE /policy-packs/{id}`, `POST /alerts/{id}/resolve`, `POST /deadletters/{id}/replay`, `DELETE /deadletters/{id}`, `POST|GET /devices/{id}/ota`, `POST /ota/campaigns/{id}/start|promote|abort`) gets `404` if the target belongs to a different org (not `403`, so it can't even confirm the target exists) — and an org-scoped token can never mutate a fleet-wide resource, only create/change entries scoped to its own sites.
- Every *create* endpoint that takes a `site_id` (`POST /routes`, `/deployments`, `/policy-packs`) — or campaign `site_ids`/`device_ids` (`POST /ota/campaigns`) — gets `403` if `site_id` is empty or belongs to a different org — an org-scoped token can't create fleet-wide config or attach a resource to another org's site.
- `GET /overview`'s per-entity counts (`sites`, `devices`, `twins`, `routes`, `deployments`, `open_alerts`, `events`) are org-scoped too. `pending_deliveries`, `delivery_queue_bytes`, and `dead_letters` are the exception — they come from the delivery queue's own stats, which have no per-site breakdown, so they stay fleet-wide totals for every caller.

Two known limitations, not oversights: `GET /audit`/`GET /audit/export` org-filter by dropping non-matching entries out of each fetched page rather than filtering the underlying query, so an org-scoped caller's page can come back thinner than its requested `limit` even though more matching history exists — `next_cursor` still pages forward correctly. And deliveries/DLQ *processing* itself (the background worker, not the `GET /deadletters` list) is never org-aware — it processes the whole fleet's queue regardless of org, which is correct, since delivery workers aren't acting on behalf of any particular caller.

Org management itself (`GET|POST /orgs`, `GET|DELETE /orgs/{id}`) is restricted to the global admin token; an org's own admin token cannot create, list, or delete orgs, including itself. Agent-authenticated endpoints (heartbeat, enrollment, event ingestion, and every `/agent/*` route) were never in scope for org filtering — a site's own agent token already scopes it to itself.

## Management

- `GET /overview`
- `GET /sites` — a global admin/viewer token sees every site; an org-scoped token (see Orgs below) sees only its own org's sites
- `POST /sites/{id}/revoke` — an org-scoped admin token gets `404` (not `403`) on a site outside its own org
- `POST /sites/{id}/rotate` — agent CSR re-sign (PKI); `403 site_revoked` when revoked
- `GET /ca/crl` — unauthenticated X.509 CRL
- `GET /audit` / `GET /audit/export` — durable audit trail; org-filtered (see "Multi-tenant orgs" above for the page-fullness caveat)
- `GET /devices`
- `GET /twins`
- `PUT /twins/{deviceID}/desired`
- `POST /devices/{id}/ota` — set desired OTA state (`pkg/ota.Request`, validated)
- `GET /devices/{id}/ota` — read a device's current OTA request/status
- `GET|POST /ota/campaigns` — list / create staged multi-site OTA canary campaigns
- `GET /ota/campaigns/{id}` — get campaign (refreshes per-device outcomes from twins)
- `POST /ota/campaigns/{id}/start` — begin first canary wave (writes Twin.Desired["ota"])
- `POST /ota/campaigns/{id}/promote` — advance to next wave
- `POST /ota/campaigns/{id}/abort` — abort campaign (does not cancel in-flight device OTAs)
- `GET|POST /routes`
- `DELETE /routes/{id}`
- `GET|POST /deployments`
- `PATCH /deployments/{id}` — update `version`, `image`, and/or `desired_state` (`running|stopped`); an `image` change is checked against policy packs first
- `DELETE /deployments/{id}`
- `GET|POST /policy-packs` — fleet policy packs (v1: `allowed_images` allowlist only)
- `PATCH|DELETE /policy-packs/{id}`
- `GET|POST /orgs` — multi-tenant orgs (v1, global admin token only — an org-scoped admin token gets `403`); `POST` returns `enrollment_token`/`admin_token`/`viewer_token` in plaintext exactly once
- `GET|DELETE /orgs/{id}` — deleting an org doesn't reassign or delete its sites, it just orphans their `org_id` — those sites become invisible to any org-scoped token, still visible to a global one
- `GET /alerts`
- `POST /alerts/{id}/resolve`
- `GET /events?minutes=60`
- `GET /deadletters`
- `POST /deadletters/{deliveryID}/replay`
- `DELETE /deadletters/{deliveryID}`

## Activity (console Logs)

In-memory ring (cap 2000). Used by the **Logs** tab and overview live preview; `nodra-sim` posts chapter lines here.

`GET /activity?limit=250` — newest entries (limit default 250, max 1000).

`POST /activity` — admin ingest of one entry or a batch:

```json
{
  "entries": [
    {
      "level": "chapter",
      "source": "sim",
      "chapter": "A",
      "site_id": "site_...",
      "site": "Alpha Plant",
      "action": "heartbeat",
      "message": "[A] Alpha Plant heartbeat ok",
      "detail": {"online": true}
    }
  ]
}
```

Levels: `info` | `warn` | `error` | `chapter` | `ok`.

## Agent

- `POST /enroll`
- `POST /heartbeat`
- `POST /devices/register`
- `GET /agent/deployments?site_id=...`
- `POST /agent/deployments/{id}/status`
- `POST /agent/deployments/{id}/rollback` — revert to the deployment's `last_good_image`/`last_good_version` (binary Docker-health-gated by the agent, not a canary rollout); returns `{"rolled_back": false}` when there's no known-good target
- `GET /agent/twins?site_id=...`
- `POST /agent/twins/{deviceID}/reported`
- `POST /agent/devices/{id}/ota/status` — report OTA status (`pkg/ota.Status`); `400` on an invalid status, `409` on an illegal state-machine transition

See [`openapi.yaml`](openapi.yaml) for schemas. Coverage of mux routes is gated by
`scripts/openapi-coverage.py` in `make qualify`.
