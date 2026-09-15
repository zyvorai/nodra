// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package queue

import (
	"fmt"
	"os"
	"testing"
	"time"
)

type testItem struct {
	Value string `json:"value"`
}

func TestPostgresQueueRoundTrip(t *testing.T) {
	dsn := os.Getenv("NODRA_DATABASE_URL")
	if dsn == "" {
		t.Skip("NODRA_DATABASE_URL unset — optional Postgres delivery queue")
	}
	table := fmt.Sprintf("nodra_test_queue_%d", time.Now().UnixNano())

	q, err := OpenPostgres[testItem](dsn, table, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = q.db.Exec("DROP TABLE IF EXISTS " + table)
		q.Close()
	}()

	if err := q.Put("a", testItem{Value: "1"}); err != nil {
		t.Fatal(err)
	}
	if err := q.Put("b", testItem{Value: "2"}); err != nil {
		t.Fatal(err)
	}
	v, ok := q.Get("a")
	if !ok || v.Value != "1" {
		t.Fatalf("Get(a)=%v ok=%v", v, ok)
	}
	list, err := q.List()
	if err != nil || len(list) != 2 {
		t.Fatalf("List()=%v err=%v", list, err)
	}
	st := q.Stats()
	if st.Items != 2 {
		t.Fatalf("Stats().Items=%d", st.Items)
	}
	if err := q.Delete("a"); err != nil {
		t.Fatal(err)
	}
	if _, ok := q.Get("a"); ok {
		t.Fatal("a should be deleted")
	}

	// Re-put an existing id: it should move to the back of FIFO order.
	if err := q.Put("b", testItem{Value: "2-updated"}); err != nil {
		t.Fatal(err)
	}
	if err := q.Put("c", testItem{Value: "3"}); err != nil {
		t.Fatal(err)
	}
	list, err = q.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Value != "2-updated" || list[1].Value != "3" {
		t.Fatalf("expected re-put b to sort before newly-added c: %v", list)
	}

	// Durability: reopening against the same table must see the same data.
	q2, err := OpenPostgres[testItem](dsn, table, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer q2.Close()
	list2, err := q2.List()
	if err != nil || len(list2) != 2 {
		t.Fatalf("reopened queue List()=%v err=%v", list2, err)
	}

	// The actual point of this feature: two independent instances against
	// the same table/DSN must see each other's writes.
	if err := q.Put("shared", testItem{Value: "visible-to-both"}); err != nil {
		t.Fatal(err)
	}
	v, ok = q2.Get("shared")
	if !ok || v.Value != "visible-to-both" {
		t.Fatalf("instance B did not see instance A's Put: v=%v ok=%v", v, ok)
	}
}
