-- Console sessions and custom roles shared across control-plane replicas.
-- File mode keeps sessions.json / roles.json; Postgres mode uses these tables.

CREATE TABLE IF NOT EXISTS nodra_console_sessions (
	token TEXT PRIMARY KEY,
	role TEXT NOT NULL,
	actor TEXT NOT NULL,
	org_id TEXT NOT NULL DEFAULT '',
	expires_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS nodra_console_sessions_exp_idx ON nodra_console_sessions (expires_at);

CREATE TABLE IF NOT EXISTS nodra_custom_roles (
	name TEXT PRIMARY KEY,
	token_hash TEXT NOT NULL,
	write_access BOOLEAN NOT NULL DEFAULT false,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS nodra_custom_roles_token_hash_idx ON nodra_custom_roles (token_hash);
