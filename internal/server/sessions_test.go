// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestConsoleSessionMintRefreshLogout(t *testing.T) {
	dir := t.TempDir()
	srv, err := New(Config{DataDir: dir, AdminToken: "adm", AdminUser: "admin", AdminPassword: "secret", EnrollmentToken: "enroll", SessionTTL: time.Minute})
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
	// Survives process restart via sessions.json
	srv2, err := New(Config{DataDir: dir, AdminToken: "adm", AdminUser: "admin", AdminPassword: "secret", EnrollmentToken: "enroll", SessionTTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	ts2 := httptest.NewServer(srv2.Handler())
	defer ts2.Close()
	c2 := testClient{ts2.URL, "adm", t}
	code, _ = c2.req("GET", "/api/v1/overview", nil, login.Token)
	if code != 200 {
		t.Fatalf("session after restart %d", code)
	}
	code, body = c2.req("POST", "/api/v1/auth/refresh", nil, login.Token)
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
	code, _ = c2.req("GET", "/api/v1/overview", nil, login.Token)
	if code != 401 {
		t.Fatalf("old token still valid after refresh")
	}
	code, _ = c2.req("POST", "/api/v1/auth/logout", nil, refreshed.Token)
	if code != 204 {
		t.Fatalf("logout %d", code)
	}
	code, _ = c2.req("GET", "/api/v1/overview", nil, refreshed.Token)
	if code != 401 {
		t.Fatalf("session still valid after logout")
	}
	code, _ = c2.req("GET", "/api/v1/overview", nil, "adm")
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

func TestPostgresSharedSessionsAndRoles(t *testing.T) {
	dsn := os.Getenv("NODRA_DATABASE_URL")
	if dsn == "" {
		t.Skip("NODRA_DATABASE_URL unset — optional Postgres shared sessions/roles")
	}
	cfgA := Config{
		StoreDriver: "postgres", DatabaseURL: dsn, DataDir: t.TempDir(),
		AdminToken: "adm", AdminUser: "admin", AdminPassword: "secret",
		EnrollmentToken: "enroll", SessionTTL: time.Minute,
	}
	cfgB := cfgA
	cfgB.DataDir = t.TempDir()
	a, err := New(cfgA)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Shutdown(context.Background())
	b, err := New(cfgB)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Shutdown(context.Background())
	tsA := httptest.NewServer(a.Handler())
	defer tsA.Close()
	tsB := httptest.NewServer(b.Handler())
	defer tsB.Close()
	cA := testClient{tsA.URL, "adm", t}
	cB := testClient{tsB.URL, "adm", t}

	code, body := cA.req("POST", "/api/v1/auth/login", map[string]any{"username": "admin", "password": "secret"}, "")
	if code != 200 {
		t.Fatalf("login %d %s", code, body)
	}
	var login struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(body, &login); err != nil || login.Token == "" {
		t.Fatalf("login %s", body)
	}
	code, _ = cB.req("GET", "/api/v1/overview", nil, login.Token)
	if code != 200 {
		t.Fatalf("replica B must accept session minted on A, got %d", code)
	}

	roleName := "ops-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	code, body = cA.req("POST", "/api/v1/roles", map[string]any{"name": roleName, "write": true}, "adm")
	if code != 201 {
		t.Fatalf("create role %d %s", code, body)
	}
	var role CustomRole
	if err := json.Unmarshal(body, &role); err != nil || role.Token == "" {
		t.Fatalf("role %s", body)
	}
	code, _ = cB.req("GET", "/api/v1/overview", nil, role.Token)
	if code != 200 {
		t.Fatalf("replica B must accept role minted on A, got %d", code)
	}
	code, _ = cB.req("DELETE", "/api/v1/roles/"+roleName, nil, "adm")
	if code != 204 {
		t.Fatalf("delete role on B %d", code)
	}
	code, _ = cA.req("GET", "/api/v1/overview", nil, role.Token)
	if code != 401 {
		t.Fatalf("role should be gone on A after B delete, got %d", code)
	}
}
