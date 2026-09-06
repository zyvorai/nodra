// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package queue

import "testing"

type item struct {
	ID string `json:"id"`
	N  int    `json:"n"`
}

func TestWALQueuePersistenceAndOrder(t *testing.T) {
	d := t.TempDir()
	q, err := Open[item](d)
	if err != nil {
		t.Fatal(err)
	}
	_ = q.Put("b", item{"b", 2})
	_ = q.Put("a", item{"a", 1})
	_ = q.Close()
	q2, err := Open[item](d)
	if err != nil {
		t.Fatal(err)
	}
	defer q2.Close()
	v, _ := q2.List()
	if len(v) != 2 || v[0].ID != "b" || v[1].ID != "a" {
		t.Fatalf("order=%#v", v)
	}
	_ = q2.Put("b", item{"b", 3})
	v, _ = q2.List()
	if len(v) != 2 || v[1].ID != "b" || v[1].N != 3 {
		t.Fatalf("replace=%#v", v)
	}
	_ = q2.Delete("a")
	if q2.Len() != 1 {
		t.Fatalf("len=%d", q2.Len())
	}
}
func TestQueueRejectQuota(t *testing.T) {
	q, _ := OpenWithOptions[item](t.TempDir(), Options{MaxItems: 1, Policy: "reject"})
	defer q.Close()
	if err := q.Put("1", item{"1", 1}); err != nil {
		t.Fatal(err)
	}
	if err := q.Put("2", item{"2", 2}); err != ErrFull {
		t.Fatalf("want ErrFull got %v", err)
	}
}
func TestQueueDropOldest(t *testing.T) {
	q, _ := OpenWithOptions[item](t.TempDir(), Options{MaxItems: 2, Policy: "drop-oldest"})
	defer q.Close()
	_ = q.Put("1", item{"1", 1})
	_ = q.Put("2", item{"2", 2})
	_ = q.Put("3", item{"3", 3})
	v, _ := q.List()
	if len(v) != 2 || v[0].ID != "2" || v[1].ID != "3" {
		t.Fatalf("%#v", v)
	}
}
func TestQueueByteQuota(t *testing.T) {
	q, _ := OpenWithOptions[item](t.TempDir(), Options{MaxBytes: 10, Policy: "reject"})
	defer q.Close()
	if err := q.Put("x", item{"abcdef", 1}); err != ErrFull {
		t.Fatalf("want full got %v", err)
	}
}
