// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestConsoleSessionMintRefreshLogout(t *testing.T) {
	srv, err := New(Config{DataDir: t.TempDir(), AdminToken: "adm", AdminUser: "admin", AdminPassword: "secret", EnrollmentToken: "enroll", SessionTTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	c := testClient{ts.URL, "adm", t}

	code, body := c.req("POST", "/api/v1/auth/login", map[string]any{"username": "admin", "password": "secret"}, "")
	if code != 200 {
		t.Fatalf("login %d %s", code, body)
	}
	var login struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.Unmarshal(body, &login); err != nil || login.Token == "" || login.Token == "adm" || login.ExpiresAt.IsZero() {
		t.Fatalf("login %s", body)
	}
	code, body = c.req("GET", "/api/v1/overview", nil, login.Token)
	if code != 200 {
		t.Fatalf("overview with session %d %s", code, body)
	}
	code, body = c.req("POST", "/api/v1/auth/refresh", nil, login.Token)
	if code != 200 {
		t.Fatalf("refresh %d %s", code, body)
	}
	var refreshed struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(body, &refreshed)
	if refreshed.Token == "" || refreshed.Token == login.Token {
		t.Fatalf("refresh did not rotate token: %s", body)
	}
	code, _ = c.req("GET", "/api/v1/overview", nil, login.Token)
	if code != 401 {
		t.Fatalf("old token still valid after refresh")
	}
	code, _ = c.req("POST", "/api/v1/auth/logout", nil, refreshed.Token)
	if code != 204 {
		t.Fatalf("logout %d", code)
	}
	code, _ = c.req("GET", "/api/v1/overview", nil, refreshed.Token)
	if code != 401 {
		t.Fatalf("session still valid after logout")
	}
	code, _ = c.req("GET", "/api/v1/overview", nil, "adm")
	if code != 200 {
		t.Fatalf("static admin token broken")
	}
}

func TestCustomRoleWriteAndRead(t *testing.T) {
	dir := t.TempDir()
	srv, err := New(Config{DataDir: dir, AdminToken: "adm", EnrollmentToken: "enroll"})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	c := testClient{ts.URL, "adm", t}

	code, body := c.req("POST", "/api/v1/roles", map[string]any{"name": "ops", "write": true}, "adm")
	if code != 201 {
		t.Fatalf("create role %d %s", code, body)
	}
	var role CustomRole
	if err := json.Unmarshal(body, &role); err != nil || role.Token == "" {
		t.Fatalf("role %s", body)
	}
	code, _ = c.req("GET", "/api/v1/overview", nil, role.Token)
	if code != 200 {
		t.Fatalf("ops overview %d", code)
	}
	code, body = c.req("POST", "/api/v1/roles", map[string]any{"name": "audit", "write": false}, "adm")
	if code != 201 {
		t.Fatalf("create audit role %d %s", code, body)
	}
	var reader CustomRole
	_ = json.Unmarshal(body, &reader)
	code, body = c.req("POST", "/api/v1/routes", map[string]any{"name": "x", "topic": "a/b", "target_url": "http://example.invalid/x"}, reader.Token)
	if code != 403 {
		t.Fatalf("read-only role mutate want 403 got %d %s", code, body)
	}

	srv2, err := New(Config{DataDir: dir, AdminToken: "adm", EnrollmentToken: "enroll"})
	if err != nil {
		t.Fatal(err)
	}
	ts2 := httptest.NewServer(srv2.Handler())
	defer ts2.Close()
	c2 := testClient{ts2.URL, "adm", t}
	code, _ = c2.req("GET", "/api/v1/overview", nil, role.Token)
	if code != 200 {
		t.Fatalf("persisted role token after restart %d", code)
	}
}

func TestZTPBootstrap(t *testing.T) {
	srv, err := New(Config{DataDir: t.TempDir(), AdminToken: "adm", EnrollmentToken: "enroll", PublicBaseURL: "https://cp.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	c := testClient{ts.URL, "adm", t}
	code, body := c.req("POST", "/api/v1/ztp/bootstrap", map[string]any{"name": "plant-1", "enrollment_token": "enroll"}, "")
	if code != 201 {
		t.Fatalf("ztp %d %s", code, body)
	}
	if !strings.Contains(string(body), `"agent_config"`) || !strings.Contains(string(body), "plant-1") {
		t.Fatalf("ztp body %s", body)
	}
}
