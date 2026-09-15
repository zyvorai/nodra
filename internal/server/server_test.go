// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zyvorai/nodra/internal/model"
	"github.com/zyvorai/nodra/internal/pki"
)

type testClient struct {
	base, admin string
	t           *testing.T
}

func (c testClient) req(method, path string, body any, token string) (int, []byte) {
	c.t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.base+path, r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}
func newTestServer(t *testing.T) (*Server, *httptest.Server, testClient) {
	t.Helper()
	s, err := New(Config{DataDir: t.TempDir(), AdminToken: "adm", EnrollmentToken: "enroll", MaxBodyBytes: 2048})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return s, ts, testClient{ts.URL, "adm", t}
}

func TestReadyz(t *testing.T) {
	_, _, c := newTestServer(t)
	code, body := c.req(http.MethodGet, "/readyz", nil, "")
	if code != 200 {
		t.Fatalf("readyz status=%d body=%s", code, body)
	}
	var out map[string]string
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	if out["status"] != "ready" {
		t.Fatalf("unexpected readyz payload: %v", out)
	}
	if out["store"] != "file" {
		t.Fatalf("expected file store, got %q", out["store"])
	}
}

func enrollSite(t *testing.T, c testClient) (string, string) {
	code, b := c.req("POST", "/api/v1/enroll", map[string]any{"name": "factory", "enrollment_token": "enroll"}, "")
	if code != 201 {
		t.Fatalf("enroll %d %s", code, b)
	}
	var v struct {
		SiteID     string `json:"site_id"`
		AgentToken string `json:"agent_token"`
	}
	_ = json.Unmarshal(b, &v)
	if v.SiteID == "" || v.AgentToken == "" {
		t.Fatal("missing credentials")
	}
	return v.SiteID, v.AgentToken
}
func TestEnrollmentHeartbeatAndOverview(t *testing.T) {
	_, _, c := newTestServer(t)
	site, tok := enrollSite(t, c)
	code, b := c.req("POST", "/api/v1/heartbeat", map[string]any{"site_id": site, "version": "0.2.0", "metrics": map[string]any{"queue_depth": 3}}, tok)
	if code != 200 {
		t.Fatalf("heartbeat %d %s", code, b)
	}
	code, b = c.req("GET", "/api/v1/overview", nil, "adm")
	if code != 200 {
		t.Fatal(code)
	}
	var o map[string]any
	_ = json.Unmarshal(b, &o)
	if o["online_sites"].(float64) != 1 {
		t.Fatalf("overview=%s", b)
	}
}
func TestAdminAuth(t *testing.T) {
	_, _, c := newTestServer(t)
	code, _ := c.req("GET", "/api/v1/sites", nil, "")
	if code != 401 {
		t.Fatalf("want 401 got %d", code)
	}
	code, _ = c.req("GET", "/api/v1/sites", nil, "wrong")
	if code != 401 {
		t.Fatalf("want 401 got %d", code)
	}
	code, _ = c.req("GET", "/api/v1/sites", nil, "adm")
	if code != 200 {
		t.Fatalf("want 200 got %d", code)
	}
}
func TestRouteDelivery(t *testing.T) {
	s, _, c := newTestServer(t)
	site, tok := enrollSite(t, c)
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Method != "POST" {
			t.Errorf("method=%s", r.Method)
		}
		w.WriteHeader(204)
	}))
	defer target.Close()
	code, b := c.req("POST", "/api/v1/routes", map[string]any{"name": "sensor webhook", "topic": "sensors/+/telemetry", "target_url": target.URL, "method": "POST", "enabled": true, "retry_max": 3}, "adm")
	if code != 201 {
		t.Fatalf("route %d %s", code, b)
	}
	code, b = c.req("POST", "/api/v1/events", map[string]any{"site_id": site, "topic": "sensors/plc/telemetry", "payload": map[string]any{"temperature": 42}}, tok)
	if code != 202 {
		t.Fatalf("event %d %s", code, b)
	}
	s.ProcessOnce(context.Background())
	if hits.Load() != 1 {
		t.Fatalf("hits=%d", hits.Load())
	}
	code, b = c.req("GET", "/api/v1/overview", nil, "adm")
	var o map[string]any
	_ = json.Unmarshal(b, &o)
	if o["pending_deliveries"].(float64) != 0 {
		t.Fatalf("pending=%v", o["pending_deliveries"])
	}
}
func TestFailedDeliveryBecomesAlert(t *testing.T) {
	s, _, c := newTestServer(t)
	site, tok := enrollSite(t, c)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "no", 500) }))
	defer target.Close()
	_, _ = c.req("POST", "/api/v1/routes", map[string]any{"name": "bad", "topic": "#", "target_url": target.URL, "method": "POST", "enabled": true, "retry_max": 1}, "adm")
	_, _ = c.req("POST", "/api/v1/events", map[string]any{"site_id": site, "topic": "x", "payload": map[string]any{"v": 1}}, tok)
	s.ProcessOnce(context.Background())
	code, b := c.req("GET", "/api/v1/alerts", nil, "adm")
	if code != 200 {
		t.Fatal(code)
	}
	if !strings.Contains(string(b), "delivery_failed") {
		t.Fatalf("alerts=%s", b)
	}
}
func TestDeviceAndDeploymentLifecycle(t *testing.T) {
	_, _, c := newTestServer(t)
	site, tok := enrollSite(t, c)
	code, b := c.req("POST", "/api/v1/devices/register", map[string]any{"site_id": site, "name": "PLC-01", "protocol": "modbus"}, tok)
	if code != 201 {
		t.Fatalf("device %d %s", code, b)
	}
	code, b = c.req("POST", "/api/v1/deployments", map[string]any{"site_id": site, "name": "vision", "version": "1.0", "image": "example/vision:1.0"}, "adm")
	if code != 201 {
		t.Fatalf("deploy %d %s", code, b)
	}
	var dep map[string]any
	_ = json.Unmarshal(b, &dep)
	id := dep["id"].(string)
	code, _ = c.req("GET", "/api/v1/agent/deployments?site_id="+site, nil, tok)
	if code != 200 {
		t.Fatal(code)
	}
	code, b = c.req("POST", "/api/v1/agent/deployments/"+id+"/status", map[string]any{"site_id": site, "status": "failed", "message": "docker unavailable"}, tok)
	if code != 200 {
		t.Fatalf("status %d %s", code, b)
	}
	_, b = c.req("GET", "/api/v1/alerts", nil, "adm")
	if !strings.Contains(string(b), "deployment_failed") {
		t.Fatalf("alerts=%s", b)
	}
}
func TestBodyLimitAndDashboardSecurityHeaders(t *testing.T) {
	_, _, c := newTestServer(t)
	huge := strings.Repeat("x", 3000)
	code, _ := c.req("POST", "/api/v1/enroll", map[string]any{"name": huge, "enrollment_token": "enroll"}, "")
	if code != 400 {
		t.Fatalf("body limit code=%d", code)
	}
	resp, err := http.Get(c.base + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.Header.Get("Content-Security-Policy") == "" {
		t.Fatal("missing CSP")
	}
	if resp.StatusCode != 200 {
		t.Fatal(resp.StatusCode)
	}
}
func TestOfflineSiteCalculation(t *testing.T) {
	s, _, c := newTestServer(t)
	site, _ := enrollSite(t, c)
	_ = s.store.UpdateSite(site, func(v *model.Site) { v.LastSeen = time.Now().Add(-3 * time.Minute) })
	code, b := c.req("GET", "/api/v1/sites", nil, "adm")
	if code != 200 {
		t.Fatal(code)
	}
	if !strings.Contains(string(b), "offline") {
		t.Fatalf("sites=%s", b)
	}
}

func TestDuplicateEventIDDoesNotDuplicateRouteDelivery(t *testing.T) {
	s, _, c := newTestServer(t)
	site, tok := enrollSite(t, c)
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["event_id"] != "edge-fixed-1" {
			t.Errorf("event_id=%v", body["event_id"])
		}
		hits.Add(1)
		w.WriteHeader(204)
	}))
	defer target.Close()
	_, _ = c.req("POST", "/api/v1/routes", map[string]any{"name": "all", "topic": "#", "target_url": target.URL, "method": "POST", "enabled": true, "retry_max": 2, "timeout_seconds": 2}, "adm")
	event := map[string]any{"event_id": "edge-fixed-1", "site_id": site, "topic": "machine/value", "payload": map[string]any{"v": 7}}
	code, _ := c.req("POST", "/api/v1/events", event, tok)
	if code != 202 {
		t.Fatal(code)
	}
	code, b := c.req("POST", "/api/v1/events", event, tok)
	if code != 202 || !strings.Contains(string(b), "duplicate") {
		t.Fatalf("duplicate response %d %s", code, b)
	}
	s.ProcessOnce(context.Background())
	if hits.Load() != 1 {
		t.Fatalf("duplicate delivered %d times", hits.Load())
	}
}
func TestRouteValidation(t *testing.T) {
	_, _, c := newTestServer(t)
	for _, body := range []map[string]any{{"name": "x", "topic": "#", "target_url": "file:///tmp/x", "method": "POST", "enabled": true}, {"name": "x", "topic": "#", "target_url": "https://example.com", "method": "DELETE", "enabled": true}, {"name": "x", "topic": "#", "target_url": "https://example.com", "method": "POST", "enabled": true, "retry_max": 21}, {"name": "x", "topic": "#", "target_url": "https://example.com", "method": "POST", "enabled": true, "timeout_seconds": 61}} {
		code, _ := c.req("POST", "/api/v1/routes", body, "adm")
		if code != 400 {
			t.Fatalf("expected 400 for %#v got %d", body, code)
		}
	}
}

