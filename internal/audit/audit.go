// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

// Package audit is a durable, exportable audit trail for control-plane and
// edge admin/agent actions. It is deliberately not part of store.Backend:
// Backend's file and Postgres implementations both load their entire
// contents into memory at open time, which is fine for bounded fleet state
// (sites/routes/twins) but wrong for an unbounded, ever-growing history.
package audit

import (
	"context"
	"fmt"
	"time"

	"github.com/zyvorai/nodra/internal/auth"
)

// Entry is one durable audit record.
type Entry struct {
	ID        string         `json:"id"`
	Time      time.Time      `json:"time"`
	Actor     string         `json:"actor,omitempty"`      // authenticated principal: admin/viewer username, site ID, or "system"
	ActorType string         `json:"actor_type,omitempty"` // admin|viewer|agent|system
	Action    string         `json:"action"`
	Target    string         `json:"target,omitempty"`
	SiteID    string         `json:"site_id,omitempty"`
	Result    string         `json:"result,omitempty"` // ok|error|denied
	Message   string         `json:"message"`
	Detail    map[string]any `json:"detail,omitempty"`
}

// Filter selects a page of entries for Query.
type Filter struct {
	Since   time.Time
	Until   time.Time
	SiteID  string
	SiteIDs []string // when set, match any of these site_ids (org-scoped pushdown)
	Action  string
	Actor   string
	Limit   int
	Cursor  string
}

// Store is the audit-log persistence surface. Query is an admin/export path,
// not the hot ingest path, so implementations may scan rather than index.
type Store interface {
	Append(Entry) error
	Query(Filter) (entries []Entry, nextCursor string, err error)
	Prune(before time.Time) error
	Ping(ctx context.Context) error
	Close() error
}

// Options configures retention. Zero RetentionDays means "use the default".
type Options struct {
	RetentionDays int
}

func (o Options) withDefaults() Options {
	if o.RetentionDays <= 0 {
		o.RetentionDays = 90
	}
	return o
}

// Open opens the configured audit store. driver: "file" (default) or "postgres".
func Open(driver, dataDir, databaseURL string, opts Options) (Store, error) {
	switch driver {
	case "", "file":
		return OpenFile(dataDir, opts)
	case "postgres":
		return OpenPostgres(databaseURL, opts)
	default:
		return nil, fmt.Errorf("unsupported audit driver %q", driver)
	}
}

func newID() string {
	t, _ := auth.NewToken(8)
	return "aud_" + t
}

func encodeCursor(t time.Time, id string) string {
	return fmt.Sprintf("%d|%s", t.UnixNano(), id)
}

func decodeCursor(c string) (time.Time, string, bool) {
	var ns int64
	var id string
	n, err := fmt.Sscanf(c, "%d|%s", &ns, &id)
	if err != nil || n != 2 {
		return time.Time{}, "", false
	}
	return time.Unix(0, ns).UTC(), id, true
}
