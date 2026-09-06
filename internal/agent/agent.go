package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/zyvorai/nodra/internal/auth"
	"github.com/zyvorai/nodra/internal/durable"
	"github.com/zyvorai/nodra/internal/model"
	"github.com/zyvorai/nodra/internal/mqtt"
	"github.com/zyvorai/nodra/internal/pki"
	"github.com/zyvorai/nodra/internal/queue"
	"github.com/zyvorai/nodra/internal/router"
	"github.com/zyvorai/nodra/internal/version"
	"github.com/zyvorai/nodra/pkg/connector"
)

type Agent struct {
	cfg             Config
	configPath      string
	spool           *queue.Queue[model.Event]
	localDeliveries *queue.Queue[model.Delivery]
	client          *http.Client
	http            *http.Server
	mqtt            *mqtt.Broker
	connectors      []connector.Connector
	wg              sync.WaitGroup
	cancel          context.CancelFunc
	twinsMu         sync.RWMutex
	twins           map[string]model.Twin
}

func New(cfg Config, configPath string) (*Agent, error) {
	if err := cfg.normalize(); err != nil {
		return nil, err
	}
	q, err := queue.OpenWithOptions[model.Event](filepath.Join(cfg.DataDir, "spool"), queue.Options{MaxItems: cfg.MaxSpoolEvents, MaxBytes: cfg.MaxSpoolBytes, Policy: cfg.SpoolPolicy})
	if err != nil {
		return nil, err
	}
	lq, err := queue.OpenWithOptions[model.Delivery](filepath.Join(cfg.DataDir, "local-deliveries"), queue.Options{MaxItems: max(cfg.MaxSpoolEvents/10, 1000), MaxBytes: max64(cfg.MaxSpoolBytes/10, 64<<20), Policy: "reject"})
	if err != nil {
		return nil, err
	}
	a := &Agent{cfg: cfg, configPath: configPath, spool: q, localDeliveries: lq, twins: map[string]model.Twin{}}
	if err = a.configureClient(); err != nil {
		return nil, err
	}
	a.loadTwins()
	a.http = &http.Server{Addr: cfg.Listen, Handler: a.localRoutes(), ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second}
	if cfg.MQTTListen != "" {
		a.mqtt = mqtt.New(cfg.MQTTListen, func(ctx context.Context, m mqtt.Message) error {
			p := m.Payload
			if !json.Valid(p) {
				p, _ = json.Marshal(string(p))
			}
			_, err := a.ingest(ctx, m.Topic, p, map[string]string{"x-nodra-ingress": "mqtt"})
			return err
		})
	}
	return a, nil
}
func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
func (a *Agent) configureClient() error {
	tr := &http.Transport{MaxIdleConns: 32, IdleConnTimeout: 60 * time.Second, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: a.cfg.InsecureSkipVerify}}
	if a.cfg.CAFile != "" {
		b, err := os.ReadFile(a.cfg.CAFile)
		if err != nil {
			return err
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(b) {
			return errors.New("invalid CA file")
		}
		tr.TLSClientConfig.RootCAs = pool
	}
	if a.cfg.ClientCertFile != "" && a.cfg.ClientKeyFile != "" {
		cert, err := tls.LoadX509KeyPair(a.cfg.ClientCertFile, a.cfg.ClientKeyFile)
		if err != nil {
			return err
		}
		tr.TLSClientConfig.Certificates = []tls.Certificate{cert}
	}
	a.client = &http.Client{Transport: tr, Timeout: 15 * time.Second}
	return nil
}
func (a *Agent) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	a.cancel = cancel
	if a.cfg.SiteID == "" || a.cfg.AgentToken == "" {
		if err := a.enroll(ctx); err != nil {
			return fmt.Errorf("enroll: %w", err)
		}
	}
	a.wg.Add(5)
	go a.heartbeatLoop(ctx)
	go a.flushLoop(ctx)
	go a.localDeliveryLoop(ctx)
	go a.deploymentLoop(ctx)
	go a.twinLoop(ctx)
	if a.mqtt != nil {
		a.wg.Add(1)
		go func() {
			defer a.wg.Done()
			if err := a.mqtt.Start(ctx); err != nil && ctx.Err() == nil {
				slog.Error("mqtt stopped", "error", err)
			}
		}()
	}
	if err := a.startConnectors(ctx); err != nil {
		return err
	}
	slog.Info("nodrad listening", "http", a.cfg.Listen, "mqtt", a.cfg.MQTTListen, "site_id", a.cfg.SiteID, "connectors", len(a.connectors))
	err := a.http.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
