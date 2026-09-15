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

// TestPostgresQueueClaimEnablesMultiWriterProcessing is the actual point of
// multi-writer HA: two independent replicas racing to claim the same item
// must never both win, a lost/expired claim must become reclaimable rather
// than stuck forever, and Put must reset an item back to unclaimed so a
// rescheduled/retried delivery isn't permanently stuck under a stale claim.
func TestPostgresQueueClaimEnablesMultiWriterProcessing(t *testing.T) {
	dsn := os.Getenv("NODRA_DATABASE_URL")
	if dsn == "" {
		t.Skip("NODRA_DATABASE_URL unset — optional Postgres delivery queue multi-writer claiming")
	}
	table := fmt.Sprintf("nodra_test_claim_%d", time.Now().UnixNano())

	replicaA, err := OpenPostgres[testItem](dsn, table, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = replicaA.db.Exec("DROP TABLE IF EXISTS " + table)
		replicaA.Close()
	}()
	replicaB, err := OpenPostgres[testItem](dsn, table, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer replicaB.Close()

	if err := replicaA.Put("x", testItem{Value: "1"}); err != nil {
		t.Fatal(err)
	}

	ok, err := replicaA.TryClaim("x", "replica-a", time.Minute)
	if err != nil || !ok {
		t.Fatalf("replica A should win the claim: ok=%v err=%v", ok, err)
	}
	ok, err = replicaB.TryClaim("x", "replica-b", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("replica B must not win a claim replica A already holds")
	}

	// The lease argument is "how stale must the *existing* claim be before
	// I'll steal it" — every replica in a real fleet is configured with the
	// same lease duration, so both sides of a reclaim check use the same
	// value here too (a short one, so the test doesn't need to sleep long).
	const lease = 20 * time.Millisecond
	if err := replicaA.ReleaseClaim("x"); err != nil {
		t.Fatal(err)
	}
	ok, err = replicaA.TryClaim("x", "replica-a", lease)
	if err != nil || !ok {
		t.Fatalf("replica A should reclaim after release: ok=%v err=%v", ok, err)
	}
	time.Sleep(3 * lease)
	ok, err = replicaB.TryClaim("x", "replica-b", lease)
	if err != nil || !ok {
		t.Fatalf("replica B should reclaim after replica A's lease expired: ok=%v err=%v", ok, err)
	}

	// Put (a reschedule after a failed delivery attempt) must clear any
	// existing claim, so the item isn't stuck for the full lease duration.
	if err := replicaA.Put("x", testItem{Value: "2-rescheduled"}); err != nil {
		t.Fatal(err)
	}
	ok, err = replicaA.TryClaim("x", "replica-a", time.Minute)
	if err != nil || !ok {
		t.Fatalf("Put should have cleared replica B's claim: ok=%v err=%v", ok, err)
	}

	// Claiming a nonexistent id is not an error, just a miss.
	ok, err = replicaA.TryClaim("does-not-exist", "replica-a", time.Minute)
	if err != nil || ok {
		t.Fatalf("claiming a nonexistent id: ok=%v err=%v", ok, err)
	}
}
