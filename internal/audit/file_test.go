// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package audit

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFileLogAppendAndQuery(t *testing.T) {
	f, err := OpenFile(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	base := time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC)
	for i, action := range []string{"login", "route.create", "site.revoke"} {
		e := Entry{Time: base.Add(time.Duration(i) * time.Minute), Actor: "admin", ActorType: "admin", Action: action, Message: action}
		if err := f.Append(e); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	if err := f.Ping(context.Background()); err != nil {
		t.Fatalf("ping: %v", err)
	}

	got, next, err := f.Query(Filter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if next != "" {
		t.Fatalf("expected no next cursor, got %q", next)
	}
	if len(got) != 3 {
		t.Fatalf("got %d entries, want 3", len(got))
	}
	// Descending by time: site.revoke, route.create, login.
	if got[0].Action != "site.revoke" || got[1].Action != "route.create" || got[2].Action != "login" {
		t.Fatalf("order=%v", got)
	}
}

func TestFileLogQueryFiltersAndPagination(t *testing.T) {
	f, err := OpenFile(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	base := time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		e := Entry{Time: base.Add(time.Duration(i) * time.Minute), SiteID: "site-a", Action: "event", Message: "e"}
		if err := f.Append(e); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Append(Entry{Time: base.Add(10 * time.Minute), SiteID: "site-b", Action: "event", Message: "other site"}); err != nil {
		t.Fatal(err)
	}

	bySite, _, err := f.Query(Filter{SiteID: "site-a", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(bySite) != 5 {
		t.Fatalf("site filter: got %d, want 5", len(bySite))
	}

	page1, cursor1, err := f.Query(Filter{SiteID: "site-a", Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(page1) != 2 || cursor1 == "" {
		t.Fatalf("page1=%d cursor=%q", len(page1), cursor1)
	}
	page2, cursor2, err := f.Query(Filter{SiteID: "site-a", Limit: 2, Cursor: cursor1})
	if err != nil {
		t.Fatal(err)
	}
	if len(page2) != 2 {
		t.Fatalf("page2=%d", len(page2))
	}
	page3, cursor3, err := f.Query(Filter{SiteID: "site-a", Limit: 2, Cursor: cursor2})
	if err != nil {
		t.Fatal(err)
	}
	if len(page3) != 1 || cursor3 != "" {
		t.Fatalf("page3=%d cursor=%q", len(page3), cursor3)
	}
	seen := map[string]bool{}
	for _, e := range append(append(page1, page2...), page3...) {
		if seen[e.ID] {
			t.Fatalf("duplicate entry across pages: %s", e.ID)
		}
		seen[e.ID] = true
	}
	if len(seen) != 5 {
		t.Fatalf("paged through %d distinct entries, want 5", len(seen))
	}
}

func TestFileLogRotatesByDay(t *testing.T) {
	dir := t.TempDir()
	f, err := OpenFile(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	day1 := time.Date(2026, 9, 14, 23, 59, 0, 0, time.UTC)
	day2 := time.Date(2026, 9, 15, 0, 1, 0, 0, time.UTC)
	if err := f.Append(Entry{Time: day1, Action: "a"}); err != nil {
		t.Fatal(err)
	}
	if err := f.Append(Entry{Time: day2, Action: "b"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "audit", "audit-20260914.log")); err != nil {
		t.Fatalf("day1 file missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "audit", "audit-20260915.log")); err != nil {
		t.Fatalf("day2 file missing: %v", err)
	}
}

func TestFileLogPruneKeepsRecentAndActiveDay(t *testing.T) {
	dir := t.TempDir()
	f, err := OpenFile(dir, Options{RetentionDays: 30})
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	old := time.Now().UTC().AddDate(0, 0, -100)
	recent := time.Now().UTC().AddDate(0, 0, -1)
	if err := f.Append(Entry{Time: old, Action: "old"}); err != nil {
		t.Fatal(err)
	}
	if err := f.Append(Entry{Time: recent, Action: "recent"}); err != nil {
		t.Fatal(err)
	}
	// Re-append to "today" so the active file (curDay) is not the old one,
	// otherwise Prune's active-file guard would (correctly) keep it anyway.
	if err := f.Append(Entry{Time: time.Now().UTC(), Action: "today"}); err != nil {
		t.Fatal(err)
	}

	cutoff := time.Now().UTC().AddDate(0, 0, -30)
	if err := f.Prune(cutoff); err != nil {
		t.Fatal(err)
	}
	got, _, err := f.Query(Filter{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range got {
		if e.Action == "old" {
			t.Fatal("expected old entry to be pruned")
		}
	}
	foundRecent := false
	for _, e := range got {
		if e.Action == "recent" {
			foundRecent = true
		}
	}
	if !foundRecent {
		t.Fatal("expected recent entry to survive prune")
	}
}
