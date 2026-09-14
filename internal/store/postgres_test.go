// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/zyvorai/nodra/internal/model"
)

func TestPostgresRoundTrip(t *testing.T) {
	dsn := os.Getenv("NODRA_DATABASE_URL")
	if dsn == "" {
		t.Skip("NODRA_DATABASE_URL unset — optional Postgres fleet store")
	}
	s, err := OpenPostgres(dsn)
	if err != nil {
		t.Fatalf("OpenPostgres: %v", err)
	}
	defer s.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.Ping(ctx); err != nil {
		t.Fatalf("Ping: %v", err)
	}

	id := "site_pg_" + time.Now().UTC().Format("150405.000")
	if err := s.AddSite(model.Site{ID: id, Name: "pg-test", Status: "online", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("AddSite: %v", err)
	}
	if _, ok := s.Site(id); !ok {
		t.Fatal("site missing in memory index")
	}

	routeID := "rt_pg_" + id
	if err := s.AddRoute(model.Route{
		ID: routeID, Name: "echo", Topic: "t/#", TargetURL: "http://127.0.0.1:9/hook",
		Method: "POST", Enabled: true, RetryMax: 3, TimeoutSecs: 5, CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("AddRoute: %v", err)
	}
	if err := s.DeleteRoute(routeID); err != nil {
		t.Fatalf("DeleteRoute: %v", err)
	}

	// Re-open to prove durability.
	_ = s.Close()
	s2, err := OpenPostgres(dsn)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	if _, ok := s2.Site(id); !ok {
		t.Fatal("site missing after reopen")
	}
}
