// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"database/sql"
	"os"
	"strconv"
	"sync"
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
		t.Fatal("site missing after write")
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

func TestRejectNewerSchema(t *testing.T) {
	if err := rejectNewerSchema(3, 2); err == nil {
		t.Fatal("expected a newer schema to be refused")
	}
	if err := rejectNewerSchema(2, 2); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresRejectsNewerSchema(t *testing.T) {
	dsn := os.Getenv("NODRA_DATABASE_URL")
	if dsn == "" {
		t.Skip("NODRA_DATABASE_URL unset — optional Postgres fleet store")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM nodra_schema_migrations WHERE version = 9999`)
		_ = db.Close()
	})
	s, err := OpenPostgres(dsn)
	if err != nil {
		t.Fatalf("OpenPostgres: %v", err)
	}
	_ = s.Close()
	if _, err = db.Exec(`INSERT INTO nodra_schema_migrations (version) VALUES (9999) ON CONFLICT DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	if _, err = OpenPostgres(dsn); err == nil {
		t.Fatal("expected a newer schema version to refuse startup")
	}
}

func TestPostgresCrossReplicaConsistency(t *testing.T) {
	dsn := os.Getenv("NODRA_DATABASE_URL")
	if dsn == "" {
		t.Skip("NODRA_DATABASE_URL unset — optional Postgres fleet store")
	}
	a, err := OpenPostgres(dsn)
	if err != nil {
		t.Fatal(err)
	}
	b, err := OpenPostgres(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = a.Close()
		_ = b.Close()
	})

	ms, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	var schemaVersion int
	if err = a.db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM nodra_schema_migrations`).Scan(&schemaVersion); err != nil {
		t.Fatal(err)
	}
	if schemaVersion != maxMigrationVersion(ms) {
		t.Fatalf("schema version %d, binary max %d", schemaVersion, maxMigrationVersion(ms))
	}

	suffix := strconv.FormatInt(time.Now().UnixNano(), 10)
	siteID := "site_rep_" + suffix
	routeID := "rt_rep_" + suffix
	twinID := "twin_rep_" + suffix
	otherTwin := "twin_other_" + suffix
	orgID := "org_rep_" + suffix
	eventID := "evt_rep_" + suffix
	t.Cleanup(func() {
		_, _ = a.db.Exec(`DELETE FROM nodra_sites WHERE id = $1`, siteID)
		_, _ = a.db.Exec(`DELETE FROM nodra_routes WHERE id = $1`, routeID)
		_, _ = a.db.Exec(`DELETE FROM nodra_twins WHERE device_id IN ($1, $2)`, twinID, otherTwin)
		_, _ = a.db.Exec(`DELETE FROM nodra_orgs WHERE id = $1`, orgID)
		_, _ = a.db.Exec(`DELETE FROM nodra_events WHERE id = $1`, eventID)
		_, _ = a.db.Exec(`DELETE FROM nodra_seen_events WHERE id = $1`, eventID)
	})

	now := time.Now().UTC()
	if err = a.AddSite(model.Site{ID: siteID, Name: "replica-a", OrgID: orgID, Status: "online", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err = a.AddRoute(model.Route{ID: routeID, Name: "echo", SiteID: siteID, Topic: "t/#", TargetURL: "http://127.0.0.1:9/hook", Method: "POST", Enabled: true, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err = a.SetTwin(model.Twin{DeviceID: twinID, SiteID: siteID, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err = a.SetTwin(model.Twin{DeviceID: otherTwin, SiteID: "somewhere-else", UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err = a.AddOrg(model.Org{ID: orgID, Name: "tenant-a", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}

	site, ok := b.Site(siteID)
	if !ok || site.Name != "replica-a" || site.OrgID != orgID {
		t.Fatalf("replica B site: %+v ok=%v", site, ok)
	}
	if _, ok = b.Org(orgID); !ok {
		t.Fatal("replica B missing org")
	}
	routes := b.Routes()
	foundRoute := false
	for _, rt := range routes {
		if rt.ID == routeID {
			foundRoute = true
		}
	}
	if !foundRoute {
		t.Fatal("replica B missing route")
	}
	twins := b.TwinsForSite(siteID)
	if len(twins) != 1 || twins[0].DeviceID != twinID {
		t.Fatalf("site twins: %+v", twins)
	}

	const n = 16
	siteBefore, err := a.rowRevision("nodra_sites", "id", siteID)
	if err != nil {
		t.Fatal(err)
	}
	twinBefore, err := a.rowRevision("nodra_twins", "device_id", twinID)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errCh := make(chan error, n*2)
	for i := 0; i < n; i++ {
		wg.Add(2)
		key := strconv.Itoa(i)
		go func() {
			defer wg.Done()
			errCh <- a.UpdateSite(siteID, func(v *model.Site) {
				if v.Metadata == nil {
					v.Metadata = map[string]string{}
				}
				v.Metadata[key] = "set"
			})
		}()
		go func() {
			defer wg.Done()
			errCh <- b.UpdateTwin(twinID, func(tw *model.Twin) {
				if tw.Desired == nil {
					tw.Desired = map[string]any{}
				}
				tw.Desired[key] = true
				tw.DesiredVersion++
				tw.UpdatedAt = time.Now().UTC()
			})
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}

	siteAfter, err := b.rowRevision("nodra_sites", "id", siteID)
	if err != nil {
		t.Fatal(err)
	}
	if siteAfter-siteBefore != n {
		t.Fatalf("site revision delta %d, want %d", siteAfter-siteBefore, n)
	}
	twinAfter, err := a.rowRevision("nodra_twins", "device_id", twinID)
	if err != nil {
		t.Fatal(err)
	}
	if twinAfter-twinBefore != n {
		t.Fatalf("twin revision delta %d, want %d", twinAfter-twinBefore, n)
	}
	site, ok = b.Site(siteID)
	if !ok || len(site.Metadata) != n {
		t.Fatalf("site metadata: %+v", site.Metadata)
	}
	tw, ok := a.Twin(twinID)
	if !ok || tw.DesiredVersion != n || len(tw.Desired) != n {
		t.Fatalf("twin: version %d desired %v", tw.DesiredVersion, tw.Desired)
	}

	c, err := OpenPostgres(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	site, ok = c.Site(siteID)
	if !ok || site.Name != "replica-a" || len(site.Metadata) != n {
		t.Fatalf("third replica site: %+v", site)
	}
	if _, ok = c.Org(orgID); !ok {
		t.Fatal("third replica missing org")
	}
	tw, ok = c.Twin(twinID)
	if !ok || tw.DesiredVersion != n {
		t.Fatalf("third replica twin: %+v", tw)
	}

	ev := model.Event{ID: eventID, SiteID: siteID, Topic: "t/1", Payload: []byte(`{"n":1}`), EventTime: now}
	if err = a.AddEvent(ev); err != nil {
		t.Fatal(err)
	}
	if !b.HasEvent(eventID) {
		t.Fatal("replica B did not see the event")
	}
	if err = b.AddEvent(ev); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = c.db.QueryRow(`SELECT count(*) FROM nodra_events WHERE id = $1`, eventID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("event rows %d, want 1", count)
	}
	got := c.EventsSince(now.Add(-time.Second))
	seen := false
	for _, e := range got {
		if e.ID == eventID {
			seen = true
		}
	}
	if !seen {
		t.Fatal("third replica EventsSince missed the event")
	}
}
