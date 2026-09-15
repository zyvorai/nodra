// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package queue

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// QueueLike is the subset of *Queue[T]'s method set the control plane
// depends on. *Queue[T] (file WAL) and *PostgresQueue[T] both satisfy it, so
// callers can swap storage without caring which is in use.
type QueueLike[T any] interface {
	Put(id string, v T) error
	Delete(id string) error
	Get(id string) (T, bool)
	List() ([]T, error)
	Len() int
	Stats() Stats
	Close() error
}

// PostgresQueue is a Postgres-backed alternative to the file-WAL Queue[T],
// letting every replica pointed at the same database see the same delivery/
// DLQ state — unlike Queue[T], which is single-process by construction (no
// cross-process file lock). Query is not cached in memory: every call hits
// Postgres directly, since this is meant to be shared across replicas, not
// a fast local cache.
//
// v1 only supports "reject" byte/item-cap semantics: the check is a
// best-effort COUNT/SUM done before the insert, not atomic against a
// concurrent Put from another replica — a soft backpressure guard, not a
// hard multi-writer invariant. drop-oldest/drop-newest aren't implemented
// here (the file Queue[T]'s eviction logic assumes it's the only writer,
// which isn't true across replicas).
type PostgresQueue[T any] struct {
	db    *sql.DB
	table string
	opts  Options
}

func pgQueueSchema(table string) string {
	return fmt.Sprintf(`
CREATE TABLE IF NOT EXISTS %s (
	id TEXT PRIMARY KEY,
	seq BIGSERIAL,
	data JSONB NOT NULL
);
CREATE INDEX IF NOT EXISTS %s_seq_idx ON %s (seq);
`, table, table, table)
}

// OpenPostgres opens (creating if needed) a Postgres-backed queue at table.
// table must be a trusted, code-controlled identifier (e.g. "nodra_deliveries")
// — it is interpolated into DDL/DML, never taken from user input. opts only
// supports Policy=="reject" (or "", defaulted to "reject") — see the type
// doc comment for why drop-oldest/drop-newest aren't offered here.
func OpenPostgres[T any](databaseURL, table string, opts Options) (*PostgresQueue[T], error) {
	if databaseURL == "" {
		return nil, errors.New("NODRA_DATABASE_URL is required for a postgres queue")
	}
	if table == "" {
		return nil, errors.New("postgres queue table name is required")
	}
	if opts.Policy == "" {
		opts.Policy = "reject"
	}
	if opts.Policy != "reject" {
		return nil, fmt.Errorf("postgres queue only supports the %q policy, got %q", "reject", opts.Policy)
	}
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(16)
	db.SetConnMaxLifetime(30 * time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err = db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("postgres queue ping: %w", err)
	}
	if _, err = db.ExecContext(ctx, pgQueueSchema(table)); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("postgres queue migrate: %w", err)
	}
	return &PostgresQueue[T]{db: db, table: table, opts: opts}, nil
}

func (q *PostgresQueue[T]) Put(id string, v T) error {
	if id == "" {
		return errors.New("queue id is required")
	}
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if q.opts.MaxItems > 0 || q.opts.MaxBytes > 0 {
		st := q.Stats()
		_, exists := q.Get(id)
		addItems, addBytes := 1, int64(len(data))
		if exists {
			addItems = 0
		}
		if (q.opts.MaxItems > 0 && st.Items+addItems > q.opts.MaxItems) ||
			(q.opts.MaxBytes > 0 && st.Bytes+addBytes > q.opts.MaxBytes) {
			return ErrFull
		}
	}
	// seq is bumped on every Put, including re-puts of an existing id, so a
	// retried/rescheduled delivery moves to the back of FIFO order — matching
	// Queue[T].Put's behavior (it always increments q.seq before appending).
	query := fmt.Sprintf(`INSERT INTO %s (id, seq, data) VALUES ($1, nextval(pg_get_serial_sequence('%s','seq')), $2)
		ON CONFLICT (id) DO UPDATE SET seq = nextval(pg_get_serial_sequence('%s','seq')), data = EXCLUDED.data`,
		q.table, q.table, q.table)
	_, err = q.db.Exec(query, id, data)
	return err
}
func (q *PostgresQueue[T]) Delete(id string) error {
	_, err := q.db.Exec(fmt.Sprintf(`DELETE FROM %s WHERE id = $1`, q.table), id)
	return err
}
func (q *PostgresQueue[T]) Get(id string) (T, bool) {
	var v T
	var data []byte
	row := q.db.QueryRow(fmt.Sprintf(`SELECT data FROM %s WHERE id = $1`, q.table), id)
	if err := row.Scan(&data); err != nil {
		return v, false
	}
	if err := json.Unmarshal(data, &v); err != nil {
		return v, false
	}
	return v, true
}
func (q *PostgresQueue[T]) List() ([]T, error) {
	rows, err := q.db.Query(fmt.Sprintf(`SELECT data FROM %s ORDER BY seq ASC`, q.table))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []T
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var v T
		if err := json.Unmarshal(data, &v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (q *PostgresQueue[T]) Len() int { return q.Stats().Items }
func (q *PostgresQueue[T]) Stats() Stats {
	var items int
	var bytes sql.NullInt64
	row := q.db.QueryRow(fmt.Sprintf(`SELECT count(*), sum(pg_column_size(data)) FROM %s`, q.table))
	_ = row.Scan(&items, &bytes)
	return Stats{Items: items, Bytes: bytes.Int64, MaxItems: q.opts.MaxItems, MaxBytes: q.opts.MaxBytes, Policy: q.opts.Policy}
}
func (q *PostgresQueue[T]) Close() error { return q.db.Close() }