func (a *Agent) startConnectors(ctx context.Context) error {
	for _, spec := range a.cfg.Connectors {
		c, err := connector.New(spec.Type, spec.Name, spec.Config)
		if err != nil {
			return fmt.Errorf("connector %s: %w", spec.Type, err)
		}
		handler := func(ctx context.Context, ev connector.Event) error {
			headers := map[string]string{}
			for k, v := range ev.Headers {
				headers[k] = v
			}
			if headers["x-nodra-ingress"] == "" {
				headers["x-nodra-ingress"] = spec.Type
			}
			payload := json.RawMessage(ev.Payload)
			if !json.Valid(payload) {
				payload, _ = json.Marshal(string(ev.Payload))
			}
			_, err := a.ingest(ctx, ev.Topic, payload, headers)
			return err
		}
		if err := c.Start(ctx, handler); err != nil {
			_ = c.Close()
			return fmt.Errorf("start connector %s: %w", c.Name(), err)
		}
		a.connectors = append(a.connectors, c)
		slog.Info("connector started", "type", spec.Type, "name", c.Name())
	}
	return nil
}
func (a *Agent) Shutdown(ctx context.Context) error {
	if a.cancel != nil {
		a.cancel()
	}
	if a.mqtt != nil {
		_ = a.mqtt.Close()
	}
	for _, c := range a.connectors {
		_ = c.Close()
	}
	err := a.http.Shutdown(ctx)
	a.wg.Wait()
	_ = a.spool.Close()
	_ = a.localDeliveries.Close()
	return err
}
func (a *Agent) Pending() int            { return a.spool.Len() }
func (a *Agent) SpoolStats() queue.Stats { return a.spool.Stats() }
func (a *Agent) localRoutes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"status": "ok", "site_id": a.cfg.SiteID, "spool": a.spool.Stats(), "local_pending": a.localDeliveries.Len()})
	})
	mux.HandleFunc("POST /v1/publish", a.publish)
	mux.HandleFunc("POST /v1/devices", a.device)
	mux.HandleFunc("GET /v1/twins", a.localTwins)
	mux.HandleFunc("POST /v1/twins/{id}/reported", a.localTwinReported)
	return mux
}
func (a *Agent) localAuth(r *http.Request) bool {
	if a.cfg.LocalToken == "" {
		return true
	}
	h := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	return auth.EqualToken(h, a.cfg.LocalToken)
}
func (a *Agent) decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return false
	}
	return true
}
func localID() string {
	t, _ := auth.NewToken(6)
	return fmt.Sprintf("edge_%020d_%s", time.Now().UTC().UnixNano(), t)
}
func localDeliveryID(eventID string, i int) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%s:%d", eventID, i)))
	return "local_" + hex.EncodeToString(h[:10])
}
func (a *Agent) publish(w http.ResponseWriter, r *http.Request) {
	if !a.localAuth(r) {
		writeJSON(w, 401, map[string]string{"error": "unauthorized"})
		return
	}
	var in struct {
		Topic     string            `json:"topic"`
		Payload   json.RawMessage   `json:"payload"`
		Headers   map[string]string `json:"headers"`
		EventTime time.Time         `json:"event_time,omitempty"`
	}
	if !a.decode(w, r, &in) {
		return
	}
	if in.Topic == "" || len(in.Payload) == 0 {
		writeJSON(w, 400, map[string]string{"error": "topic and payload are required"})
		return
	}
	idv, err := a.ingestWithTime(r.Context(), in.Topic, in.Payload, in.Headers, in.EventTime)
	if err != nil {
		status := 500
		if errors.Is(err, queue.ErrFull) {
			status = 507
		}
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 202, map[string]any{"queued": true, "event_id": idv, "pending": a.Pending(), "spool": a.spool.Stats()})
}
func (a *Agent) ingest(ctx context.Context, topic string, payload json.RawMessage, headers map[string]string) (string, error) {
	return a.ingestWithTime(ctx, topic, payload, headers, time.Time{})
}
func (a *Agent) ingestWithTime(ctx context.Context, topic string, payload json.RawMessage, headers map[string]string, eventTime time.Time) (string, error) {
	if topic == "" || len(payload) == 0 {
		return "", errors.New("topic and payload are required")
	}
	if eventTime.IsZero() {
		eventTime = time.Now().UTC()
	}
	ev := model.Event{ID: localID(), SiteID: a.cfg.SiteID, Topic: topic, Payload: append(json.RawMessage(nil), payload...), Headers: headers, EventTime: eventTime, CreatedAt: eventTime}
	if err := a.spool.Put(ev.ID, ev); err != nil {
		return "", err
	}
	for i, rt := range a.cfg.LocalRoutes {
		if !router.Match(rt.Topic, topic) {
			continue
		}
		method := strings.ToUpper(rt.Method)
		if method == "" {
			method = "POST"
		}
		timeout := 10
		if rt.Timeout != "" {
			if d, err := time.ParseDuration(rt.Timeout); err == nil {
				timeout = max(int(d.Seconds()), 1)
			}
		}
		d := model.Delivery{ID: localDeliveryID(ev.ID, i), EventID: ev.ID, SiteID: a.cfg.SiteID, Topic: topic, TargetURL: rt.TargetURL, Method: method, Payload: ev.Payload, Headers: rt.Headers, TimeoutSecs: timeout, MaxAttempts: 100, NextAttempt: time.Now().UTC(), EventTime: eventTime, CreatedAt: time.Now().UTC()}
		if err := a.localDeliveries.Put(d.ID, d); err != nil {
			return "", err
		}
	}
	return ev.ID, nil
}
func (a *Agent) device(w http.ResponseWriter, r *http.Request) {
	if !a.localAuth(r) {
		writeJSON(w, 401, map[string]string{"error": "unauthorized"})
		return
	}
	var d model.Device
	if !a.decode(w, r, &d) {
		return
	}
	d.SiteID = a.cfg.SiteID
	b, _ := json.Marshal(d)
	resp, err := a.do(ctxOrBackground(r.Context()), "POST", "/api/v1/devices/register", b)
	if err != nil {
		writeJSON(w, 502, map[string]string{"error": err.Error()})
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(body)
}

func (a *Agent) enroll(ctx context.Context) error {
	var keyPEM, csrPEM []byte
	var err error
	if a.cfg.RequestCertificate {
		keyPEM, csrPEM, err = pki.NewClientCSR(a.cfg.SiteName)
		if err != nil {
			return err
		}
	}
	body, _ := json.Marshal(map[string]any{"name": a.cfg.SiteName, "enrollment_token": a.cfg.EnrollmentToken, "metadata": a.cfg.Metadata, "csr_pem": string(csrPEM)})
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(a.cfg.ServerURL, "/")+"/api/v1/enroll", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 201 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		return fmt.Errorf("server returned %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	var out struct {
		SiteID            string `json:"site_id"`
		AgentToken        string `json:"agent_token"`
		ClientCertificate string `json:"client_certificate"`
		CACertificate     string `json:"ca_certificate"`
	}
	if err = json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return err
	}
	a.cfg.SiteID = out.SiteID
	a.cfg.AgentToken = out.AgentToken
	a.cfg.EnrollmentToken = ""
	if out.ClientCertificate != "" && len(keyPEM) > 0 {
		certPath := filepath.Join(a.cfg.DataDir, "identity.crt")
		keyPath := filepath.Join(a.cfg.DataDir, "identity.key")
		caPath := filepath.Join(a.cfg.DataDir, "ca.crt")
		if err = durable.AtomicWrite(certPath, []byte(out.ClientCertificate), 0o600); err != nil {
			return err
		}
		if err = durable.AtomicWrite(keyPath, keyPEM, 0o600); err != nil {
			return err
		}
		if err = durable.AtomicWrite(caPath, []byte(out.CACertificate), 0o644); err != nil {
			return err
		}
		a.cfg.ClientCertFile = certPath
		a.cfg.ClientKeyFile = keyPath
		if a.cfg.CAFile == "" {
			a.cfg.CAFile = caPath
		}
		if err = a.configureClient(); err != nil {
			return err
		}
	}
	if a.configPath != "" {
		if err = SaveConfig(a.configPath, a.cfg); err != nil {
			return err
		}
	}
	return nil
}
func (a *Agent) heartbeatLoop(ctx context.Context) {
	defer a.wg.Done()
	a.heartbeat(ctx)
	t := time.NewTicker(a.cfg.Heartbeat)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.heartbeat(ctx)
		}
	}
}
func (a *Agent) heartbeat(ctx context.Context) {
	host, _ := os.Hostname()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	qs := a.spool.Stats()
	m := map[string]any{"hostname": host, "goos": runtime.GOOS, "goarch": runtime.GOARCH, "goroutines": runtime.NumGoroutine(), "heap_bytes": ms.HeapAlloc, "queue_depth": qs.Items, "queue_bytes": qs.Bytes, "queue_max_bytes": qs.MaxBytes, "local_delivery_depth": a.localDeliveries.Len()}
	b, _ := json.Marshal(map[string]any{"site_id": a.cfg.SiteID, "version": version.Version, "metrics": m})
	resp, err := a.do(ctx, "POST", "/api/v1/heartbeat", b)
	if err != nil {
		slog.Warn("heartbeat failed", "error", err)
		return
	}
	_ = resp.Body.Close()
}
func (a *Agent) flushLoop(ctx context.Context) {
	defer a.wg.Done()
	t := time.NewTicker(a.cfg.Flush)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.flush(ctx)
		}
	}
}
func (a *Agent) FlushNow(ctx context.Context) { a.flush(ctx) }
func (a *Agent) flush(ctx context.Context) {
	items, err := a.spool.List()
	if err != nil {
		return
	}
	for _, ev := range items {
		b, _ := json.Marshal(map[string]any{"event_id": ev.ID, "site_id": a.cfg.SiteID, "topic": ev.Topic, "payload": json.RawMessage(ev.Payload), "headers": ev.Headers, "event_time": ev.EventTime})
		resp, err := a.do(ctx, "POST", "/api/v1/events", b)
		if err != nil {
			return
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			_ = resp.Body.Close()
			return
		}
		_ = resp.Body.Close()
		if err = a.spool.Delete(ev.ID); err != nil {
			return
		}
	}
}
func (a *Agent) do(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(a.cfg.ServerURL, "/")+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+a.cfg.AgentToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "nodrad/"+version.Version)
	return a.client.Do(req)
}

