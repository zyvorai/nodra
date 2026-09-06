// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package relaybridge

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPublishDirect(t *testing.T) {
	var got AuthCheck
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.Method = r.Method
		got.Path = r.URL.Path
		got.Auth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		got.Body = string(b)
		if r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "unauthorized", 401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"event":{"id":"evt_99"}}`))
	}))
	t.Cleanup(ts.Close)

	c := &Client{RelayBase: ts.URL, RelayToken: "tok", HTTP: ts.Client()}
	res, err := c.Publish(RelayEvent{
		Type: "factory.alert", Severity: "high", Source: "nodra",
		IdempotencyKey: "dlv_1", Data: map[string]any{"site_id": "s1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Path != "relay" || res.EventID != "evt_99" {
		t.Fatalf("%+v", res)
	}
	if got.Method != "POST" || got.Path != "/v1/events" || got.Auth != "Bearer tok" {
		t.Fatalf("%+v", got)
	}
	var body map[string]any
	_ = json.Unmarshal([]byte(got.Body), &body)
	if body["type"] != "factory.alert" || body["idempotency_key"] != "dlv_1" {
		t.Fatalf("%s", got.Body)
	}
}

func TestPublishDirectUnauthorized(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", 401)
	}))
	t.Cleanup(ts.Close)
	c := &Client{RelayBase: ts.URL, RelayToken: "bad", HTTP: ts.Client()}
	_, err := c.Publish(RelayEvent{Type: "x", IdempotencyKey: "k", Source: "nodra", Severity: "info", Data: map[string]any{}})
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("want 401 error, got %v", err)
	}
}

func TestPublishGateway(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/projects/fasal-onprem/topics/factory.alert", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			w.WriteHeader(200)
			return
		}
		http.NotFound(w, r)
	})
	mux.HandleFunc("/v1/projects/fasal-onprem/topics/factory.alert:publish", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"messageIds":["1"]}`))
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	c := &Client{GatewayBase: ts.URL, Project: "fasal-onprem", HTTP: ts.Client()}
	res, err := c.Publish(RelayEvent{
		Type: "factory.alert", Severity: "medium", Source: "nodra",
		IdempotencyKey: "k1", Data: map[string]any{"ok": true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Path != "gateway" {
		t.Fatalf("%+v", res)
	}
}

type AuthCheck struct {
	Method, Path, Auth, Body string
}
