// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

// Package leader decides which of possibly several control-plane replicas
// is allowed to run a given periodic job right now. It is deliberately
// separate from any storage concern — an Elector only ever gates a call,
// it never touches queue/store data itself — mirroring how store.Backend
// and the MQTT broker are already separate concerns in this codebase.
package leader

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// Elector reports whether the caller may act as leader right now.
// TryAcquire must never block waiting on another holder — a caller on a
// periodic tick should skip this tick, not stall it, when it isn't leader.
type Elector interface {
	TryAcquire(ctx context.Context) (bool, error)
	Close() error
}

// AlwaysLeader is used when there is only ever one process by construction
// (the file-WAL storage backend, which has no cross-process coordination at
// all) — no election is needed.
type AlwaysLeader struct{}

func (AlwaysLeader) TryAcquire(context.Context) (bool, error) { return true, nil }
func (AlwaysLeader) Close() error                             { return nil }

// PostgresLock is a session-scoped pg_try_advisory_lock bound to one
// dedicated *sql.Conn (not the general pool — a session-scoped advisory
// lock is only meaningful tied to a single, specific backend session). If
// this process dies or the connection drops, Postgres releases the lock
// automatically, so another replica's next TryAcquire wins it. This is
// single-active-writer with automatic failover, not a distributed
// consensus protocol: there is a short window around handover where two
// replicas could both believe they're briefly not-leader/leader.
type PostgresLock struct {
	db  *sql.DB
	key int64
	mu  sync.Mutex
	// conn is the dedicated backend connection currently holding (or last
	// attempting to hold) the lock; nil when not held.
	conn *sql.Conn
}

func NewPostgresLock(databaseURL string, key int64) (*PostgresLock, error) {
	if databaseURL == "" {
		return nil, errors.New("NODRA_DATABASE_URL is required for a postgres leader lock")
	}
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err = db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("postgres leader lock ping: %w", err)
	}
	return &PostgresLock{db: db, key: key}, nil
}

// TryAcquire is non-blocking: it never waits on another holder.
func (l *PostgresLock) TryAcquire(ctx context.Context) (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.conn == nil {
		conn, err := l.db.Conn(ctx)
		if err != nil {
			return false, err
		}
		l.conn = conn
	}
	var held bool
	err := l.conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock($1)`, l.key).Scan(&held)
	if err != nil {
		// The dedicated conn errored (e.g. network drop) — drop it so the
		// next TryAcquire opens a fresh one. Postgres releases any lock this
		// session held the moment the underlying backend connection closes.
		_ = l.conn.Close()
		l.conn = nil
		return false, err
	}
	return held, nil
}

func (l *PostgresLock) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.conn != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, _ = l.conn.ExecContext(ctx, `SELECT pg_advisory_unlock($1)`, l.key)
		cancel()
		_ = l.conn.Close()
		l.conn = nil
	}
	return l.db.Close()
}
