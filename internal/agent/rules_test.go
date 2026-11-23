// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-2.0

package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const agentRules = `{
  "rules": [{
    "name": "hot", "topic": "plant/+/temp", "field": "c", "op": "above", "raise": 90, "clear": 80,
    "on_fire": [{"type": "alert", "severity": "critical"}],
    "on_clear": [{"type": "emit", "topic": "plant/alarms/cleared"}]
  }]
}`

func newRulesAgent(t *testing.T, dir, rulesPath string, now *time.Time) *Agent {
	t.Helper()
	cfg := DefaultConfig()
	cfg.SiteName = "edge"
	cfg.DataDir = dir
	cfg.LocalToken = "local"
	cfg.MQTTListen = ""
	cfg.RulesFile = rulesPath
	a, err := New(cfg, filepath.Join(dir, "nodrad.json"))
	if err != nil {
		t.Fatal(err)
	}
	a.rulesNow = func() time.Time { return *now }
	return a
}

func spoolTopics(t *testing.T, a *Agent) []string {
	t.Helper()
	items, err := a.spool.List()
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range items {
		out = append(out, e.Topic)
	}
	return out
}

func count(ts []string, topic string) int {
	n := 0
	for _, x := range ts {
		if x == topic {
			n++
		}
	}
	return n
}

func TestRulesFireOnceAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	rp := filepath.Join(dir, "rules.json")
	if err := os.WriteFile(rp, []byte(agentRules), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	data := filepath.Join(dir, "data")
	a := newRulesAgent(t, data, rp, &now)
	ctx := context.Background()
	for _, v := range []string{`{"c":95}`, `{"c":96}`} {
		now = now.Add(time.Second)
		if _, err := a.ingest(ctx, "plant/a/temp", json.RawMessage(v), nil); err != nil {
			t.Fatal(err)
		}
	}
	ts := spoolTopics(t, a)
	if count(ts, "nodra/rules/alert/hot") != 1 {
		t.Fatalf("alert events: %v", ts)
	}
	if a.rulesFiring() != 1 {
		t.Fatal("not firing")
	}
	if _, err := os.Stat(filepath.Join(data, rulesStateFile)); err != nil {
		t.Fatalf("state not persisted: %v", err)
	}
	_ = a.spool.Close()
	_ = a.localDeliveries.Close()
	_ = a.auditLog.Close()

	// "restart": a new agent on the same data dir
	now = now.Add(time.Minute)
	b := newRulesAgent(t, data, rp, &now)
	if b.rulesFiring() != 1 {
		t.Fatal("firing state lost across restart")
	}
	if _, err := b.ingest(ctx, "plant/a/temp", json.RawMessage(`{"c":99}`), nil); err != nil {
		t.Fatal(err)
	}
	ts = spoolTopics(t, b)
	if count(ts, "nodra/rules/alert/hot") != 1 {
		t.Fatalf("re-fired after restart: %v", ts)
	}
	now = now.Add(time.Second)
	if _, err := b.ingest(ctx, "plant/a/temp", json.RawMessage(`{"c":10}`), nil); err != nil {
		t.Fatal(err)
	}
	if ts = spoolTopics(t, b); count(ts, "plant/alarms/cleared") != 1 || b.rulesFiring() != 0 {
		t.Fatalf("clear after restart: %v", ts)
	}
}

func TestRulesEmittedEventsDoNotRetrigger(t *testing.T) {
	dir := t.TempDir()
	rp := filepath.Join(dir, "rules.json")
	loop := `{"rules":[{"name":"loop","topic":"#","field":"c","op":"above","raise":1,"clear":0,
	  "on_fire":[{"type":"emit","topic":"x/y","transform":{"set_fields":{"c":100}}}]}]}`
	_ = os.WriteFile(rp, []byte(loop), 0o600)
	now := time.Now()
	a := newRulesAgent(t, filepath.Join(dir, "d"), rp, &now)
	if _, err := a.ingest(context.Background(), "s/1", json.RawMessage(`{"c":5}`), nil); err != nil {
		t.Fatal(err)
	}
	if n := len(spoolTopics(t, a)); n != 2 {
		t.Fatalf("spool has %d events, want 2 (input + one emitted)", n)
	}
}

