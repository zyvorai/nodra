# API guide

Base path: `/api/v1`.

Authentication:

- management endpoints: `Authorization: Bearer <admin-token>`
- agent endpoints: `Authorization: Bearer <site-token>` or a verified mTLS client certificate whose Common Name equals the site ID
- enrollment: bootstrap token in the JSON body

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
- `GET /alerts`
- `POST /alerts/{id}/resolve`
- `GET /events?minutes=60`
- `GET /deadletters`
- `POST /deadletters/{deliveryID}/replay`
- `DELETE /deadletters/{deliveryID}`

## Agent

- `POST /enroll`
- `POST /heartbeat`
- `POST /devices/register`
- `GET /agent/deployments?site_id=...`
- `POST /agent/deployments/{id}/status`
- `GET /agent/twins?site_id=...`
- `POST /agent/twins/{deviceID}/reported`

See `openapi.yaml` for schemas.
