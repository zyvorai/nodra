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
- `PATCH /deployments/{id}` — update `version`, `image`, and/or `desired_state` (`running|stopped`)
- `DELETE /deployments/{id}`
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
- `GET /agent/twins?site_id=...`
- `POST /agent/twins/{deviceID}/reported`

See `openapi.yaml` for schemas.