func TestRulesFileInvalidFailsStartupWithLine(t *testing.T) {
	dir := t.TempDir()
	rp := filepath.Join(dir, "rules.json")
	_ = os.WriteFile(rp, []byte("{\n\"rules\":[{\n\"name\":\"a\",\n\"bogus\":1}]}"), 0o600)
	cfg := DefaultConfig()
	cfg.SiteName, cfg.DataDir, cfg.MQTTListen, cfg.RulesFile = "e", filepath.Join(dir, "d"), "", rp
	_, err := New(cfg, "")
	if err == nil || !strings.Contains(err.Error(), "line 4") {
		t.Fatalf("err=%v", err)
	}
	cfg.RulesFile = filepath.Join(dir, "missing.json")
	if _, err = New(cfg, ""); err == nil {
		t.Fatal("missing file accepted")
	}
}

func TestRulesCorruptStateStartsClean(t *testing.T) {
	dir := t.TempDir()
	rp := filepath.Join(dir, "rules.json")
	_ = os.WriteFile(rp, []byte(agentRules), 0o600)
	data := filepath.Join(dir, "data")
	_ = os.MkdirAll(data, 0o750)
	_ = os.WriteFile(filepath.Join(data, rulesStateFile), []byte(`{"version":1,"rules":{"hot":{`), 0o600)
	now := time.Now()
	a := newRulesAgent(t, data, rp, &now)
	if a.rulesFiring() != 0 {
		t.Fatal("garbage state restored")
	}
	if _, err := os.Stat(filepath.Join(data, rulesStateFile+".corrupt")); err != nil {
		t.Fatal("corrupt file not quarantined")
	}
}

func TestRulesEvaluateEndpoint(t *testing.T) {
	dir := t.TempDir()
	rp := filepath.Join(dir, "rules.json")
	_ = os.WriteFile(rp, []byte(agentRules), 0o600)
	now := time.Now()
	a := newRulesAgent(t, filepath.Join(dir, "d"), rp, &now)
	post := func(body string) (int, map[string]any) {
		req := httptest.NewRequest("POST", "/v1/rules/evaluate", bytes.NewBufferString(body))
		req.Header.Set("Authorization", "Bearer local")
		rr := httptest.NewRecorder()
		a.Handler().ServeHTTP(rr, req)
		var m map[string]any
		_ = json.Unmarshal(rr.Body.Bytes(), &m)
		return rr.Code, m
	}
	// loaded rules
	code, m := post(`{"start":"2026-11-23T12:00:00Z","messages":[{"topic":"plant/a/temp","payload":{"c":95}},{"topic":"plant/a/temp","payload":{"c":50}}]}`)
	rep, _ := m["report"].(map[string]any)
	if code != 200 || rep["fires"] != float64(1) || rep["clears"] != float64(1) {
		t.Fatalf("%d %v", code, m)
	}
	if a.rulesFiring() != 0 || len(spoolTopics(t, a)) != 0 {
		t.Fatal("dry run had side effects")
	}
	// inline rules as string with a bad field: line numbers
	code, m = post(`{"rules":"{\n\"rules\":[{\"name\":\"a\",\n\"nope\":1}]}","messages":[]}`)
	if code != 400 || !strings.Contains(string(mustJSON(m)), `"line":3`) {
		t.Fatalf("%d %v", code, m)
	}
	// inline object rules
	code, _ = post(`{"rules":` + agentRules + `,"messages":[]}`)
	if code != 200 {
		t.Fatal(code)
	}
	for _, bad := range []string{`{`, `{"rules":5}`, `{"messages":[{"topic":1}]}`, `{"unknown":1}`} {
		if code, _ = post(bad); code != 400 {
			t.Fatalf("%s -> %d", bad, code)
		}
	}
	// auth
	req := httptest.NewRequest("POST", "/v1/rules/evaluate", strings.NewReader(`{}`))
	rr := httptest.NewRecorder()
	a.Handler().ServeHTTP(rr, req)
	if rr.Code != 401 {
		t.Fatalf("unauth=%d", rr.Code)
	}
	// status endpoint
	req = httptest.NewRequest("GET", "/v1/rules", nil)
	req.Header.Set("Authorization", "Bearer local")
	rr = httptest.NewRecorder()
	a.Handler().ServeHTTP(rr, req)
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), `"enabled":true`) {
		t.Fatal(rr.Body.String())
	}
}

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }
