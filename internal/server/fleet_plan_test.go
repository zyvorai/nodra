// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-2.0

package server

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/zyvorai/nodra/internal/model"
)

func decodePlan(t *testing.T, b []byte) fleetPlan {
	t.Helper()
	var p fleetPlan
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatalf("%v: %s", err, b)
	}
	return p
}

func TestFleetPlanEndpoint(t *testing.T) {
	_, _, c := newTestServer(t)
	site, tok := enrollSite(t, c)
	for _, id := range []string{"a", "b", "c"} {
		if code, b := c.req("POST", "/api/v1/devices/register", map[string]any{"id": id, "site_id": site, "name": id, "protocol": "modbus"}, tok); code != 201 {
			t.Fatalf("%d %s", code, b)
		}
	}
	schema := map[string]any{"type": "object", "additionalProperties": false,
		"properties": map[string]any{"speed": map[string]any{"type": "integer", "maximum": 3000}, "mode": map[string]any{"type": "string"}}}
	if code, b := c.req("PUT", "/api/v1/twins/b/schema", map[string]any{"schema": schema}, "adm"); code != 200 {
		t.Fatalf("%d %s", code, b)
	}
	if code, b := c.req("PUT", "/api/v1/twins/a/desired", map[string]any{"desired": map[string]any{"speed": 100}}, "adm"); code != 200 {
		t.Fatalf("%d %s", code, b)
	}

	body := map[string]any{"defaults": map[string]any{"mode": "auto"}, "site_ids": []string{site},
		"devices": map[string]any{"a": map[string]any{"speed": 100, "mode": "auto"}, "b": map[string]any{"speed": 9000}, "ghost": map[string]any{"x": 1}}}
	code, raw := c.req("POST", "/api/v1/twins/plan", body, "adm")
	if code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	p := decodePlan(t, raw)
	byID := map[string]fleetPlanDevice{}
	for _, d := range p.Devices {
		byID[d.DeviceID] = d
	}
	if !p.DryRun || p.Valid {
		t.Fatalf("plan should be invalid: %s", raw)
	}
	if byID["a"].Status != planChange || len(byID["a"].Changes) != 1 || byID["a"].DesiredVersion != 1 {
		t.Fatalf("a: %+v", byID["a"])
	}
	if byID["b"].Status != planInvalid || len(byID["b"].Violations) == 0 {
		t.Fatalf("b: %+v", byID["b"])
	}
	if byID["c"].Status != planChange { // site default applies to unlisted device c
		t.Fatalf("c: %+v", byID["c"])
	}
	if byID["ghost"].Status != planUnknown {
		t.Fatalf("ghost: %+v", byID["ghost"])
	}
	if p.Summary.Devices != 4 || p.Summary.Changed != 2 || p.Summary.Invalid != 1 || p.Summary.Unknown != 1 {
		t.Fatalf("%+v", p.Summary)
	}
	if !strings.HasPrefix(p.Hash, "sha256:") || len(p.Hash) != 71 {
		t.Fatal(p.Hash)
	}

	// nothing stored
	_, tw := c.req("GET", "/api/v1/twins", nil, "adm")
	if strings.Contains(string(tw), "auto") {
		t.Fatalf("plan wrote state: %s", tw)
	}

	// stable hash for identical input and fleet state
	_, raw2 := c.req("POST", "/api/v1/twins/plan", body, "adm")
	if decodePlan(t, raw2).Hash != p.Hash {
		t.Fatal("plan hash not stable")
	}
	// hash changes when the base twin version moves
	if code, b := c.req("PUT", "/api/v1/twins/c/desired", map[string]any{"desired": map[string]any{"k": 1}}, "adm"); code != 200 {
		t.Fatalf("%d %s", code, b)
	}
	_, raw3 := c.req("POST", "/api/v1/twins/plan", body, "adm")
	if decodePlan(t, raw3).Hash == p.Hash {
		t.Fatal("hash did not change after the fleet changed")
	}

	// hostile / malformed input
	for name, bad := range map[string]any{
		"empty":         map[string]any{},
		"defaults only": map[string]any{"defaults": map[string]any{"a": 1}},
		"unknown field": map[string]any{"devices": map[string]any{"a": map[string]any{}}, "apply": true},
		"bad device":    map[string]any{"devices": map[string]any{"a": 5}},
	} {
		if code, _ := c.req("POST", "/api/v1/twins/plan", bad, "adm"); code != 400 {
			t.Errorf("%s: %d", name, code)
		}
	}
	if code, _ := c.req("POST", "/api/v1/twins/plan", body, ""); code != 401 {
		t.Errorf("unauth: %d", code)
	}
	big := map[string]any{}
	for i := 0; i <= maxPlanDevices; i++ {
		big[fmt.Sprintf("d%d", i)] = map[string]any{}
	}
	if code, _ := c.req("POST", "/api/v1/twins/plan", map[string]any{"devices": big}, "adm"); code != 400 {
		t.Errorf("oversize plan: %d", code)
	}
	if code, b := c.req("POST", "/api/v1/twins/plan", map[string]any{"devices": map[string]any{"a": map[string]any{"ota": "x"}}}, "adm"); code != 200 || !strings.Contains(string(b), planInvalid) {
		t.Errorf("reserved ota key: %d %s", code, b)
	}
}

