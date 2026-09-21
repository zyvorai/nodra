// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"testing"
)

func TestMigrationsLoadContiguous(t *testing.T) {
	ms, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) < 3 {
		t.Fatalf("expected at least migrations 1..3, got %d", len(ms))
	}
	for i, m := range ms {
		want := i + 1
		if m.version != want {
			t.Fatalf("migration[%d] version=%d want %d (must be contiguous from 1)", i, m.version, want)
		}
		if m.sql == "" {
			t.Fatalf("migration %d has empty SQL", m.version)
		}
	}
	if maxMigrationVersion(ms) != ms[len(ms)-1].version {
		t.Fatal("maxMigrationVersion mismatch")
	}
}