func TestEventPreservesOriginalTime(t *testing.T) {
	s, _, c := newTestServer(t)
	site, tok := enrollSite(t, c)
	eventTime := time.Date(2026, 9, 6, 1, 2, 3, 0, time.UTC)
	var got map[string]any
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(204)
	}))
	defer target.Close()
	_, _ = c.req("POST", "/api/v1/routes", map[string]any{"name": "all", "topic": "#", "target_url": target.URL, "method": "POST", "enabled": true, "retry_max": 1}, "adm")
	code, b := c.req("POST", "/api/v1/events", map[string]any{"event_id": "time-1", "site_id": site, "topic": "sensor/t", "payload": map[string]any{"v": 1}, "event_time": eventTime}, tok)
	if code != 202 {
		t.Fatalf("%d %s", code, b)
	}
	s.ProcessOnce(context.Background())
	if got["event_time"] != eventTime.Format(time.RFC3339) {
		t.Fatalf("event_time=%v", got["event_time"])
	}
	evs := s.store.EventsSince(eventTime.Add(-time.Second))
	if len(evs) != 1 || !evs[0].EventTime.Equal(eventTime) || !evs[0].IngestedAt.After(eventTime) {
		t.Fatalf("events=%+v", evs)
	}
}

func TestDurableQueueFailureDoesNotAckEvent(t *testing.T) {
	s, err := New(Config{DataDir: t.TempDir(), AdminToken: "adm", EnrollmentToken: "enroll", DeliveryMaxItems: 1})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	c := testClient{ts.URL, "adm", t}
	site, tok := enrollSite(t, c)
	_, _ = c.req("POST", "/api/v1/routes", map[string]any{"name": "r1", "topic": "#", "target_url": "http://127.0.0.1:1/a", "method": "POST", "enabled": true, "retry_max": 3}, "adm")
	_, _ = c.req("POST", "/api/v1/routes", map[string]any{"name": "r2", "topic": "#", "target_url": "http://127.0.0.1:1/b", "method": "POST", "enabled": true, "retry_max": 3}, "adm")
	code, _ := c.req("POST", "/api/v1/events", map[string]any{"event_id": "must-retry", "site_id": site, "topic": "x", "payload": map[string]any{"a": 1}}, tok)
	if code != 503 {
		t.Fatalf("want 503, got %d", code)
	}
	if s.store.HasEvent("must-retry") {
		t.Fatal("event was ACK-committed despite delivery persistence failure")
	}
}

func TestDeadLetterReplay(t *testing.T) {
	s, _, c := newTestServer(t)
	site, tok := enrollSite(t, c)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "down", 500) }))
	defer target.Close()
	_, _ = c.req("POST", "/api/v1/routes", map[string]any{"name": "bad", "topic": "#", "target_url": target.URL, "method": "POST", "enabled": true, "retry_max": 1}, "adm")
	_, _ = c.req("POST", "/api/v1/events", map[string]any{"event_id": "dlq-1", "site_id": site, "topic": "x", "payload": 1}, tok)
	s.ProcessOnce(context.Background())
	code, b := c.req("GET", "/api/v1/deadletters", nil, "adm")
	if code != 200 {
		t.Fatal(code)
	}
	var dls []model.DeadLetter
	_ = json.Unmarshal(b, &dls)
	if len(dls) != 1 {
		t.Fatalf("%s", b)
	}
	code, _ = c.req("POST", "/api/v1/deadletters/"+dls[0].Delivery.ID+"/replay", nil, "adm")
	if code != 202 {
		t.Fatal(code)
	}
	if s.deliveries.Len() != 1 || s.dlq.Len() != 0 {
		t.Fatalf("pending=%d dlq=%d", s.deliveries.Len(), s.dlq.Len())
	}
}

func TestDeviceTwinDesiredReported(t *testing.T) {
	_, _, c := newTestServer(t)
	site, tok := enrollSite(t, c)
	code, b := c.req("POST", "/api/v1/devices/register", map[string]any{"id": "plc-1", "site_id": site, "name": "PLC", "protocol": "modbus"}, tok)
	if code != 201 {
		t.Fatalf("%d %s", code, b)
	}
	code, b = c.req("PUT", "/api/v1/twins/plc-1/desired", map[string]any{"desired": map[string]any{"speed": 1200}}, "adm")
	if code != 200 {
		t.Fatalf("%d %s", code, b)
	}
	code, b = c.req("GET", "/api/v1/agent/twins?site_id="+site, nil, tok)
	if code != 200 || !strings.Contains(string(b), "1200") {
		t.Fatalf("%d %s", code, b)
	}
	code, b = c.req("POST", "/api/v1/agent/twins/plc-1/reported", map[string]any{"site_id": site, "reported": map[string]any{"speed": 1198}}, tok)
	if code != 200 || !strings.Contains(string(b), "1198") {
		t.Fatalf("%d %s", code, b)
	}
}

func TestPKIEnrollmentSignsCSR(t *testing.T) {
	s, err := New(Config{DataDir: t.TempDir(), AdminToken: "adm", EnrollmentToken: "enroll", PKIEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	c := testClient{ts.URL, "adm", t}
	_, csr, err := pki.NewClientCSR("bootstrap")
	if err != nil {
		t.Fatal(err)
	}
	code, b := c.req("POST", "/api/v1/enroll", map[string]any{"name": "secure", "enrollment_token": "enroll", "csr_pem": string(csr)}, "")
	if code != 201 {
		t.Fatalf("%d %s", code, b)
	}
	if !strings.Contains(string(b), "client_certificate") || !strings.Contains(string(b), "ca_certificate") {
		t.Fatalf("missing certs: %s", b)
	}
}

func enrollPKISite(t *testing.T, c testClient, name string) (siteID, agentToken string) {
	t.Helper()
	_, csr, err := pki.NewClientCSR(name)
	if err != nil {
		t.Fatal(err)
	}
	code, b := c.req("POST", "/api/v1/enroll", map[string]any{"name": name, "enrollment_token": "enroll", "csr_pem": string(csr)}, "")
	if code != 201 {
		t.Fatalf("enroll %d %s", code, b)
	}
	var out struct {
		SiteID     string `json:"site_id"`
		AgentToken string `json:"agent_token"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out.SiteID == "" || out.AgentToken == "" {
		t.Fatalf("missing credentials: %s", b)
	}
	return out.SiteID, out.AgentToken
}

func TestSiteRevokeCRLAndHeartbeat403(t *testing.T) {
	s, err := New(Config{DataDir: t.TempDir(), AdminToken: "adm", EnrollmentToken: "enroll", PKIEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	c := testClient{ts.URL, "adm", t}
	siteID, tok := enrollPKISite(t, c, "crl-site")

	// Bad token still gets a generic 401, not the revoked-specific 403.
	code, _ := c.req("POST", "/api/v1/heartbeat", map[string]any{"site_id": siteID}, "wrong-token")
	if code != 401 {
		t.Fatalf("bad-token heartbeat = %d, want 401", code)
	}

	code, b := c.req("POST", "/api/v1/sites/"+siteID+"/revoke", nil, "adm")
	if code != 200 {
		t.Fatalf("revoke %d %s", code, b)
	}

	// A revoked site's own (otherwise-valid) token now gets 403 site_revoked.
	code, b = c.req("POST", "/api/v1/heartbeat", map[string]any{"site_id": siteID}, tok)
	if code != 403 || !strings.Contains(string(b), "site_revoked") {
		t.Fatalf("revoked heartbeat = %d %s, want 403 site_revoked", code, b)
	}

	site, ok := s.store.Site(siteID)
	if !ok {
		t.Fatal("site missing after revoke")
	}
	serial, ok := new(big.Int).SetString(site.CertificateSerial, 16)
	if !ok {
		t.Fatalf("bad stored serial %q", site.CertificateSerial)
	}

	resp, err := http.Get(ts.URL + "/api/v1/ca/crl")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	crlBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("crl status=%d body=%s", resp.StatusCode, crlBytes)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/pkix-crl" {
		t.Fatalf("crl content-type=%q", ct)
	}
	block, _ := pem.Decode(crlBytes)
	if block == nil {
		t.Fatal("invalid CRL PEM")
	}
	crl, err := x509.ParseRevocationList(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range crl.RevokedCertificateEntries {
		if e.SerialNumber.Cmp(serial) == 0 {
			found = true
		}
	}
	if !found {
		t.Fatalf("serial %s not present in CRL entries %v", site.CertificateSerial, crl.RevokedCertificateEntries)
	}
}

func TestSiteRotateIssuesNewSerial(t *testing.T) {
	s, err := New(Config{DataDir: t.TempDir(), AdminToken: "adm", EnrollmentToken: "enroll", PKIEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	c := testClient{ts.URL, "adm", t}
	siteID, tok := enrollPKISite(t, c, "rotate-site")

	site, ok := s.store.Site(siteID)
	if !ok {
		t.Fatal("site missing after enroll")
	}
	oldSerial := site.CertificateSerial
	if oldSerial == "" {
		t.Fatal("enrollment did not issue a certificate")
	}

	_, csr2, err := pki.NewClientCSR(siteID)
	if err != nil {
		t.Fatal(err)
	}
	code, b := c.req("POST", "/api/v1/sites/"+siteID+"/rotate", map[string]any{"csr_pem": string(csr2)}, tok)
	if code != 200 {
		t.Fatalf("rotate %d %s", code, b)
	}
	var out struct {
		CertificateSerial string `json:"certificate_serial"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out.CertificateSerial == "" || out.CertificateSerial == oldSerial {
		t.Fatalf("rotate did not return a fresh serial: old=%s got=%s", oldSerial, out.CertificateSerial)
	}
	site, ok = s.store.Site(siteID)
	if !ok || site.CertificateSerial != out.CertificateSerial {
		t.Fatalf("store not updated: site=%+v want serial=%s", site, out.CertificateSerial)
	}

	// A revoked site cannot rotate — the same trust boundary as agentSite
	// enforces for heartbeat/events.
	code, b = c.req("POST", "/api/v1/sites/"+siteID+"/revoke", nil, "adm")
	if code != 200 {
		t.Fatalf("revoke %d %s", code, b)
	}
	_, csr3, err := pki.NewClientCSR(siteID)
	if err != nil {
		t.Fatal(err)
	}
	code, b = c.req("POST", "/api/v1/sites/"+siteID+"/rotate", map[string]any{"csr_pem": string(csr3)}, tok)
	if code != 403 || !strings.Contains(string(b), "site_revoked") {
		t.Fatalf("rotate after revoke = %d %s, want 403 site_revoked", code, b)
	}
}

func TestPolicyPackDeniesDeployment(t *testing.T) {
	_, _, c := newTestServer(t)
	site, _ := enrollSite(t, c)

	code, b := c.req("POST", "/api/v1/policy-packs", map[string]any{"name": "fleet", "allowed_images": []string{"ghcr.io/zyvorai/*"}, "enabled": true}, "adm")
	if code != 201 {
		t.Fatalf("policy create %d %s", code, b)
	}

	code, b = c.req("POST", "/api/v1/deployments", map[string]any{"site_id": site, "name": "app", "version": "v1", "image": "docker.io/evil/image:latest"}, "adm")
	if code != 403 {
		t.Fatalf("expected 403 for denied image, got %d %s", code, b)
	}

	code, b = c.req("POST", "/api/v1/deployments", map[string]any{"site_id": site, "name": "app", "version": "v1", "image": "ghcr.io/zyvorai/nodra:v1"}, "adm")
	if code != 201 {
		t.Fatalf("expected 201 for allowed image, got %d %s", code, b)
	}
}

func TestPolicyPackScopedToSite(t *testing.T) {
	_, _, c := newTestServer(t)
	site1, _ := enrollSite(t, c)
	site2, _ := enrollSite(t, c)

	code, b := c.req("POST", "/api/v1/policy-packs", map[string]any{"name": "site-only", "site_id": site1, "allowed_images": []string{"ghcr.io/zyvorai/*"}, "enabled": true}, "adm")
	if code != 201 {
		t.Fatalf("policy create %d %s", code, b)
	}

	code, b = c.req("POST", "/api/v1/deployments", map[string]any{"site_id": site1, "name": "app", "version": "v1", "image": "docker.io/evil/image:latest"}, "adm")
	if code != 403 {
		t.Fatalf("expected 403 for site1 (constrained), got %d %s", code, b)
	}

	code, b = c.req("POST", "/api/v1/deployments", map[string]any{"site_id": site2, "name": "app", "version": "v1", "image": "docker.io/evil/image:latest"}, "adm")
	if code != 201 {
		t.Fatalf("expected 201 for site2 (unconstrained), got %d %s", code, b)
	}
}
