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
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zyvorai/nodra/internal/model"
	"github.com/zyvorai/nodra/internal/server"
	"github.com/zyvorai/nodra/internal/transform"
	"github.com/zyvorai/nodra/pkg/ota"
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
	cfg.AllowUnauthenticatedLocal = true
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
	open := DefaultConfig()
	open.SiteID = "s"
	open.AgentToken = "t"
	open.DataDir = t.TempDir()
	a3, _ := New(open, "")
	rr = httptest.NewRecorder()
	req = httptest.NewRequest("POST", "/v1/publish", bytes.NewBufferString(`{"topic":"x","payload":1}`))
	a3.Handler().ServeHTTP(rr, req)
	if rr.Code != 401 {
		t.Fatalf("empty token want 401 got %d", rr.Code)
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
	cfg.AllowUnauthenticatedLocal = true
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
	cfg.AllowUnauthenticatedLocal = true
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
	cfg.AllowUnauthenticatedLocal = true
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

func TestLocalRouteFilterAndTransform(t *testing.T) {
	var bodies []string
	var topics []string
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		topics = append(topics, r.Header.Get("X-Nodra-Topic"))
		w.WriteHeader(204)
	}))
	defer local.Close()
	cfg := DefaultConfig()
	cfg.ServerURL = "http://127.0.0.1:1"
	cfg.SiteID = "s"
	cfg.AgentToken = "t"
	cfg.DataDir = t.TempDir()
	cfg.AllowUnauthenticatedLocal = true
	cfg.LocalRoutes = []LocalRoute{{
		Name: "MES", Topic: "factory/+/temp", TargetURL: local.URL, Method: "POST",
		Headers:   map[string]string{"X-Nodra-Topic": "will-rewrite"},
		Filter:    &transform.Filter{Min: map[string]float64{"c": 0}, Max: map[string]float64{"c": 80}},
		Transform: &transform.Transform{SetFields: map[string]any{"unit": "C"}, TopicRewrite: "mes/{{topic}}", SetHeaders: map[string]string{"X-Nodra-Topic": "mes/factory/line1/temp"}},
	}}
	a, err := New(cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	h := a.Handler()
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("POST", "/v1/publish", bytes.NewBufferString(`{"topic":"factory/line1/temp","payload":{"c":31}}`)))
	if rr.Code != 202 {
		t.Fatalf("ok %d %s", rr.Code, rr.Body.String())
	}
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("POST", "/v1/publish", bytes.NewBufferString(`{"topic":"factory/line1/temp","payload":{"c":99}}`)))
	if rr.Code != 202 {
		t.Fatalf("hot %d %s", rr.Code, rr.Body.String())
	}
	a.flushLocal(context.Background())
	if len(bodies) != 1 {
		t.Fatalf("deliveries=%d bodies=%v", len(bodies), bodies)
	}
	if !strings.Contains(bodies[0], `"unit":"C"`) {
		t.Fatalf("body=%s", bodies[0])
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

func TestAgentRotateCertificateSwapsIdentity(t *testing.T) {
	srv, err := server.New(server.Config{DataDir: t.TempDir(), AdminToken: "adm", EnrollmentToken: "enroll", PKIEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	cfg := DefaultConfig()
	cfg.ServerURL = ts.URL
	cfg.SiteName = "edge-rotate"
	cfg.EnrollmentToken = "enroll"
	cfg.RequestCertificate = true
	cfg.DataDir = filepath.Join(t.TempDir(), "agent")
	a, err := New(cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	if err = a.EnsureEnrolled(context.Background()); err != nil {
		t.Fatal(err)
	}
	certPath := filepath.Join(cfg.DataDir, "identity.crt")
	oldCertPEM, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatal(err)
	}

	if err := a.rotateCertificate(context.Background()); err != nil {
		t.Fatal(err)
	}

	newCertPEM, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(oldCertPEM, newCertPEM) {
		t.Fatal("identity.crt did not change after rotation")
	}
	if a.Revoked() {
		t.Fatal("rotating a non-revoked site must not set the revoked flag")
	}
	// The bearer token is still valid post-rotation (only the cert changed),
	// so a heartbeat should keep succeeding — proves configureClient's
	// reload didn't break the client.
	a.heartbeat(context.Background())
	if a.Revoked() {
		t.Fatal("heartbeat after rotation should not report revoked")
	}
}

func TestAgentDetectsRevocationOnHeartbeat(t *testing.T) {
	srv, err := server.New(server.Config{DataDir: t.TempDir(), AdminToken: "adm", EnrollmentToken: "enroll", PKIEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	cfg := DefaultConfig()
	cfg.ServerURL = ts.URL
	cfg.SiteName = "edge-revoke"
	cfg.EnrollmentToken = "enroll"
	cfg.RequestCertificate = true
	cfg.DataDir = filepath.Join(t.TempDir(), "agent")
	a, err := New(cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	if err = a.EnsureEnrolled(context.Background()); err != nil {
		t.Fatal(err)
	}
	if a.Revoked() {
		t.Fatal("freshly enrolled agent must not start out revoked")
	}

	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/sites/"+a.SiteID()+"/revoke", nil)
	req.Header.Set("Authorization", "Bearer adm")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	a.heartbeat(context.Background())
	if !a.Revoked() {
		t.Fatal("agent should have detected revocation from the heartbeat response")
	}

	// Rotation must refuse to proceed once revoked.
	if err := a.rotateCertificate(context.Background()); err == nil {
		t.Fatal("rotateCertificate should fail for a revoked site")
	}
}

func writeFakeDocker(t *testing.T, dir string, script string) string {
	t.Helper()
	path := filepath.Join(dir, "docker")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReconcileDockerAlreadyRunning(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "calls.log")
	bin := writeFakeDocker(t, dir, "#!/bin/sh\necho \"$@\" >>\""+logPath+"\"\nif [ \"$1\" = inspect ]; then echo true; exit 0; fi\necho unexpected >&2; exit 1\n")
	t.Setenv("NODRA_DOCKER_BIN", bin)
	cfg := DefaultConfig()
	cfg.DataDir = t.TempDir()
	a, err := New(cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	state, err := a.reconcileDocker(context.Background(), model.Deployment{
		ID: "dep_abc", Image: "busybox:latest", DesiredState: "running",
	})
	if err != nil || state != "running" {
		t.Fatalf("state=%s err=%v", state, err)
	}
	b, _ := os.ReadFile(logPath)
	if !bytes.Contains(b, []byte("inspect")) {
		t.Fatalf("log=%s", b)
	}
	if bytes.Contains(b, []byte("pull")) || bytes.Contains(b, []byte("run")) {
		t.Fatalf("should not pull/run when already running: %s", b)
	}
}

func TestReconcileDockerStartWhenStopped(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "calls.log")
	bin := writeFakeDocker(t, dir, "#!/bin/sh\necho \"$@\" >>\""+logPath+"\"\nif [ \"$1\" = inspect ]; then echo false; exit 0; fi\nif [ \"$1\" = pull ] || [ \"$1\" = rm ] || [ \"$1\" = run ]; then exit 0; fi\necho unexpected >&2; exit 1\n")
	t.Setenv("NODRA_DOCKER_BIN", bin)
	cfg := DefaultConfig()
	cfg.DataDir = t.TempDir()
	a, err := New(cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	state, err := a.reconcileDocker(context.Background(), model.Deployment{
		ID: "dep_start1", Image: "alpine:3.19", DesiredState: "running",
		Env: map[string]string{"K": "V"}, Ports: []string{"8080:80"},
	})
	if err != nil || state != "running" {
		t.Fatalf("state=%s err=%v", state, err)
	}
	b, _ := os.ReadFile(logPath)
	if !bytes.Contains(b, []byte("pull alpine:3.19")) {
		t.Fatalf("missing pull: %s", b)
	}
	if !bytes.Contains(b, []byte("run -d")) || !bytes.Contains(b, []byte("--name nodra-start1")) {
		t.Fatalf("missing run: %s", b)
	}
	if !bytes.Contains(b, []byte("-e K=V")) || !bytes.Contains(b, []byte("-p 8080:80")) {
		t.Fatalf("missing env/ports: %s", b)
	}
}

func TestReconcileDockerStopRunning(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "calls.log")
	bin := writeFakeDocker(t, dir, "#!/bin/sh\necho \"$@\" >>\""+logPath+"\"\nif [ \"$1\" = inspect ]; then echo true; exit 0; fi\nif [ \"$1\" = rm ]; then exit 0; fi\necho unexpected >&2; exit 1\n")
	t.Setenv("NODRA_DOCKER_BIN", bin)
	cfg := DefaultConfig()
	cfg.DataDir = t.TempDir()
	a, err := New(cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	state, err := a.reconcileDocker(context.Background(), model.Deployment{
		ID: "dep_x", Image: "busybox:latest", DesiredState: "stopped",
	})
	if err != nil || state != "stopped" {
		t.Fatalf("state=%s err=%v", state, err)
	}
	b, _ := os.ReadFile(logPath)
	if !bytes.Contains(b, []byte("rm -f nodra-x")) {
		t.Fatalf("missing rm: %s", b)
	}
}

func TestLocalAuditTrailRecordsEnrollAndDeniedAccess(t *testing.T) {
	srv, _ := server.New(server.Config{DataDir: t.TempDir(), AdminToken: "adm", EnrollmentToken: "enroll"})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	cfg := DefaultConfig()
	cfg.ServerURL = ts.URL
	cfg.SiteName = "edge-audit"
	cfg.EnrollmentToken = "enroll"
	cfg.DataDir = filepath.Join(t.TempDir(), "agent")
	cfg.LocalToken = "local-secret"
	a, err := New(cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	if err = a.EnsureEnrolled(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Unauthorized local request should be denied and audited.
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/publish", bytes.NewBufferString(`{"topic":"x","payload":1}`))
	a.Handler().ServeHTTP(rr, req)
	if rr.Code != 401 {
		t.Fatalf("expected 401, got %d", rr.Code)
	}

	// Authorized audit list read.
	rr = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/v1/audit?limit=50", nil)
	req.Header.Set("Authorization", "Bearer local-secret")
	a.Handler().ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("audit list: %d %s", rr.Code, rr.Body.String())
	}
	var out struct {
		Entries []map[string]any `json:"entries"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v (%s)", err, rr.Body.String())
	}
	seen := map[string]map[string]any{}
	for _, e := range out.Entries {
		action, _ := e["action"].(string)
		seen[action] = e
	}
	enrollEntry, ok := seen["enroll"]
	if !ok || enrollEntry["result"] != "ok" {
		t.Errorf("expected an ok enroll audit entry, got %+v", enrollEntry)
	}
	denied, ok := seen["local.publish"]
	if !ok || denied["result"] != "denied" {
		t.Errorf("expected a denied local.publish audit entry, got %+v", denied)
	}
}

func writeFakeCosign(t *testing.T, dir string, script string) string {
	t.Helper()
	path := filepath.Join(dir, "cosign")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReconcileDockerEnforceRefusesUnverifiedImage(t *testing.T) {
	dir := t.TempDir()
	dockerLog := filepath.Join(dir, "docker.log")
	dockerBinPath := writeFakeDocker(t, dir, "#!/bin/sh\necho \"$@\" >>\""+dockerLog+"\"\nif [ \"$1\" = inspect ]; then echo false; exit 0; fi\nexit 0\n")
	t.Setenv("NODRA_DOCKER_BIN", dockerBinPath)
	cosignBinPath := writeFakeCosign(t, dir, "#!/bin/sh\necho unverified >&2\nexit 1\n")
	t.Setenv("NODRA_COSIGN_BIN", cosignBinPath)
	cfg := DefaultConfig()
	cfg.DataDir = t.TempDir()
	cfg.SignatureMode = "enforce"
	a, err := New(cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	state, err := a.reconcileDocker(context.Background(), model.Deployment{
		ID: "dep_enf", Image: "busybox:latest", DesiredState: "running",
	})
	if err == nil {
		t.Fatal("expected enforce mode to refuse an unverified image")
	}
	if state != "stopped" {
		t.Fatalf("state=%s", state)
	}
	b, _ := os.ReadFile(dockerLog)
	if bytes.Contains(b, []byte("pull")) {
		t.Fatalf("enforce mode should not have pulled: %s", b)
	}
}

func TestReconcileDockerWarnPullsDespiteUnverifiedImage(t *testing.T) {
	dir := t.TempDir()
	dockerLog := filepath.Join(dir, "docker.log")
	dockerBinPath := writeFakeDocker(t, dir, "#!/bin/sh\necho \"$@\" >>\""+dockerLog+"\"\nif [ \"$1\" = inspect ]; then echo false; exit 0; fi\nexit 0\n")
	t.Setenv("NODRA_DOCKER_BIN", dockerBinPath)
	cosignBinPath := writeFakeCosign(t, dir, "#!/bin/sh\necho unverified >&2\nexit 1\n")
	t.Setenv("NODRA_COSIGN_BIN", cosignBinPath)
	cfg := DefaultConfig()
	cfg.DataDir = t.TempDir()
	cfg.SignatureMode = "warn"
	a, err := New(cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	state, err := a.reconcileDocker(context.Background(), model.Deployment{
		ID: "dep_warn", Image: "busybox:latest", DesiredState: "running",
	})
	if err != nil || state != "running" {
		t.Fatalf("warn mode should still pull/run: state=%s err=%v", state, err)
	}
	b, _ := os.ReadFile(dockerLog)
	if !bytes.Contains(b, []byte("pull")) {
		t.Fatalf("warn mode should have pulled: %s", b)
	}
}

func TestReconcileDockerVerifiedImagePulls(t *testing.T) {
	dir := t.TempDir()
	dockerLog := filepath.Join(dir, "docker.log")
	dockerBinPath := writeFakeDocker(t, dir, "#!/bin/sh\necho \"$@\" >>\""+dockerLog+"\"\nif [ \"$1\" = inspect ]; then echo false; exit 0; fi\nexit 0\n")
	t.Setenv("NODRA_DOCKER_BIN", dockerBinPath)
	cosignBinPath := writeFakeCosign(t, dir, "#!/bin/sh\nexit 0\n")
	t.Setenv("NODRA_COSIGN_BIN", cosignBinPath)
	cfg := DefaultConfig()
	cfg.DataDir = t.TempDir()
	cfg.SignatureMode = "enforce"
	a, err := New(cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	state, err := a.reconcileDocker(context.Background(), model.Deployment{
		ID: "dep_ok", Image: "busybox:latest", DesiredState: "running",
	})
	if err != nil || state != "running" {
		t.Fatalf("state=%s err=%v", state, err)
	}
	b, _ := os.ReadFile(dockerLog)
	if !bytes.Contains(b, []byte("pull")) {
		t.Fatalf("expected a pull after successful verification: %s", b)
	}
}

func TestLocalOTAStatusForwardsAndValidates(t *testing.T) {
	srv, _ := server.New(server.Config{DataDir: t.TempDir(), AdminToken: "adm", EnrollmentToken: "enroll"})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	cfg := DefaultConfig()
	cfg.ServerURL = ts.URL
	cfg.SiteName = "edge-ota"
	cfg.EnrollmentToken = "enroll"
	cfg.DataDir = filepath.Join(t.TempDir(), "agent")
	cfg.LocalToken = "local"
	a, err := New(cfg, filepath.Join(t.TempDir(), "nodrad.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = a.EnsureEnrolled(context.Background()); err != nil {
		t.Fatal(err)
	}

	regReq, _ := http.NewRequest("POST", ts.URL+"/api/v1/devices/register", bytes.NewBufferString(`{"id":"gw-1","site_id":"`+a.SiteID()+`","name":"Gateway","protocol":"modbus"}`))
	regReq.Header.Set("Authorization", "Bearer "+a.cfg.AgentToken)
	regReq.Header.Set("Content-Type", "application/json")
	regResp, err := http.DefaultClient.Do(regReq)
	if err != nil {
		t.Fatal(err)
	}
	regResp.Body.Close()
	if regResp.StatusCode != 201 {
		t.Fatalf("device register status=%d", regResp.StatusCode)
	}

	statusBody, _ := json.Marshal(map[string]any{"status": ota.Status{
		UpdateID: "upd-1", DeviceID: "gw-1", State: ota.StatePending,
	}})
	req := httptest.NewRequest("POST", "/v1/devices/gw-1/ota/status", bytes.NewReader(statusBody))
	req.Header.Set("Authorization", "Bearer local")
	rr := httptest.NewRecorder()
	a.Handler().ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("local ota status=%d body=%s", rr.Code, rr.Body.String())
	}

	// An invalid status (unknown state) must be rejected locally, without
	// ever reaching the control plane.
	badBody, _ := json.Marshal(map[string]any{"status": map[string]any{
		"update_id": "upd-1", "device_id": "gw-1", "state": "not-a-real-state",
	}})
	req2 := httptest.NewRequest("POST", "/v1/devices/gw-1/ota/status", bytes.NewReader(badBody))
	req2.Header.Set("Authorization", "Bearer local")
	rr2 := httptest.NewRecorder()
	a.Handler().ServeHTTP(rr2, req2)
	if rr2.Code != 400 {
		t.Fatalf("expected 400 for invalid state, got %d body=%s", rr2.Code, rr2.Body.String())
	}

	getReq, _ := http.NewRequest("GET", ts.URL+"/api/v1/devices/gw-1/ota", nil)
	getReq.Header.Set("Authorization", "Bearer adm")
	getResp, err := http.DefaultClient.Do(getReq)
	if err != nil {
		t.Fatal(err)
	}
	defer getResp.Body.Close()
	gb, _ := io.ReadAll(getResp.Body)
	if getResp.StatusCode != 200 || !bytes.Contains(gb, []byte(`"pending"`)) {
		t.Fatalf("expected control plane to have the forwarded status, got %d %s", getResp.StatusCode, gb)
	}
}
