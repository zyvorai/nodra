-- Baseline schema. CREATE IF NOT EXISTS so a database created by an
-- older binary (which applied the same DDL at open) still adopts version 1.

CREATE TABLE IF NOT EXISTS nodra_sites (id TEXT PRIMARY KEY, data JSONB NOT NULL);
CREATE TABLE IF NOT EXISTS nodra_devices (id TEXT PRIMARY KEY, data JSONB NOT NULL);
CREATE TABLE IF NOT EXISTS nodra_twins (device_id TEXT PRIMARY KEY, data JSONB NOT NULL);
CREATE TABLE IF NOT EXISTS nodra_routes (id TEXT PRIMARY KEY, data JSONB NOT NULL);
CREATE TABLE IF NOT EXISTS nodra_deployments (id TEXT PRIMARY KEY, data JSONB NOT NULL);
CREATE TABLE IF NOT EXISTS nodra_policy_packs (id TEXT PRIMARY KEY, data JSONB NOT NULL);
CREATE TABLE IF NOT EXISTS nodra_ota_campaigns (id TEXT PRIMARY KEY, data JSONB NOT NULL);
CREATE TABLE IF NOT EXISTS nodra_orgs (id TEXT PRIMARY KEY, data JSONB NOT NULL);
CREATE TABLE IF NOT EXISTS nodra_alerts (id TEXT PRIMARY KEY, data JSONB NOT NULL);
CREATE TABLE IF NOT EXISTS nodra_events (id TEXT PRIMARY KEY, data JSONB NOT NULL, event_time TIMESTAMPTZ);
CREATE TABLE IF NOT EXISTS nodra_seen_events (id TEXT PRIMARY KEY);

CREATE TABLE IF NOT EXISTS nodra_deliveries (
    id TEXT PRIMARY KEY,
    seq BIGSERIAL,
    data JSONB NOT NULL,
    claimed_by TEXT,
    claimed_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS nodra_deliveries_seq_idx ON nodra_deliveries (seq);
ALTER TABLE nodra_deliveries ADD COLUMN IF NOT EXISTS claimed_by TEXT;
ALTER TABLE nodra_deliveries ADD COLUMN IF NOT EXISTS claimed_at TIMESTAMPTZ;

CREATE TABLE IF NOT EXISTS nodra_deadletters (
    id TEXT PRIMARY KEY,
    seq BIGSERIAL,
    data JSONB NOT NULL,
    claimed_by TEXT,
    claimed_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS nodra_deadletters_seq_idx ON nodra_deadletters (seq);
ALTER TABLE nodra_deadletters ADD COLUMN IF NOT EXISTS claimed_by TEXT;
ALTER TABLE nodra_deadletters ADD COLUMN IF NOT EXISTS claimed_at TIMESTAMPTZ;

CREATE TABLE IF NOT EXISTS nodra_audit_log (
    id TEXT PRIMARY KEY,
    ts TIMESTAMPTZ NOT NULL,
    actor TEXT,
    actor_type TEXT,
    action TEXT,
    target TEXT,
    site_id TEXT,
    result TEXT,
    message TEXT,
    detail JSONB
);
CREATE INDEX IF NOT EXISTS nodra_audit_log_ts_idx ON nodra_audit_log (ts DESC);
CREATE INDEX IF NOT EXISTS nodra_audit_log_site_idx ON nodra_audit_log (site_id, ts DESC);
