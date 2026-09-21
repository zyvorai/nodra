-- Normalized columns and optimistic-concurrency revisions.
-- JSON data remains the document. Columns are written on every upsert so
-- later tenant filters can use them without another schema break.

ALTER TABLE nodra_sites ADD COLUMN IF NOT EXISTS revision BIGINT NOT NULL DEFAULT 1;
ALTER TABLE nodra_sites ADD COLUMN IF NOT EXISTS org_id TEXT NOT NULL DEFAULT '';
UPDATE nodra_sites
SET org_id = COALESCE(data->>'org_id', '')
WHERE org_id = '' AND COALESCE(data->>'org_id', '') <> '';
CREATE INDEX IF NOT EXISTS nodra_sites_org_id_idx ON nodra_sites (org_id);

ALTER TABLE nodra_devices ADD COLUMN IF NOT EXISTS revision BIGINT NOT NULL DEFAULT 1;
ALTER TABLE nodra_devices ADD COLUMN IF NOT EXISTS site_id TEXT NOT NULL DEFAULT '';
UPDATE nodra_devices
SET site_id = COALESCE(data->>'site_id', '')
WHERE site_id = '' AND COALESCE(data->>'site_id', '') <> '';
CREATE INDEX IF NOT EXISTS nodra_devices_site_id_idx ON nodra_devices (site_id);

ALTER TABLE nodra_twins ADD COLUMN IF NOT EXISTS revision BIGINT NOT NULL DEFAULT 1;
ALTER TABLE nodra_twins ADD COLUMN IF NOT EXISTS site_id TEXT NOT NULL DEFAULT '';
ALTER TABLE nodra_twins ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ;
UPDATE nodra_twins
SET site_id = COALESCE(data->>'site_id', '')
WHERE site_id = '' AND COALESCE(data->>'site_id', '') <> '';
UPDATE nodra_twins
SET updated_at = (data->>'updated_at')::timestamptz
WHERE updated_at IS NULL
  AND COALESCE(data->>'updated_at', '') ~ '^[0-9]{4}-';
CREATE INDEX IF NOT EXISTS nodra_twins_site_id_idx ON nodra_twins (site_id);

ALTER TABLE nodra_routes ADD COLUMN IF NOT EXISTS revision BIGINT NOT NULL DEFAULT 1;
ALTER TABLE nodra_routes ADD COLUMN IF NOT EXISTS site_id TEXT NOT NULL DEFAULT '';
UPDATE nodra_routes
SET site_id = COALESCE(data->>'site_id', '')
WHERE site_id = '' AND COALESCE(data->>'site_id', '') <> '';
CREATE INDEX IF NOT EXISTS nodra_routes_site_id_idx ON nodra_routes (site_id);

ALTER TABLE nodra_deployments ADD COLUMN IF NOT EXISTS revision BIGINT NOT NULL DEFAULT 1;
ALTER TABLE nodra_deployments ADD COLUMN IF NOT EXISTS site_id TEXT NOT NULL DEFAULT '';
ALTER TABLE nodra_deployments ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ;
UPDATE nodra_deployments
SET site_id = COALESCE(data->>'site_id', '')
WHERE site_id = '' AND COALESCE(data->>'site_id', '') <> '';
UPDATE nodra_deployments
SET updated_at = (data->>'updated_at')::timestamptz
WHERE updated_at IS NULL
  AND COALESCE(data->>'updated_at', '') ~ '^[0-9]{4}-';
CREATE INDEX IF NOT EXISTS nodra_deployments_site_id_idx ON nodra_deployments (site_id);

ALTER TABLE nodra_policy_packs ADD COLUMN IF NOT EXISTS revision BIGINT NOT NULL DEFAULT 1;
ALTER TABLE nodra_policy_packs ADD COLUMN IF NOT EXISTS site_id TEXT NOT NULL DEFAULT '';
ALTER TABLE nodra_policy_packs ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ;
UPDATE nodra_policy_packs
SET site_id = COALESCE(data->>'site_id', '')
WHERE site_id = '' AND COALESCE(data->>'site_id', '') <> '';
UPDATE nodra_policy_packs
SET updated_at = (data->>'updated_at')::timestamptz
WHERE updated_at IS NULL
  AND COALESCE(data->>'updated_at', '') ~ '^[0-9]{4}-';
CREATE INDEX IF NOT EXISTS nodra_policy_packs_site_id_idx ON nodra_policy_packs (site_id);

ALTER TABLE nodra_ota_campaigns ADD COLUMN IF NOT EXISTS revision BIGINT NOT NULL DEFAULT 1;
ALTER TABLE nodra_ota_campaigns ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ;
UPDATE nodra_ota_campaigns
SET updated_at = (data->>'updated_at')::timestamptz
WHERE updated_at IS NULL
  AND COALESCE(data->>'updated_at', '') ~ '^[0-9]{4}-';

ALTER TABLE nodra_orgs ADD COLUMN IF NOT EXISTS revision BIGINT NOT NULL DEFAULT 1;

ALTER TABLE nodra_alerts ADD COLUMN IF NOT EXISTS revision BIGINT NOT NULL DEFAULT 1;
ALTER TABLE nodra_alerts ADD COLUMN IF NOT EXISTS site_id TEXT NOT NULL DEFAULT '';
UPDATE nodra_alerts
SET site_id = COALESCE(data->>'site_id', '')
WHERE site_id = '' AND COALESCE(data->>'site_id', '') <> '';
CREATE INDEX IF NOT EXISTS nodra_alerts_site_id_idx ON nodra_alerts (site_id);

ALTER TABLE nodra_events ADD COLUMN IF NOT EXISTS site_id TEXT NOT NULL DEFAULT '';
UPDATE nodra_events
SET site_id = COALESCE(data->>'site_id', '')
WHERE site_id = '' AND COALESCE(data->>'site_id', '') <> '';
CREATE INDEX IF NOT EXISTS nodra_events_site_id_idx ON nodra_events (site_id);
CREATE INDEX IF NOT EXISTS nodra_events_event_time_idx ON nodra_events (event_time);
