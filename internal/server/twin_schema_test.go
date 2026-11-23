// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-2.0

package server

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTwinSchemaDriftAndDryRun(t *testing.T) {
	_, _, c := newTestServer(t)
	site, tok := enrollSite(t, c)
	if code, b := c.req("POST", "/api/v1/devices/register", map[string]any{"id": "plc-9", "site_id": site, "name": "PLC", "protocol": "modbus"}, tok); code != 201 {
		t.Fatalf("%d %s", code, b)
	}
	schema := map[string]any{"type": "object", "additionalProperties": false, "required": []any{"speed"},
		"properties": map[string]any{"speed": map[string]any{"type": "integer", "minimum": 0, "maximum": 3000}}}

	// invalid / unsupported schemas are rejected
	for name, bad := range map[string]map[string]any{"unsupported keyword": {"oneOf": []any{}}, "bad type": {"type": "float"}} {
		if code, b := c.req("PUT", "/api/v1/twins/plc-9/schema", map[string]any{"schema": bad}, "adm"); code != 400 {
			t.Errorf("%s: %d %s", name, code, b)
		}
	}
	if code, _ := c.req("PUT", "/api/v1/twins/nope/schema", map[string]any{"schema": schema}, "adm"); code != 404 {
		t.Errorf("unknown device: %d", code)
	}
	if code, _ := c.req("PUT", "/api/v1/twins/plc-9/schema", map[string]any{"schema": schema}, ""); code != 401 {
		t.Errorf("unauthenticated schema set: %d", code)
	}
	if code, b := c.req("PUT", "/api/v1/twins/plc-9/schema", map[string]any{"schema": schema}, "adm"); code != 200 {
		t.Fatalf("set schema %d %s", code, b)
	}

	// desired violating the schema is refused and not stored
	code, b := c.req("PUT", "/api/v1/twins/plc-9/desired", map[string]any{"desired": map[string]any{"speed": 9000}}, "adm")
	if code != 422 || !strings.Contains(string(b), "above maximum") {
		t.Fatalf("expected 422, got %d %s", code, b)
	}
	if code, b = c.req("PUT", "/api/v1/twins/plc-9/desired", map[string]any{"desired": map[string]any{"speed": 1200, "bogus": true}}, "adm"); code != 422 {
		t.Fatalf("additionalProperties: %d %s", code, b)
	}

	// dry run reports validity and the diff, and does not write
	if code, b = c.req("PUT", "/api/v1/twins/plc-9/desired", map[string]any{"desired": map[string]any{"speed": 1200}}, "adm"); code != 200 {
		t.Fatalf("%d %s", code, b)
	}
	code, b = c.req("PUT", "/api/v1/twins/plc-9/desired?dry_run=true", map[string]any{"desired": map[string]any{"speed": 1500}}, "adm")
	var dry struct {
		DryRun  bool `json:"dry_run"`
		Valid   bool `json:"valid"`
		Changes []struct{ Path, Kind string }
		Ver     uint64 `json:"desired_version"`
	}
	_ = json.Unmarshal(b, &dry)
	if code != 200 || !dry.DryRun || !dry.Valid || len(dry.Changes) != 1 || dry.Changes[0].Path != "/speed" || dry.Changes[0].Kind != "change" || dry.Ver != 1 {
		t.Fatalf("dry run: %d %s", code, b)
	}
	code, b = c.req("PUT", "/api/v1/twins/plc-9/desired?dry_run=true", map[string]any{"desired": map[string]any{"speed": -1}}, "adm")
	if code != 200 || !strings.Contains(string(b), `"valid":false`) {
		t.Fatalf("dry run invalid: %d %s", code, b)
	}
	code, b = c.req("GET", "/api/v1/twins", nil, "adm")
	if !strings.Contains(string(b), `"desired_version":1`) || !strings.Contains(string(b), `"speed":1200`) {
		t.Fatalf("dry run must not store: %s", b)
	}

	// drift: nothing reported yet
	code, b = c.req("GET", "/api/v1/twins/plc-9/drift", nil, "adm")
	if code != 200 || !strings.Contains(string(b), `"in_sync":false`) || !strings.Contains(string(b), `"missing"`) {
		t.Fatalf("drift: %d %s", code, b)
	}
	code, b = c.req("GET", "/api/v1/twins/drift", nil, "adm")
	if code != 200 || !strings.Contains(string(b), "plc-9") {
		t.Fatalf("fleet drift: %d %s", code, b)
	}
	code, b = c.req("GET", "/metrics", nil, "")
	if !strings.Contains(string(b), "nodra_twins_drifted 1\n") {
		t.Fatalf("metrics: %s", b)
	}

	// device reports a mismatching value, then converges
	c.req("POST", "/api/v1/agent/twins/plc-9/reported", map[string]any{"site_id": site, "reported": map[string]any{"speed": 1100}}, tok)
	_, b = c.req("GET", "/api/v1/twins/plc-9/drift", nil, "adm")
	if !strings.Contains(string(b), `"mismatch"`) {
		t.Fatalf("expected mismatch: %s", b)
	}
	c.req("POST", "/api/v1/agent/twins/plc-9/reported", map[string]any{"site_id": site, "reported": map[string]any{"speed": 1200}}, tok)
	_, b = c.req("GET", "/api/v1/twins/plc-9/drift", nil, "adm")
	if !strings.Contains(string(b), `"in_sync":true`) {
		t.Fatalf("expected in sync: %s", b)
	}
	if _, b = c.req("GET", "/api/v1/twins/drift", nil, "adm"); strings.TrimSpace(string(b)) != "[]" {
		t.Fatalf("in-sync twin listed: %s", b)
	}
	// reported state violating the schema keeps the twin flagged
	c.req("POST", "/api/v1/agent/twins/plc-9/reported", map[string]any{"site_id": site, "reported": map[string]any{"speed": 1200, "junk": 1}}, tok)
	_, b = c.req("GET", "/api/v1/twins/plc-9/drift", nil, "adm")
	if !strings.Contains(string(b), `"reported_violations"`) || !strings.Contains(string(b), `"in_sync":false`) {
		t.Fatalf("reported violation: %s", b)
	}
	// clearing the schema re-allows free-form desired state
	if code, _ = c.req("PUT", "/api/v1/twins/plc-9/schema", map[string]any{"schema": nil}, "adm"); code != 200 {
		t.Fatal(code)
	}
	if code, b = c.req("PUT", "/api/v1/twins/plc-9/desired", map[string]any{"desired": map[string]any{"anything": "goes"}}, "adm"); code != 200 {
		t.Fatalf("%d %s", code, b)
	}
	if code, _ = c.req("GET", "/api/v1/twins/missing/drift", nil, "adm"); code != 404 {
		t.Errorf("missing twin drift: %d", code)
	}
}

func TestSiteSpoolForecastMetrics(t *testing.T) {
	_, _, c := newTestServer(t)
	site, tok := enrollSite(t, c)
	if code, b := c.req("POST", "/api/v1/heartbeat", map[string]any{"site_id": site, "version": "0.2.0", "metrics": map[string]any{"spool_time_to_full_seconds": 1234.5, "spool_fill_ratio": 0.25}}, tok); code != 200 {
		t.Fatalf("%d %s", code, b)
	}
	_, b := c.req("GET", "/metrics", nil, "")
	if !strings.Contains(string(b), `nodra_site_spool_time_to_full_seconds{site_id="`+site+`"} 1234.5`) || !strings.Contains(string(b), `nodra_site_spool_fill_ratio{site_id="`+site+`"} 0.25`) {
		t.Fatalf("metrics: %s", b)
	}
}