func (a *Agent) localDeliveryLoop(ctx context.Context) {
	defer a.wg.Done()
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.flushLocal(ctx)
		}
	}
}
func (a *Agent) flushLocal(ctx context.Context) {
	items, err := a.localDeliveries.List()
	if err != nil {
		return
	}
	for _, d := range items {
		if d.NextAttempt.After(time.Now()) {
			continue
		}
		if err = a.deliverLocal(ctx, d); err == nil {
			_ = a.localDeliveries.Delete(d.ID)
			continue
		}
		d.Attempts++
		d.LastError = err.Error()
		d.NextAttempt = time.Now().Add(time.Duration(1<<min(d.Attempts, 6)) * time.Second)
		_ = a.localDeliveries.Put(d.ID, d)
	}
}
func (a *Agent) deliverLocal(ctx context.Context, d model.Delivery) error {
	to := time.Duration(d.TimeoutSecs) * time.Second
	if to <= 0 {
		to = 5 * time.Second
	}
	cctx, cancel := context.WithTimeout(ctx, to)
	defer cancel()
	body, _ := json.Marshal(map[string]any{"event_id": d.EventID, "site_id": d.SiteID, "topic": d.Topic, "payload": json.RawMessage(d.Payload), "event_time": d.EventTime})
	req, err := http.NewRequestWithContext(cctx, d.Method, d.TargetURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range d.Headers {
		req.Header.Set(k, v)
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 32<<10))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("local target returned %s", resp.Status)
	}
	return nil
}

