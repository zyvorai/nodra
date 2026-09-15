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

## Management

- `GET /overview`
- `GET /sites`
- `POST /sites/{id}/revoke`
- `GET /devices`
- `GET /twins`
- `PUT /twins/{deviceID}/desired`
- `GET|POST /routes`
- `DELETE /routes/{id}`
- `GET|POST /deployments`
- `PATCH /deployments/{id}` — update `version`, `image`, and/or `desired_state` (`running|stopped`); an `image` change is checked against policy packs first
- `DELETE /deployments/{id}`
- `GET|POST /policy-packs` — fleet policy packs (v1: `allowed_images` allowlist only)
- `PATCH|DELETE /policy-packs/{id}`
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

See [`openapi.yaml`](openapi.yaml) for schemas. Coverage of mux routes is gated by
`scripts/openapi-coverage.py` in `make qualify`.
