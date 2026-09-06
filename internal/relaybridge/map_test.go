// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package relaybridge

import (
	"encoding/json"
	"testing"
	"time"
)

func TestMapDelivery(t *testing.T) {
	d := NodraDelivery{
		EventID:    "edge_1",
		SiteID:     "site_a",
		Topic:      "factory/line-1/alert",
		Payload:    json.RawMessage(`{"c":31.2,"severity":"high","zone_id":"z1"}`),
		Headers:    map[string]string{"x-source": "plc"},
		DeliveryID: "dlv_abc",
		EventTime:  time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC),
	}
	ev := MapDelivery(d)
	if ev.Type != "factory.line-1.alert" {
		t.Fatalf("type=%q", ev.Type)
	}
	if ev.IdempotencyKey != "dlv_abc" {
		t.Fatalf("idem=%q", ev.IdempotencyKey)
	}
	if ev.Source != "nodra" || ev.Severity != "high" {
		t.Fatalf("source/severity=%s/%s", ev.Source, ev.Severity)
	}
	if ev.Data["site_id"] != "site_a" || ev.Data["zone_id"] != "z1" {
		t.Fatalf("data=%v", ev.Data)
	}
}

func TestMapDeliveryTypeHeader(t *testing.T) {
	ev := MapDelivery(NodraDelivery{
		EventID: "e1",
		Topic:   "ignored/topic",
		Headers: map[string]string{"x-nodra-relay-type": "irrigation.required", "x-nodra-relay-severity": "critical"},
		Payload: json.RawMessage(`{}`),
	})
	if ev.Type != "irrigation.required" || ev.Severity != "critical" || ev.IdempotencyKey != "e1" {
		t.Fatalf("%+v", ev)
	}
}

func TestTopicToType(t *testing.T) {
	if got := topicToType("a/b/c"); got != "a.b.c" {
		t.Fatal(got)
	}
}
