// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestAuditTrailCapturesAdminActions(t *testing.T) {
	_, ts, c := newTestServer(t)

	// 1. A successful console login is audited under the admin username.
	code, b := c.req("POST", "/api/v1/auth/login", map[string]any{"username": "admin", "password": "adm"}, "")
	if code != 200 {
		t.Fatalf("login: %d %s", code, b)
	}

	// 2. A failed login attempt is audited too (as a denial).
	code, b = c.req("POST", "/api/v1/auth/login", map[string]any{"username": "admin", "password": "wrong"}, "")
	if code != 401 {
		t.Fatalf("bad login: %d %s", code, b)
	}

	// 3. Enroll + revoke a site.
	siteID, _ := enrollSite(t, c)
	code, b = c.req("POST", "/api/v1/sites/"+siteID+"/revoke", map[string]bool{}, "adm")
	if code != 200 {
		t.Fatalf("revoke: %d %s", code, b)
	}

	// 4. Create a route.
	code, b = c.req("POST", "/api/v1/routes", map[string]any{
		"name": "MES", "topic": "factory/#", "target_url": "http://127.0.0.1:9/hook",
	}, "adm")
	if code != 201 {
		t.Fatalf("route create: %d %s", code, b)
	}

	code, b = c.req(http.MethodGet, "/api/v1/audit?limit=100", nil, "adm")
	if code != 200 {
		t.Fatalf("audit list: %d %s", code, b)
	}
	var out struct {
		Entries    []map[string]any `json:"entries"`
		NextCursor string           `json:"next_cursor"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal audit list: %v (%s)", err, b)
	}
	if len(out.Entries) == 0 {
		t.Fatal("expected audit entries, got none")
	}

	seen := map[string]map[string]any{}
	for _, e := range out.Entries {
		action, _ := e["action"].(string)
		seen[action] = e
	}
	want := map[string]string{
		"login":        "admin",
		"enroll":       siteID,
		"site.revoke":  "admin",
		"route.create": "admin",
	}
	for action, wantActor := range want {
		e, ok := seen[action]
		if !ok {
			t.Errorf("missing audit entry for action %q; got actions=%v", action, keysOf(seen))
			continue
		}
		if got, _ := e["actor"].(string); got != wantActor {
			t.Errorf("action %q: actor=%q, want %q", action, got, wantActor)
		}
	}
	if e, ok := seen["login"]; !ok || e["result"] != "ok" {
		t.Errorf("expected a passing login entry with result=ok, got %+v", e)
	}

	// The failed login should also be present, with the attempted username
	// as actor and a denied result.
	foundDenied := false
	for _, e := range out.Entries {
		if e["action"] == "login" && e["result"] == "denied" {
			foundDenied = true
		}
	}
	if !foundDenied {
		t.Error("expected a denied login entry for the bad-password attempt")
	}

	// GET /api/v1/audit/export streams NDJSON with at least the same entries.
	code, b = c.req(http.MethodGet, "/api/v1/audit/export", nil, "adm")
	if code != 200 {
		t.Fatalf("audit export: %d %s", code, b)
	}
	lines := 0
	for _, line := range splitNDJSON(b) {
		if len(line) == 0 {
			continue
		}
		var e map[string]any
		if err := json.Unmarshal(line, &e); err != nil {
			t.Fatalf("export line not valid JSON: %v (%s)", err, line)
		}
		lines++
	}
	if lines < len(out.Entries) {
		t.Fatalf("export returned %d lines, list returned %d entries", lines, len(out.Entries))
	}
	_ = ts
}

func keysOf(m map[string]map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func splitNDJSON(b []byte) [][]byte {
	var out [][]byte
	start := 0
	for i, c := range b {
		if c == '\n' {
			out = append(out, b[start:i])
			start = i + 1
		}
	}
	if start < len(b) {
		out = append(out, b[start:])
	}
	return out
}
