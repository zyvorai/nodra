// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package leader

import (
	"context"
	"os"
	"testing"
)

func TestAlwaysLeaderAlwaysHolds(t *testing.T) {
	var e Elector = AlwaysLeader{}
	held, err := e.TryAcquire(context.Background())
	if err != nil || !held {
		t.Fatalf("held=%v err=%v", held, err)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresLockContendsAndFailsOver(t *testing.T) {
	dsn := os.Getenv("NODRA_DATABASE_URL")
	if dsn == "" {
		t.Skip("NODRA_DATABASE_URL unset — optional Postgres leader lock")
	}
	const key int64 = 0x6e6f647261645f74 // arbitrary, test-only key
	ctx := context.Background()

	a, err := NewPostgresLock(dsn, key)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	held, err := a.TryAcquire(ctx)
	if err != nil || !held {
		t.Fatalf("lock A should acquire: held=%v err=%v", held, err)
	}

	b, err := NewPostgresLock(dsn, key)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	held, err = b.TryAcquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if held {
		t.Fatal("lock B should not acquire while A holds it")
	}

	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	held, err = b.TryAcquire(ctx)
	if err != nil || !held {
		t.Fatalf("lock B should acquire after A released: held=%v err=%v", held, err)
	}
}
