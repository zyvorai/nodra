// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/zyvorai/nodra/internal/server"
)

func TestAgentEnrollPublishFlush(t *testing.T) {
	srv, _ := server.New(server.Config{DataDir: t.TempDir(), AdminToken: "adm", EnrollmentToken: "enroll"})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	cfg := DefaultConfig()
	cfg.ServerURL = ts.URL
	cfg.SiteName = "edge-a"
	cfg.EnrollmentToken = "enroll"
	cfg.DataDir = filepath.Join(t.TempDir(), "agent")
	cfg.LocalToken = "local"
	path := filepath.Join(t.TempDir(), "nodrad.json")
	a, err := New(cfg, path)
	if err != nil {
		t.Fatal(err)
	}
	if err = a.EnsureEnrolled(context.Background()); err != nil {
		t.Fatal(err)
	}
	if a.SiteID() == "" {
		t.Fatal("not enrolled")
	}
	body := []byte(`{"topic":"sensors/temp","payload":{"c":24}}`)
	req := httptest.NewRequest("POST", "/v1/publish", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer local")
	rr := httptest.NewRecorder()
	a.Handler().ServeHTTP(rr, req)
	if rr.Code != 202 {
		t.Fatalf("publish=%d %s", rr.Code, rr.Body.String())
	}
	if a.Pending() != 1 {
		t.Fatalf("pending=%d", a.Pending())
	}
	a.FlushNow(context.Background())
	if a.Pending() != 0 {
		t.Fatalf("pending after flush=%d", a.Pending())
	}
	adminReq, _ := http.NewRequest("GET", ts.URL+"/api/v1/events", nil)
	adminReq.Header.Set("Authorization", "Bearer adm")
	resp, err := http.DefaultClient.Do(adminReq)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(b, []byte("sensors/temp")) {
		t.Fatalf("events=%s", b)
	}
}
func TestAgentOfflineQueueSurvives(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ServerURL = "http://127.0.0.1:1"
	cfg.SiteName = "offline"
	cfg.SiteID = "site-test"
	cfg.AgentToken = "agent-test"
	cfg.DataDir = t.TempDir()
	a, _ := New(cfg, "")
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/publish", bytes.NewBufferString(`{"topic":"offline/data","payload":{"x":1}}`))
	a.Handler().ServeHTTP(rr, req)
	if rr.Code != 202 {
		t.Fatal(rr.Code)
	}
	a.FlushNow(context.Background())
	if a.Pending() != 1 {
		t.Fatalf("offline event was lost: pending=%d", a.Pending())
	}
	a2, _ := New(cfg, "")
	if a2.Pending() != 1 {
		t.Fatalf("queue did not persist: %d", a2.Pending())
	}
}
func TestLocalTokenRequired(t *testing.T) {
	cfg := DefaultConfig()
	cfg.SiteID = "s"
	cfg.AgentToken = "t"
	cfg.DataDir = t.TempDir()
	cfg.LocalToken = "secret"
	a, _ := New(cfg, "")
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/publish", bytes.NewBufferString(`{"topic":"x","payload":1}`))
	a.Handler().ServeHTTP(rr, req)
	if rr.Code != 401 {
		t.Fatalf("want 401 got %d", rr.Code)
	}
}
func TestConfigRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "cfg.json")
	c := DefaultConfig()
	c.SiteName = "roundtrip"
	c.EnrollmentToken = "secret"
	if err := SaveConfig(p, c); err != nil {
		t.Fatal(err)
	}
	got, err := LoadConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	if got.SiteName != "roundtrip" || got.Heartbeat == 0 {
		t.Fatalf("got=%+v", got)
	}
	b, _ := json.Marshal(got)
	if len(b) == 0 {
		t.Fatal("empty marshal")
	}
}

func TestSpoolFlushOrderUsesAcceptanceOrder(t *testing.T) {
	cfg := DefaultConfig()
	cfg.SiteID = "s"
	cfg.AgentToken = "t"
	cfg.DataDir = t.TempDir()
	a, _ := New(cfg, "")
	for _, topic := range []string{"first", "second", "third"} {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/publish", bytes.NewBufferString(`{"topic":"`+topic+`","payload":1}`))
		a.Handler().ServeHTTP(rr, req)
		if rr.Code != 202 {
			t.Fatal(rr.Code)
		}
	}
	items, err := a.spool.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 || items[0].Topic != "first" || items[1].Topic != "second" || items[2].Topic != "third" {
		t.Fatalf("order=%v", items)
	}
}

func TestSpoolQuotaRejectsWithoutSilentLoss(t *testing.T) {
	cfg := DefaultConfig()
	cfg.SiteID = "s"
	cfg.AgentToken = "t"
	cfg.DataDir = t.TempDir()
	cfg.MaxSpoolEvents = 1
	cfg.SpoolPolicy = "reject"
	a, err := New(cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/publish", bytes.NewBufferString(`{"topic":"x","payload":{"n":1}}`))
		a.Handler().ServeHTTP(rr, req)
		if i == 0 && rr.Code != 202 {
			t.Fatalf("first=%d", rr.Code)
		}
		if i == 1 && rr.Code != 507 {
			t.Fatalf("second=%d %s", rr.Code, rr.Body.String())
		}
	}
	if a.Pending() != 1 {
		t.Fatalf("pending=%d", a.Pending())
	}
}

func TestLocalRouteWorksWhileCloudIsDown(t *testing.T) {
	var hits int
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++; w.WriteHeader(204) }))
	defer local.Close()
	cfg := DefaultConfig()
	cfg.ServerURL = "http://127.0.0.1:1"
	cfg.SiteID = "s"
	cfg.AgentToken = "t"
	cfg.DataDir = t.TempDir()
	cfg.LocalRoutes = []LocalRoute{{Name: "MES", Topic: "factory/+/temp", TargetURL: local.URL, Method: "POST"}}
	a, err := New(cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/publish", bytes.NewBufferString(`{"topic":"factory/line1/temp","payload":{"c":31}}`))
	a.Handler().ServeHTTP(rr, req)
	if rr.Code != 202 {
		t.Fatalf("%d %s", rr.Code, rr.Body.String())
	}
	a.flushLocal(context.Background())
	if hits != 1 {
		t.Fatalf("hits=%d", hits)
	}
	a.FlushNow(context.Background())
	if a.Pending() != 1 {
		t.Fatalf("cloud spool should remain, got %d", a.Pending())
	}
}

func TestAgentSendsOriginalEventTime(t *testing.T) {
	var got time.Time
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/events" {
			var x struct {
				EventTime time.Time `json:"event_time"`
			}
			_ = json.NewDecoder(r.Body).Decode(&x)
			got = x.EventTime
			w.WriteHeader(202)
			return
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()
	cfg := DefaultConfig()
	cfg.ServerURL = srv.URL
	cfg.SiteID = "s"
	cfg.AgentToken = "t"
	cfg.DataDir = t.TempDir()
	a, _ := New(cfg, "")
	when := time.Date(2026, 9, 6, 2, 3, 4, 0, time.UTC)
	_, err := a.ingestWithTime(context.Background(), "x", json.RawMessage(`1`), nil, when)
	if err != nil {
		t.Fatal(err)
	}
	a.FlushNow(context.Background())
	if !got.Equal(when) {
		t.Fatalf("got=%s", got)
	}
}
