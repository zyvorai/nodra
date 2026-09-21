// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

const pgAuditSchema = `
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
`

// PostgresLog persists audit entries directly in Postgres and queries them
// with real SQL. Fleet state in store.PostgresStore is also read from SQL
// on every call; an audit trail stays in its own table because it is an
// append-only log rather than fleet documents.
type PostgresLog struct {
	db   *sql.DB
	opts Options
}

func OpenPostgres(databaseURL string, opts Options) (*PostgresLog, error) {
	if databaseURL == "" {
		return nil, errors.New("NODRA_DATABASE_URL is required for postgres audit log")
	}
	opts = opts.withDefaults()
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(8)
	db.SetConnMaxLifetime(30 * time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err = db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("postgres audit ping: %w", err)
	}
	if _, err = db.ExecContext(ctx, pgAuditSchema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("postgres audit migrate: %w", err)
	}
	return &PostgresLog{db: db, opts: opts}, nil
}

func (p *PostgresLog) Append(e Entry) error {
	if e.ID == "" {
		e.ID = newID()
	}
	if e.Time.IsZero() {
		e.Time = time.Now().UTC()
	}
	detail, err := json.Marshal(e.Detail)
	if err != nil {
		return err
	}
	_, err = p.db.Exec(`INSERT INTO nodra_audit_log (id, ts, actor, actor_type, action, target, site_id, result, message, detail)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		ON CONFLICT (id) DO NOTHING`,
		e.ID, e.Time, e.Actor, e.ActorType, e.Action, e.Target, e.SiteID, e.Result, e.Message, detail)
	return err
}

func (p *PostgresLog) Query(filter Filter) ([]Entry, string, error) {
	limit := filter.Limit
	if limit <= 0 || limit > 1000 {
		limit = 250
	}
	var clauses []string
	var args []any
	n := 1
	add := func(clause string, val any) {
		clauses = append(clauses, fmt.Sprintf(clause, n))
		args = append(args, val)
		n++
	}
	if !filter.Since.IsZero() {
		add("ts >= $%d", filter.Since)
	}
	if !filter.Until.IsZero() {
		add("ts <= $%d", filter.Until)
	}
	if filter.SiteID != "" {
		add("site_id = $%d", filter.SiteID)
	}
	if filter.Action != "" {
		add("action = $%d", filter.Action)
	}
	if filter.Actor != "" {
		add("actor = $%d", filter.Actor)
	}
	if filter.Cursor != "" {
		if ct, cid, ok := decodeCursor(filter.Cursor); ok {
			clauses = append(clauses, fmt.Sprintf("(ts, id) < ($%d, $%d)", n, n+1))
			args = append(args, ct, cid)
			n += 2
		}
	}
	q := `SELECT id, ts, actor, actor_type, action, target, site_id, result, message, detail FROM nodra_audit_log`
	if len(clauses) > 0 {
		q += " WHERE " + strings.Join(clauses, " AND ")
	}
	q += fmt.Sprintf(" ORDER BY ts DESC, id DESC LIMIT $%d", n)
	args = append(args, limit+1)

	rows, err := p.db.Query(q, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	var out []Entry
	for rows.Next() {
		var e Entry
		var detail []byte
		if err = rows.Scan(&e.ID, &e.Time, &e.Actor, &e.ActorType, &e.Action, &e.Target, &e.SiteID, &e.Result, &e.Message, &detail); err != nil {
			return nil, "", err
		}
		if len(detail) > 0 {
			_ = json.Unmarshal(detail, &e.Detail)
		}
		out = append(out, e)
	}
	if err = rows.Err(); err != nil {
		return nil, "", err
	}
	var next string
	if len(out) > limit {
		next = encodeCursor(out[limit-1].Time, out[limit-1].ID)
		out = out[:limit]
	}
	return out, next, nil
}

func (p *PostgresLog) Prune(before time.Time) error {
	_, err := p.db.Exec(`DELETE FROM nodra_audit_log WHERE ts < $1`, before)
	return err
}

func (p *PostgresLog) Ping(ctx context.Context) error {
	if p.db == nil {
		return errors.New("postgres audit log is closed")
	}
	return p.db.PingContext(ctx)
}

func (p *PostgresLog) Close() error {
	if p.db != nil {
		return p.db.Close()
	}
	return nil
}
