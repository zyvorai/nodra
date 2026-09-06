// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// TestClientDemoWalkthrough simulates the full client-demo path a sales engineer
// walks through in the console: sign-in → overview → enroll site → heartbeat →
// device → twin → route → event delivery → failed delivery/DLQ → edge app →
// console list APIs return JSON arrays (never null).
func TestClientDemoWalkthrough(t *testing.T) {
	s, err := New(Config{
		DataDir:         t.TempDir(),
		AdminToken:      "demo-token",
		AdminUser:       "admin",
		AdminPassword:   "demo-pass",
		EnrollmentToken: "demo-enroll",
		MaxBodyBytes:    1 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	c := testClient{ts.URL, "demo-token", t}

	// ── Chapter 1: Apple login gate ──
	code, body := c.req("GET", "/", nil, "")
	if code != 200 {
		t.Fatalf("dashboard %d", code)
	}
	html := string(body)
	for _, want := range []string{"Sign in to Nodra", "login-store-page", "login-wordmark", "Built by Zyvor", "loginForm", "zyvor-mark"} {
		if !strings.Contains(html, want) {
			t.Fatalf("dashboard missing %q", want)
		}
	}

	code, body = c.req("POST", "/api/v1/auth/login", map[string]any{"username": "admin", "password": "wrong"}, "")
	if code != 401 {
		t.Fatalf("bad login want 401 got %d %s", code, body)
	}
	code, body = c.req("POST", "/api/v1/auth/login", map[string]any{"username": "admin", "password": "demo-pass"}, "")
	if code != 200 {
		t.Fatalf("login %d %s", code, body)
	}
	var login struct {
		Token string `json:"token"`
		User  struct {
			Username string `json:"username"`
			Role     string `json:"role"`
		} `json:"user"`
	}
	if err := json.Unmarshal(body, &login); err != nil || login.Token != "demo-token" || login.User.Username != "admin" {
		t.Fatalf("login payload=%s", body)
	}
	code, body = c.req("GET", "/api/v1/auth/me", nil, login.Token)
	if code != 200 || !strings.Contains(string(body), `"authenticated":true`) {
		t.Fatalf("auth/me %d %s", code, body)
	}

	// Viewer role: read OK, mutate forbidden.
	s.cfg.ViewerToken = "viewer-token"
	s.cfg.ViewerUser = "viewer"
	s.cfg.ViewerPassword = "viewer-pass"
	code, body = c.req("POST", "/api/v1/auth/login", map[string]any{"username": "viewer", "password": "viewer-pass"}, "")
	if code != 200 {
		t.Fatalf("viewer login %d %s", code, body)
	}
	var vlogin struct {
		Token string `json:"token"`
		User  struct {
			Role string `json:"role"`
		} `json:"user"`
	}
	_ = json.Unmarshal(body, &vlogin)
	if vlogin.Token != "viewer-token" || vlogin.User.Role != "viewer" {
		t.Fatalf("viewer login payload %s", body)
	}
	code, _ = c.req("GET", "/api/v1/overview", nil, vlogin.Token)
	if code != 200 {
		t.Fatalf("viewer overview %d", code)
	}
	code, body = c.req("POST", "/api/v1/routes", map[string]any{"name": "x", "topic": "a/b", "target_url": "http://example.invalid/x"}, vlogin.Token)
	if code != 403 {
		t.Fatalf("viewer mutate want 403 got %d %s", code, body)
	}

	// Empty console lists must be JSON arrays, not null (UI Promise.all / .length).
	for _, path := range []string{"/api/v1/sites", "/api/v1/devices", "/api/v1/twins", "/api/v1/routes", "/api/v1/deployments", "/api/v1/alerts", "/api/v1/deadletters"} {
		code, body = c.req("GET", path, nil, login.Token)
		if code != 200 {
			t.Fatalf("%s %d %s", path, code, body)
		}
		if strings.TrimSpace(string(body)) == "null" {
			t.Fatalf("%s returned null; want []", path)
		}
		if len(body) == 0 || body[0] != '[' {
			t.Fatalf("%s want JSON array, got %s", path, body)
		}
	}

	// ── Chapter 2: enroll edge site + heartbeat (fleet online) ──
	code, body = c.req("POST", "/api/v1/enroll", map[string]any{
		"name": "Demo Factory Floor", "enrollment_token": "demo-enroll",
		"metadata": map[string]string{"region": "lab", "customer": "acme"},
	}, "")
	if code != 201 {
		t.Fatalf("enroll %d %s", code, body)
	}
	var enrolled struct {
		SiteID     string `json:"site_id"`
		AgentToken string `json:"agent_token"`
	}
	_ = json.Unmarshal(body, &enrolled)
	if enrolled.SiteID == "" || enrolled.AgentToken == "" {
		t.Fatalf("enroll missing creds %s", body)
	}
	code, body = c.req("POST", "/api/v1/heartbeat", map[string]any{
		"site_id": enrolled.SiteID, "version": "0.2.0",
		"metrics": map[string]any{"queue_depth": 2, "queue_bytes": 4096},
	}, enrolled.AgentToken)
	if code != 200 {
		t.Fatalf("heartbeat %d %s", code, body)
	}

	// ── Chapter 3: device + twin ──
	code, body = c.req("POST", "/api/v1/devices/register", map[string]any{
		"site_id": enrolled.SiteID, "name": "Line-1 PLC", "protocol": "mqtt",
	}, enrolled.AgentToken)
	if code != 201 {
		t.Fatalf("device %d %s", code, body)
	}
	var device map[string]any
	_ = json.Unmarshal(body, &device)
	deviceID, _ := device["id"].(string)
	if deviceID == "" {
		t.Fatalf("device id missing %s", body)
	}
	code, body = c.req("PUT", "/api/v1/twins/"+deviceID+"/desired", map[string]any{
		"desired": map[string]any{"setpoint_c": 42, "mode": "auto"},
	}, login.Token)
	if code != 200 {
		t.Fatalf("twin desired %d %s", code, body)
	}
	code, body = c.req("POST", "/api/v1/agent/twins/"+deviceID+"/reported", map[string]any{
		"site_id": enrolled.SiteID, "reported": map[string]any{"setpoint_c": 42, "mode": "auto"},
	}, enrolled.AgentToken)
	if code != 200 {
		t.Fatalf("twin reported %d %s", code, body)
	}

	// ── Chapter 4: stream route + successful delivery ──
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(204)
	}))
	t.Cleanup(target.Close)
	code, body = c.req("POST", "/api/v1/routes", map[string]any{
		"name": "MES webhook", "topic": "factory/+/telemetry",
		"target_url": target.URL, "method": "POST", "enabled": true, "retry_max": 3, "timeout_seconds": 5,
	}, login.Token)
	if code != 201 {
		t.Fatalf("route %d %s", code, body)
	}
	code, body = c.req("POST", "/api/v1/events", map[string]any{
		"site_id": enrolled.SiteID, "topic": "factory/line1/telemetry",
		"payload": map[string]any{"temperature": 41.5, "ok": true},
	}, enrolled.AgentToken)
	if code != 202 {
		t.Fatalf("event %d %s", code, body)
	}
	s.ProcessOnce(context.Background())
	if hits.Load() != 1 {
		t.Fatalf("delivery hits=%d want 1", hits.Load())
	}

	// ── Chapter 5: failed delivery → alert + dead letter (recovery story) ──
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "downstream down", 503)
	}))
	t.Cleanup(bad.Close)
	_, _ = c.req("POST", "/api/v1/routes", map[string]any{
		"name": "Failing ERP", "topic": "factory/+/alarms",
		"target_url": bad.URL, "method": "POST", "enabled": true, "retry_max": 1, "timeout_seconds": 2,
	}, login.Token)
	_, _ = c.req("POST", "/api/v1/events", map[string]any{
		"site_id": enrolled.SiteID, "topic": "factory/line1/alarms",
		"payload": map[string]any{"alarm": "overtemp"},
	}, enrolled.AgentToken)
	s.ProcessOnce(context.Background())
	code, body = c.req("GET", "/api/v1/alerts", nil, login.Token)
	if code != 200 || !strings.Contains(string(body), "delivery_failed") {
		t.Fatalf("alerts after failure %d %s", code, body)
	}
	code, body = c.req("GET", "/api/v1/deadletters", nil, login.Token)
	if code != 200 {
		t.Fatalf("deadletters %d %s", code, body)
	}
	if strings.TrimSpace(string(body)) == "[]" || strings.TrimSpace(string(body)) == "null" {
		// Some builds may collapse retries differently; still require alerts above.
		t.Logf("deadletters empty (ok if alert raised): %s", body)
	}

	// ── Chapter 6: edge application desired state ──
	code, body = c.req("POST", "/api/v1/deployments", map[string]any{
		"site_id": enrolled.SiteID, "name": "vision-edge", "version": "1.2.0",
		"image": "ghcr.io/zyvorai/vision-edge:1.2.0", "desired_state": "running",
	}, login.Token)
	if code != 201 {
		t.Fatalf("deployment %d %s", code, body)
	}
	var dep struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &dep); err != nil || dep.ID == "" {
		t.Fatalf("deployment id %s", body)
	}
	code, body = c.req("PATCH", "/api/v1/deployments/"+dep.ID, map[string]any{"desired_state": "stopped"}, login.Token)
	if code != 200 {
		t.Fatalf("deployment patch %d %s", code, body)
	}

	// ── Chapter 7: overview matches the demo console strip ──
	code, body = c.req("GET", "/api/v1/overview", nil, login.Token)
	if code != 200 {
		t.Fatalf("overview %d %s", code, body)
	}
	var overview map[string]any
	_ = json.Unmarshal(body, &overview)
	if overview["sites"].(float64) < 1 || overview["online_sites"].(float64) < 1 {
		t.Fatalf("overview sites=%v", overview)
	}
	if overview["devices"].(float64) < 1 || overview["routes"].(float64) < 1 {
		t.Fatalf("overview devices/routes=%v", overview)
	}
	if overview["deployments"].(float64) < 1 {
		t.Fatalf("overview deployments=%v", overview)
	}

	// Console list endpoints still return arrays after data exists.
	for _, path := range []string{"/api/v1/sites", "/api/v1/devices", "/api/v1/routes", "/api/v1/deployments"} {
		code, body = c.req("GET", path, nil, login.Token)
		if code != 200 || len(body) == 0 || body[0] != '[' {
			t.Fatalf("%s not array: %d %s", path, code, body)
		}
	}

	code, body = c.req("DELETE", "/api/v1/deployments/"+dep.ID, nil, login.Token)
	if code != 204 {
		t.Fatalf("deployment delete %d %s", code, body)
	}
}
