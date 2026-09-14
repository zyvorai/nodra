// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package audit

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestPostgresAuditRoundTrip(t *testing.T) {
	dsn := os.Getenv("NODRA_DATABASE_URL")
	if dsn == "" {
		t.Skip("NODRA_DATABASE_URL unset — optional Postgres audit log")
	}
	p, err := OpenPostgres(dsn, Options{})
	if err != nil {
		t.Fatalf("OpenPostgres: %v", err)
	}
	defer p.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := p.Ping(ctx); err != nil {
		t.Fatalf("Ping: %v", err)
	}

	siteID := "site_pg_audit_" + time.Now().UTC().Format("150405.000")
	base := time.Now().UTC()
	entries := []Entry{
		{Time: base, SiteID: siteID, Actor: "admin", Action: "login", Message: "ok"},
		{Time: base.Add(time.Second), SiteID: siteID, Actor: "admin", Action: "route.create", Message: "created route"},
	}
	for _, e := range entries {
		if err := p.Append(e); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	got, _, err := p.Query(Filter{SiteID: siteID, Limit: 10})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d entries, want 2", len(got))
	}
	if got[0].Action != "route.create" || got[1].Action != "login" {
		t.Fatalf("unexpected order: %+v", got)
	}

	if err := p.Prune(base.Add(24 * time.Hour)); err != nil {
		t.Fatalf("Prune: %v", err)
	}
	got, _, err = p.Query(Filter{SiteID: siteID, Limit: 10})
	if err != nil {
		t.Fatalf("Query after prune: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected prune to remove entries older than cutoff, got %d", len(got))
	}
}