func (a *Agent) twinLoop(ctx context.Context) {
	defer a.wg.Done()
	a.syncTwins(ctx)
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.syncTwins(ctx)
		}
	}
}
func (a *Agent) syncTwins(ctx context.Context) {
	if a.cfg.SiteID == "" {
		return
	}
	resp, err := a.do(ctx, "GET", "/api/v1/agent/twins?site_id="+a.cfg.SiteID, nil)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return
	}
	var twins []model.Twin
	if json.NewDecoder(resp.Body).Decode(&twins) != nil {
		return
	}
	a.twinsMu.Lock()
	for _, tw := range twins {
		a.twins[tw.DeviceID] = tw
	}
	a.twinsMu.Unlock()
	a.saveTwins()
}
func (a *Agent) loadTwins() {
	b, err := os.ReadFile(filepath.Join(a.cfg.DataDir, "twins.json"))
	if err != nil {
		return
	}
	var ts []model.Twin
	if json.Unmarshal(b, &ts) != nil {
		return
	}
	for _, tw := range ts {
		a.twins[tw.DeviceID] = tw
	}
}
func (a *Agent) saveTwins() {
	a.twinsMu.RLock()
	ts := make([]model.Twin, 0, len(a.twins))
	for _, tw := range a.twins {
		ts = append(ts, tw)
	}
	a.twinsMu.RUnlock()
	b, _ := json.MarshalIndent(ts, "", "  ")
	_ = durable.AtomicWrite(filepath.Join(a.cfg.DataDir, "twins.json"), b, 0o600)
}
func (a *Agent) localTwins(w http.ResponseWriter, r *http.Request) {
	if !a.localAuth(r) {
		writeJSON(w, 401, map[string]string{"error": "unauthorized"})
		return
	}
	a.twinsMu.RLock()
	out := make([]model.Twin, 0, len(a.twins))
	for _, tw := range a.twins {
		out = append(out, tw)
	}
	a.twinsMu.RUnlock()
	writeJSON(w, 200, out)
}
func (a *Agent) localTwinReported(w http.ResponseWriter, r *http.Request) {
	if !a.localAuth(r) {
		writeJSON(w, 401, map[string]string{"error": "unauthorized"})
		return
	}
	var in struct {
		Reported map[string]any `json:"reported"`
	}
	if !a.decode(w, r, &in) {
		return
	}
	idv := r.PathValue("id")
	b, _ := json.Marshal(map[string]any{"site_id": a.cfg.SiteID, "reported": in.Reported})
	resp, err := a.do(r.Context(), "POST", "/api/v1/agent/twins/"+idv+"/reported", b)
	if err != nil {
		writeJSON(w, 502, map[string]string{"error": err.Error()})
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(body)
}

func (a *Agent) deploymentLoop(ctx context.Context) {
	defer a.wg.Done()
	t := time.NewTicker(20 * time.Second)
	defer t.Stop()
	a.syncDeployments(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.syncDeployments(ctx)
		}
	}
}
func (a *Agent) syncDeployments(ctx context.Context) {
	if a.cfg.Runner != "docker" {
		return
	}
	resp, err := a.do(ctx, "GET", "/api/v1/agent/deployments?site_id="+a.cfg.SiteID, nil)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return
	}
	var deps []model.Deployment
	if json.NewDecoder(resp.Body).Decode(&deps) != nil {
		return
	}
	for _, d := range deps {
		actual, err := a.reconcileDocker(ctx, d)
		status := "running"
		msg := ""
		if d.DesiredState == "stopped" {
			status = "stopped"
		}
		if err != nil {
			status = "failed"
			msg = err.Error()
		}
		b, _ := json.Marshal(map[string]string{"site_id": a.cfg.SiteID, "status": status, "actual_state": actual, "message": msg})
		r, er := a.do(ctx, "POST", "/api/v1/agent/deployments/"+d.ID+"/status", b)
		if er == nil {
			_ = r.Body.Close()
		}
	}
}
func (a *Agent) reconcileDocker(ctx context.Context, d model.Deployment) (string, error) {
	name := "nodra-" + strings.TrimPrefix(d.ID, "dep_")
	running := exec.CommandContext(ctx, "docker", "inspect", "-f", "{{.State.Running}}", name)
	out, _ := running.Output()
	isRunning := strings.TrimSpace(string(out)) == "true"
	if d.DesiredState == "stopped" {
		if isRunning {
			if b, err := exec.CommandContext(ctx, "docker", "rm", "-f", name).CombinedOutput(); err != nil {
				return "unknown", fmt.Errorf("docker stop: %v: %s", err, strings.TrimSpace(string(b)))
			}
		}
		return "stopped", nil
	}
	if isRunning {
		return "running", nil
	}
	if out, err := exec.CommandContext(ctx, "docker", "pull", d.Image).CombinedOutput(); err != nil {
		return "stopped", fmt.Errorf("docker pull: %v: %s", err, strings.TrimSpace(string(out)))
	}
	_ = exec.CommandContext(ctx, "docker", "rm", "-f", name).Run()
	args := []string{"run", "-d", "--restart", "unless-stopped", "--name", name, "--label", "io.zyvor.nodra.deployment=" + d.ID}
	for k, v := range d.Env {
		args = append(args, "-e", k+"="+v)
	}
	for _, p := range d.Ports {
		args = append(args, "-p", p)
	}
	for _, v := range d.Volumes {
		args = append(args, "-v", v)
	}
	args = append(args, d.Image)
	args = append(args, d.Command...)
	if out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput(); err != nil {
		return "stopped", fmt.Errorf("docker run: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return "running", nil
}
func ctxOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func (a *Agent) Handler() http.Handler { return a.localRoutes() }
func (a *Agent) EnsureEnrolled(ctx context.Context) error {
	if a.cfg.SiteID != "" && a.cfg.AgentToken != "" {
		return nil
	}
	return a.enroll(ctx)
}
func (a *Agent) SiteID() string { return a.cfg.SiteID }