func TestDesiredPutKeepsInFlightOTA(t *testing.T) {
	_, _, c := newTestServer(t)
	site, tok := enrollSite(t, c)
	if code, b := c.req("POST", "/api/v1/devices/register", map[string]any{"id": "o1", "site_id": site, "name": "o", "protocol": "modbus"}, tok); code != 201 {
		t.Fatalf("%d %s", code, b)
	}
	// seed desired.ota via the twin store path used by OTA requests
	if code, b := c.req("PUT", "/api/v1/twins/o1/desired", map[string]any{"desired": map[string]any{"ota": map[string]any{"update_id": "u1"}, "speed": 1}}, "adm"); code != 200 {
		t.Fatalf("%d %s", code, b)
	}
	code, b := c.req("PUT", "/api/v1/twins/o1/desired", map[string]any{"desired": map[string]any{"speed": 2}}, "adm")
	if code != 200 || !strings.Contains(string(b), "u1") || !strings.Contains(string(b), `"speed":2`) {
		t.Fatalf("OTA request wiped by config write: %d %s", code, b)
	}
}

func TestBuildFleetPlanOrgScoping(t *testing.T) {
	devs := []model.Device{{ID: "mine", SiteID: "s1"}, {ID: "theirs", SiteID: "s2"}}
	twin := func(string) (model.Twin, bool) { return model.Twin{}, false }
	p := buildFleetPlan(fleetPlanRequest{Defaults: map[string]any{"a": 1}, SiteIDs: []string{"s1", "s2"}, Devices: map[string]map[string]any{"theirs": {"b": 2}}},
		devs, twin, func(s string) bool { return s == "s1" })
	got := map[string]string{}
	for _, d := range p.Devices {
		got[d.DeviceID] = d.Status
	}
	if got["mine"] != planChange || got["theirs"] != planUnknown || len(got) != 2 {
		t.Fatalf("%v", got)
	}
}

func TestPlanHashIndependentOfInputOrder(t *testing.T) {
	devs := []model.Device{{ID: "a", SiteID: "s"}, {ID: "b", SiteID: "s"}}
	twin := func(string) (model.Twin, bool) { return model.Twin{}, false }
	all := func(string) bool { return true }
	h := func(req fleetPlanRequest) string { return buildFleetPlan(req, devs, twin, all).Hash }
	r1 := fleetPlanRequest{Devices: map[string]map[string]any{"a": {"x": 1, "y": 2}, "b": {"z": []any{1, 2}}}}
	r2 := fleetPlanRequest{Devices: map[string]map[string]any{"b": {"z": []any{1, 2}}, "a": {"y": 2, "x": 1}}}
	if h(r1) != h(r2) {
		t.Fatal("hash depends on map order")
	}
	r3 := fleetPlanRequest{Devices: map[string]map[string]any{"a": {"x": 1, "y": 3}, "b": {"z": []any{1, 2}}}}
	if h(r1) == h(r3) {
		t.Fatal("hash ignores content")
	}
}
