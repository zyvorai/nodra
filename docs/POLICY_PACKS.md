<!-- Copyright 2026 Zyvor AI Labs · https://zyvor.dev -->
<!-- SPDX-License-Identifier: Apache-2.0 -->
# Fleet policy packs

"Fleet policy packs" had no prior definition anywhere in this project before
this v1. To keep the scope honest and avoid the term meaning something
different next time it comes up: **v1 constrains only which deployment
images are allowed to run.** It does not define RBAC rules, alert
thresholds, or any other kind of fleet policy — those would each be a
separate, future policy-pack type.

## Model

A policy pack is a named, versioned object:

```json
{
  "id": "policy_...",
  "name": "fleet-baseline",
  "version": 1,
  "site_id": "",
  "enabled": true,
  "allowed_images": ["ghcr.io/zyvorai/*"],
  "created_at": "...",
  "updated_at": "..."
}
```

- `site_id` empty means the pack applies fleet-wide; a non-empty `site_id`
  scopes it to one site.
- `allowed_images` entries are glob patterns where `*` matches any sequence
  of characters, **including `/`** — this is deliberately not
  `path/filepath.Match` semantics, since an OCI image reference uses `/` as
  an ordinary separator (registry/namespace/repository), not a path boundary
  a wildcard should stop at. `ghcr.io/zyvorai/*` matches
  `ghcr.io/zyvorai/nodra:v1` and `ghcr.io/zyvorai/team/app:latest` alike.
- A pack with an empty `allowed_images` list is a no-op — it doesn't
  constrain anything (useful for staging a pack before populating it).
- Disabled packs (`enabled: false`) are never enforced.

## Enforcement semantics

Every **enabled** pack that applies to a deployment's site (fleet-wide, plus
any pack scoped to that specific site) and that has at least one
`allowed_images` entry must match the image — constraints are **ANDed**
across applicable packs. If a fleet-wide pack allows `ghcr.io/zyvorai/*` and
a site-scoped pack additionally only allows `ghcr.io/zyvorai/nodra:*`, that
site can only deploy images matching both.

Enforcement is server-side only, in `POST /api/v1/deployments` (create) and
`PATCH /api/v1/deployments/{id}` (only when the patch changes `image`) — a
denial returns `403` and is recorded in the audit trail (`policy.deny`), but
does **not** raise an alert: a denial is the system working as intended, not
a fleet incident.

## API

- `GET /api/v1/policy-packs` — list all packs.
- `POST /api/v1/policy-packs` — create (`name` required; `site_id`,
  `enabled`, `allowed_images` optional).
- `PATCH /api/v1/policy-packs/{id}` — update `name`, `enabled`, and/or
  `allowed_images`.
- `DELETE /api/v1/policy-packs/{id}`.

All admin-gated, like route/deployment CRUD. Also available via
`nodractl policy list|create|delete`.

## Explicitly out of scope for v1

- RBAC-rule packs (who can do what).
- Alert-threshold packs (when an alert fires).
- Any enforcement point other than deployment create/patch (e.g. this does
  not retroactively stop an already-running deployment whose image would
  now be denied).
