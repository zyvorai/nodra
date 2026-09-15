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
ALTER TABLE %s ADD COLUMN IF NOT EXISTS claimed_by TEXT;
ALTER TABLE %s ADD COLUMN IF NOT EXISTS claimed_at TIMESTAMPTZ;
`, table, table, table, table, table)
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
	// claimed_by/claimed_at are cleared unconditionally: whoever calls Put
	// (a fresh insert, or a reschedule after a failed delivery attempt) is
	// declaring this record ready to be claimed again by any replica — a
	// stale claim from a previous attempt must never linger past its data.
	query := fmt.Sprintf(`INSERT INTO %s (id, seq, data) VALUES ($1, nextval(pg_get_serial_sequence('%s','seq')), $2)
		ON CONFLICT (id) DO UPDATE SET seq = nextval(pg_get_serial_sequence('%s','seq')), data = EXCLUDED.data, claimed_by = NULL, claimed_at = NULL`,
		q.table, q.table, q.table)
	_, err = q.db.Exec(query, id, data)
	return err
}

// Claimer is implemented by *PostgresQueue[T] (not by the file-WAL Queue[T],
// which is single-process by construction and never needs it) to let
// multiple replicas concurrently process the same shared queue: each item
// is claimed by exactly one replica at a time via an atomic conditional
// UPDATE, not gated behind a single elected leader — see internal/server's
// processDeliveries for how this replaces the old single-leader-lock gate
// specifically for delivery/DLQ processing (other singleton background
// tasks, like audit retention, are unaffected and stay leader-gated, since
// they don't need to scale and duplicate runs are merely wasteful, not
// unsafe).
type Claimer interface {
	// TryClaim atomically claims id for owner if it is unclaimed or its
	// existing claim's lease has expired (the claimant crashed or hung
	// without releasing it), returning true iff this call won the claim.
	// Never blocks waiting on another claimant.
	//
	// lease is evaluated against the EXISTING claim's age, not recorded
	// anywhere — every replica claiming against the same table must pass
	// the same configured lease duration, not an arbitrary per-call value,
	// or "how stale is stale enough to steal" becomes inconsistent between
	// callers.
	TryClaim(id, owner string, lease time.Duration) (bool, error)
	// ReleaseClaim clears id's claim (a no-op, not an error, if id no
	// longer exists — e.g. it was already deleted after a successful
	// delivery).
	ReleaseClaim(id string) error
}

func (q *PostgresQueue[T]) TryClaim(id, owner string, lease time.Duration) (bool, error) {
	if id == "" || owner == "" {
		return false, errors.New("queue id and owner are required")
	}
	// Fractional seconds, not truncated to int — a sub-second lease (useful
	// in tests, and for fast reclaim of a genuinely dead worker) must not
	// get floored to a whole second.
	leaseSeconds := lease.Seconds()
	if leaseSeconds <= 0 {
		leaseSeconds = 1
	}
	query := fmt.Sprintf(`UPDATE %s SET claimed_by = $1, claimed_at = now()
		WHERE id = $2 AND (claimed_by IS NULL OR claimed_at < now() - ($3 * interval '1 second'))
		RETURNING id`, q.table)
	row := q.db.QueryRow(query, owner, id, leaseSeconds)
	var got string
	if err := row.Scan(&got); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil // claimed elsewhere within its lease, or id no longer exists
		}
		return false, err
	}
	return true, nil
}

func (q *PostgresQueue[T]) ReleaseClaim(id string) error {
	_, err := q.db.Exec(fmt.Sprintf(`UPDATE %s SET claimed_by = NULL, claimed_at = NULL WHERE id = $1`, q.table), id)
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
