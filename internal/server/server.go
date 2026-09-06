package server

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
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zyvorai/nodra/internal/auth"
	"github.com/zyvorai/nodra/internal/model"
	"github.com/zyvorai/nodra/internal/pki"
	"github.com/zyvorai/nodra/internal/queue"
	"github.com/zyvorai/nodra/internal/router"
	"github.com/zyvorai/nodra/internal/store"
	"github.com/zyvorai/nodra/internal/telemetry"
	"github.com/zyvorai/nodra/internal/version"
	webassets "github.com/zyvorai/nodra/web"
)

type Config struct {
	Listen            string
	DataDir           string
	AdminToken        string
	AdminUser         string
	AdminPassword     string
	ViewerToken       string
	ViewerUser        string
	ViewerPassword    string
	EnrollmentToken   string
	PublicRead        bool
	MaxBodyBytes      int64
	WorkerInterval    time.Duration
	WorkerConcurrency int
	DeliveryMaxItems  int
	DeliveryMaxBytes  int64
	PKIEnabled        bool
	PKIDir            string
	TLSCertFile       string
	TLSKeyFile        string
	ClientCAFile      string
	StoreDriver       string // file|postgres
	DatabaseURL       string
}

type Server struct {
	cfg        Config
	store      store.Backend
	deliveries *queue.Queue[model.Delivery]
	dlq        *queue.Queue[model.DeadLetter]
	metrics    *telemetry.Metrics
	activity   *activityLog
	client     *http.Client
	http       *http.Server
	wg         sync.WaitGroup
	cancel     context.CancelFunc
	inflight   sync.Map
	ca         *pki.CA
}

func New(cfg Config) (*Server, error) {
	if cfg.Listen == "" {
		cfg.Listen = ":8080"
	}
	if cfg.DataDir == "" {
		cfg.DataDir = "./data"
	}
	if cfg.AdminUser == "" {
		cfg.AdminUser = "admin"
	}
	if cfg.AdminPassword == "" {
		cfg.AdminPassword = cfg.AdminToken
	}
	if cfg.ViewerUser == "" {
		cfg.ViewerUser = "viewer"
	}
	if cfg.ViewerPassword == "" && cfg.ViewerToken != "" {
		cfg.ViewerPassword = cfg.ViewerToken
	}
	if cfg.StoreDriver == "" {
		cfg.StoreDriver = "file"
	}
	if cfg.MaxBodyBytes == 0 {
		cfg.MaxBodyBytes = 1 << 20
	}
	if cfg.WorkerInterval == 0 {
		cfg.WorkerInterval = time.Second
	}
	if cfg.WorkerConcurrency <= 0 {
		cfg.WorkerConcurrency = 8
	}
	if cfg.DeliveryMaxItems <= 0 {
		cfg.DeliveryMaxItems = 250000
	}
	if cfg.DeliveryMaxBytes <= 0 {
		cfg.DeliveryMaxBytes = 4 << 30
	}
	if err := os.MkdirAll(cfg.DataDir, 0o750); err != nil {
		return nil, err
	}
	st, err := store.OpenBackend(cfg.StoreDriver, filepath.Join(cfg.DataDir, "state.json"), cfg.DatabaseURL)
	if err != nil {
		return nil, err
	}
	q, err := queue.OpenWithOptions[model.Delivery](filepath.Join(cfg.DataDir, "deliveries"), queue.Options{MaxItems: cfg.DeliveryMaxItems, MaxBytes: cfg.DeliveryMaxBytes, Policy: "reject"})
	if err != nil {
		return nil, err
	}
	dlq, err := queue.OpenWithOptions[model.DeadLetter](filepath.Join(cfg.DataDir, "deadletters"), queue.Options{MaxItems: cfg.DeliveryMaxItems, MaxBytes: cfg.DeliveryMaxBytes, Policy: "reject"})
	if err != nil {
		return nil, err
	}
	s := &Server{cfg: cfg, store: st, deliveries: q, dlq: dlq, metrics: &telemetry.Metrics{}, activity: &activityLog{}, client: &http.Client{Transport: &http.Transport{MaxIdleConns: 128, MaxIdleConnsPerHost: 16, IdleConnTimeout: 90 * time.Second}}}
	if cfg.PKIEnabled {
		dir := cfg.PKIDir
		if dir == "" {
			dir = filepath.Join(cfg.DataDir, "pki")
		}
		s.ca, err = pki.EnsureCA(dir)
		if err != nil {
			return nil, err
		}
	}
	s.http = &http.Server{Addr: cfg.Listen, Handler: s.routes(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}
	if cfg.ClientCAFile != "" {
		b, er := os.ReadFile(cfg.ClientCAFile)
		if er != nil {
			return nil, er
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(b) {
			return nil, errors.New("invalid client CA")
		}
		s.http.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12, ClientCAs: pool, ClientAuth: tls.VerifyClientCertIfGiven}
	}
	return s, nil
}
func (s *Server) Handler() http.Handler { return s.routes() }
func (s *Server) Start(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.wg.Add(1)
	go s.worker(ctx)
	var err error
	if s.cfg.TLSCertFile != "" && s.cfg.TLSKeyFile != "" {
		err = s.http.ListenAndServeTLS(s.cfg.TLSCertFile, s.cfg.TLSKeyFile)
	} else {
		err = s.http.ListenAndServe()
	}
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
func (s *Server) Shutdown(ctx context.Context) error {
	if s.cancel != nil {
		s.cancel()
	}
	err := s.http.Shutdown(ctx)
	s.wg.Wait()
	_ = s.deliveries.Close()
	_ = s.dlq.Close()
	_ = s.store.Close()
	return err
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]string{"status": "ready"}) })
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		ds := s.deliveries.Stats()
		qs := s.dlq.Stats()
		_, _ = io.WriteString(w, s.metrics.Prometheus()+fmt.Sprintf("# TYPE nodra_pending_deliveries gauge\nnodra_pending_deliveries %d\n# TYPE nodra_delivery_queue_bytes gauge\nnodra_delivery_queue_bytes %d\n# TYPE nodra_dead_letters gauge\nnodra_dead_letters %d\n", ds.Items, ds.Bytes, qs.Items))
	})
	mux.HandleFunc("GET /api/v1/version", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]string{"name": "nodra", "version": version.Version, "commit": version.Commit, "build_date": version.BuildDate})
	})
	mux.HandleFunc("POST /api/v1/auth/login", s.login)
	mux.HandleFunc("GET /api/v1/auth/me", s.authMe)
	mux.HandleFunc("POST /api/v1/enroll", s.enroll)
	mux.HandleFunc("POST /api/v1/heartbeat", s.heartbeat)
	mux.HandleFunc("POST /api/v1/events", s.events)
	mux.HandleFunc("POST /api/v1/devices/register", s.registerDevice)
	mux.HandleFunc("GET /api/v1/agent/deployments", s.agentDeployments)
	mux.HandleFunc("POST /api/v1/agent/deployments/{id}/status", s.agentDeploymentStatus)
	mux.HandleFunc("GET /api/v1/agent/twins", s.agentTwins)
	mux.HandleFunc("POST /api/v1/agent/twins/{id}/reported", s.agentTwinReported)
	mux.Handle("GET /api/v1/overview", s.admin(http.HandlerFunc(s.overview)))
	mux.Handle("GET /api/v1/sites", s.admin(http.HandlerFunc(s.sites)))
	mux.Handle("POST /api/v1/sites/{id}/revoke", s.admin(http.HandlerFunc(s.siteRevoke)))
	mux.Handle("GET /api/v1/routes", s.admin(http.HandlerFunc(s.routesList)))
	mux.Handle("POST /api/v1/routes", s.admin(http.HandlerFunc(s.routeCreate)))
	mux.Handle("DELETE /api/v1/routes/{id}", s.admin(http.HandlerFunc(s.routeDelete)))
	mux.Handle("GET /api/v1/devices", s.admin(http.HandlerFunc(s.devices)))
	mux.Handle("GET /api/v1/twins", s.admin(http.HandlerFunc(s.twins)))
	mux.Handle("PUT /api/v1/twins/{id}/desired", s.admin(http.HandlerFunc(s.twinDesired)))
	mux.Handle("GET /api/v1/deployments", s.admin(http.HandlerFunc(s.deployments)))
	mux.Handle("POST /api/v1/deployments", s.admin(http.HandlerFunc(s.deploymentCreate)))
	mux.Handle("PATCH /api/v1/deployments/{id}", s.admin(http.HandlerFunc(s.deploymentPatch)))
	mux.Handle("DELETE /api/v1/deployments/{id}", s.admin(http.HandlerFunc(s.deploymentDelete)))
	mux.Handle("GET /api/v1/alerts", s.admin(http.HandlerFunc(s.alerts)))
	mux.Handle("POST /api/v1/alerts/{id}/resolve", s.admin(http.HandlerFunc(s.alertResolve)))
	mux.Handle("GET /api/v1/events", s.admin(http.HandlerFunc(s.eventsList)))
	mux.Handle("GET /api/v1/deadletters", s.admin(http.HandlerFunc(s.deadletters)))
	mux.Handle("POST /api/v1/deadletters/{id}/replay", s.admin(http.HandlerFunc(s.deadletterReplay)))
	mux.Handle("DELETE /api/v1/deadletters/{id}", s.admin(http.HandlerFunc(s.deadletterDelete)))
	mux.Handle("GET /api/v1/activity", s.admin(http.HandlerFunc(s.activityList)))
	mux.Handle("POST /api/v1/activity", s.admin(http.HandlerFunc(s.activityPost)))
	mux.HandleFunc("GET /assets/{name}", s.asset)
	mux.HandleFunc("GET /", s.index)
	return s.securityHeaders(s.requestLog(mux))
}
func (s *Server) requestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		slog.Debug("request", "method", r.Method, "path", r.URL.Path, "duration", time.Since(start))
	})
}
func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self'; script-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}
func (s *Server) admin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.PublicRead && r.Method == http.MethodGet {
			next.ServeHTTP(w, r)
			return
		}
		role := s.roleForBearer(bearer(r))
		if role == "" {
			if s.cfg.AdminToken == "" && s.cfg.ViewerToken == "" {
				errorJSON(w, 503, "admin authentication is not configured")
				return
			}
			errorJSON(w, 401, "unauthorized")
			return
		}
		if !isSafeMethod(r.Method) && role != "admin" {
			errorJSON(w, 403, "admin role required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func isSafeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

func (s *Server) roleForBearer(tok string) string {
	if tok == "" {
		return ""
	}
	if s.cfg.AdminToken != "" && auth.EqualToken(tok, s.cfg.AdminToken) {
		return "admin"
	}
	if s.cfg.ViewerToken != "" && auth.EqualToken(tok, s.cfg.ViewerToken) {
		return "viewer"
	}
	return ""
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	user := strings.TrimSpace(in.Username)
	if s.cfg.AdminToken != "" && s.cfg.AdminPassword != "" &&
		auth.EqualToken(user, s.cfg.AdminUser) && auth.EqualToken(in.Password, s.cfg.AdminPassword) {
		writeJSON(w, 200, map[string]any{
			"token": s.cfg.AdminToken,
			"user":  map[string]string{"username": s.cfg.AdminUser, "role": "admin"},
		})
		return
	}
	if s.cfg.ViewerToken != "" && s.cfg.ViewerPassword != "" &&
		auth.EqualToken(user, s.cfg.ViewerUser) && auth.EqualToken(in.Password, s.cfg.ViewerPassword) {
		writeJSON(w, 200, map[string]any{
			"token": s.cfg.ViewerToken,
			"user":  map[string]string{"username": s.cfg.ViewerUser, "role": "viewer"},
		})
		return
	}
	if s.cfg.AdminToken == "" && s.cfg.ViewerToken == "" {
		errorJSON(w, 503, "login is not configured")
		return
	}
	errorJSON(w, 401, "invalid username or password")
}

func (s *Server) authMe(w http.ResponseWriter, r *http.Request) {
	role := s.roleForBearer(bearer(r))
	if role == "" {
		writeJSON(w, 200, map[string]any{"authenticated": false})
		return
	}
	user := s.cfg.AdminUser
	if role == "viewer" {
		user = s.cfg.ViewerUser
	}
	writeJSON(w, 200, map[string]any{
		"authenticated": true,
		"user":          map[string]string{"username": user, "role": role},
	})
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Bearer ") {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
}
func (s *Server) agentSite(r *http.Request, siteID string) (model.Site, bool) {
	site, ok := s.store.Site(siteID)
	if !ok || site.Revoked {
		return model.Site{}, false
	}
	if r.TLS != nil && len(r.TLS.PeerCertificates) > 0 && r.TLS.PeerCertificates[0].Subject.CommonName == siteID {
		return site, true
	}
	return site, auth.EqualHash(site.TokenHash, bearer(r))
}
func (s *Server) decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, s.cfg.MaxBodyBytes)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		errorJSON(w, 400, "invalid JSON: "+err.Error())
		return false
	}
	return true
}
func id(prefix string) string { t, _ := auth.NewToken(8); return prefix + "_" + t }
func deterministicDeliveryID(eventID, routeID string) string {
	h := sha256.Sum256([]byte(eventID + "\x00" + routeID))
	return "dlv_" + hex.EncodeToString(h[:12])
}

func (s *Server) enroll(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name            string            `json:"name"`
		EnrollmentToken string            `json:"enrollment_token"`
		Metadata        map[string]string `json:"metadata"`
		CSRPem          string            `json:"csr_pem,omitempty"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	if s.cfg.EnrollmentToken == "" || !auth.EqualToken(in.EnrollmentToken, s.cfg.EnrollmentToken) {
		errorJSON(w, 401, "invalid enrollment token")
		return
	}
	if strings.TrimSpace(in.Name) == "" {
		errorJSON(w, 400, "site name is required")
		return
	}
	tok, err := auth.NewToken(24)
	if err != nil {
		errorJSON(w, 500, "token generation failed")
		return
	}
	now := time.Now().UTC()
	site := model.Site{ID: id("site"), Name: in.Name, TokenHash: auth.Hash(tok), Status: "online", CreatedAt: now, LastSeen: now, Metadata: in.Metadata}
	out := map[string]any{"agent_token": tok}
	if s.ca != nil && in.CSRPem != "" {
		signed, er := pki.SignCSR(s.ca, []byte(in.CSRPem), site.ID, 90*24*time.Hour)
		if er != nil {
			errorJSON(w, 400, "invalid certificate request: "+er.Error())
			return
		}
		site.CertificateSerial = signed.Serial
		site.CertificateExpiresAt = signed.ExpiresAt
		out["client_certificate"] = string(signed.CertPEM)
		out["ca_certificate"] = string(s.ca.CertPEM)
	}
	if err = s.store.AddSite(site); err != nil {
		errorJSON(w, 507, "site persistence failed: "+err.Error())
		return
	}
	s.metrics.Enrollments.Add(1)
	out["site_id"] = site.ID
	publicSite := site
	publicSite.TokenHash = ""
	out["site"] = publicSite
	s.note("ok", "control-plane", "", site.ID, site.Name, "enroll", "site enrolled and agent token issued", map[string]any{"metadata": in.Metadata})
	writeJSON(w, 201, out)
}
func (s *Server) heartbeat(w http.ResponseWriter, r *http.Request) {
	var in struct {
		SiteID  string         `json:"site_id"`
		Version string         `json:"version"`
		Metrics map[string]any `json:"metrics"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	if _, ok := s.agentSite(r, in.SiteID); !ok {
		errorJSON(w, 401, "unauthorized agent")
		return
	}
	err := s.store.UpdateSite(in.SiteID, func(v *model.Site) {
		v.Status = "online"
		v.LastSeen = time.Now().UTC()
		v.Version = in.Version
		v.Metrics = in.Metrics
	})
	if err != nil {
		errorJSON(w, 507, "heartbeat persistence failed")
		return
	}
	s.metrics.Heartbeats.Add(1)
	writeJSON(w, 200, map[string]any{"ok": true, "server_time": time.Now().UTC()})
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	var in struct {
		EventID   string            `json:"event_id"`
		SiteID    string            `json:"site_id"`
		Topic     string            `json:"topic"`
		Payload   json.RawMessage   `json:"payload"`
		Headers   map[string]string `json:"headers"`
		EventTime time.Time         `json:"event_time"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	if _, ok := s.agentSite(r, in.SiteID); !ok {
		errorJSON(w, 401, "unauthorized agent")
		return
	}
	if in.Topic == "" || len(in.Payload) == 0 {
		errorJSON(w, 400, "topic and payload are required")
		return
	}
	eventID := in.EventID
	if eventID == "" {
		eventID = id("evt")
	}
	if s.store.HasEvent(eventID) {
		writeJSON(w, 202, map[string]any{"accepted": true, "event_id": eventID, "duplicate": true, "matched_routes": 0})
		return
	}
	eventTime := in.EventTime
	if eventTime.IsZero() {
		eventTime = time.Now().UTC()
	}
	now := time.Now().UTC()
	matched := 0
	for _, rt := range s.store.Routes() {
		if !rt.Enabled || (rt.SiteID != "" && rt.SiteID != in.SiteID) || !router.Match(rt.Topic, in.Topic) {
			continue
		}
		d := model.Delivery{ID: deterministicDeliveryID(eventID, rt.ID), RouteID: rt.ID, EventID: eventID, SiteID: in.SiteID, Topic: in.Topic, TargetURL: rt.TargetURL, Method: rt.Method, Payload: in.Payload, Headers: mergeHeaders(in.Headers, rt.Headers), TimeoutSecs: rt.TimeoutSecs, MaxAttempts: max(rt.RetryMax, 1), NextAttempt: now, EventTime: eventTime, CreatedAt: now}
		if err := s.deliveries.Put(d.ID, d); err != nil {
			errorJSON(w, 503, "delivery queue unavailable: "+err.Error())
			return
		}
		matched++
	}
	ev := model.Event{ID: eventID, SiteID: in.SiteID, Topic: in.Topic, Payload: in.Payload, Headers: in.Headers, EventTime: eventTime, IngestedAt: now, CreatedAt: eventTime}
	if err := s.store.AddEvent(ev); err != nil {
		errorJSON(w, 507, "event persistence failed: "+err.Error())
		return
	}
	s.metrics.Events.Add(1)
	// Keep event auto-logs light — detailed chapter lines come from nodra-sim.
	if matched > 0 {
		s.note("info", "agent", "", in.SiteID, "", "event", "telemetry accepted on "+in.Topic, map[string]any{
			"event_id": ev.ID, "matched_routes": matched,
		})
	}
	writeJSON(w, 202, map[string]any{"accepted": true, "event_id": ev.ID, "matched_routes": matched, "event_time": eventTime, "ingested_at": now})
}
func mergeHeaders(a, b map[string]string) map[string]string {
	if len(a) == 0 && len(b) == 0 {
		return nil
	}
	out := map[string]string{}
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

func (s *Server) registerDevice(w http.ResponseWriter, r *http.Request) {
	var in model.Device
	if !s.decode(w, r, &in) {
		return
	}
	if _, ok := s.agentSite(r, in.SiteID); !ok {
		errorJSON(w, 401, "unauthorized agent")
		return
	}
	if in.Name == "" || in.Protocol == "" {
		errorJSON(w, 400, "name and protocol are required")
		return
	}
	if in.ID == "" {
		in.ID = id("dev")
	}
	in.Status = "online"
	if in.CreatedAt.IsZero() {
		in.CreatedAt = time.Now().UTC()
	}
	in.LastSeen = time.Now().UTC()
	if err := s.store.AddDevice(in); err != nil {
		errorJSON(w, 507, err.Error())
		return
	}
	if _, ok := s.store.Twin(in.ID); !ok {
		_ = s.store.SetTwin(model.Twin{DeviceID: in.ID, SiteID: in.SiteID, UpdatedAt: time.Now().UTC()})
	}
	s.note("ok", "agent", "", in.SiteID, "", "device.register", "device registered: "+in.Name, map[string]any{"device_id": in.ID, "protocol": in.Protocol})
	writeJSON(w, 201, in)
}
func (s *Server) overview(w http.ResponseWriter, r *http.Request) {
	st := s.store.Snapshot()
	online := 0
	cut := time.Now().Add(-90 * time.Second)
	for _, v := range st.Sites {
		if !v.Revoked && v.LastSeen.After(cut) {
			online++
		}
	}
	ds := s.deliveries.Stats()
	qs := s.dlq.Stats()
	writeJSON(w, 200, map[string]any{"sites": len(st.Sites), "online_sites": online, "devices": len(st.Devices), "twins": len(st.Twins), "routes": len(st.Routes), "deployments": len(st.Deployments), "open_alerts": countOpen(st.Alerts), "pending_deliveries": ds.Items, "delivery_queue_bytes": ds.Bytes, "dead_letters": qs.Items, "events": len(st.Events)})
}
func countOpen(a []model.Alert) int {
	n := 0
	for _, v := range a {
		if !v.Resolved {
			n++
		}
	}
	return n
}
func (s *Server) sites(w http.ResponseWriter, r *http.Request) {
	v := s.store.Sites()
	cut := time.Now().Add(-90 * time.Second)
	for i := range v {
		if v[i].Revoked {
			v[i].Status = "revoked"
		} else if v[i].LastSeen.Before(cut) {
			v[i].Status = "offline"
		}
	}
	for i := range v {
		v[i].TokenHash = ""
	}
	writeJSON(w, 200, asJSONList(v))
}
func (s *Server) siteRevoke(w http.ResponseWriter, r *http.Request) {
	idv := r.PathValue("id")
	if err := s.store.UpdateSite(idv, func(v *model.Site) { v.Revoked = true; v.Status = "revoked" }); err != nil {
		errorJSON(w, 404, "site not found")
		return
	}
	writeJSON(w, 200, map[string]bool{"revoked": true})
}
func (s *Server) routesList(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, asJSONList(s.store.Routes()))
}
func (s *Server) routeCreate(w http.ResponseWriter, r *http.Request) {
	var in model.Route
	if !s.decode(w, r, &in) {
		return
	}
	if in.Name == "" || in.Topic == "" || in.TargetURL == "" {
		errorJSON(w, 400, "name, topic and target_url are required")
		return
	}
	u, err := url.ParseRequestURI(in.TargetURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		errorJSON(w, 400, "target_url must be an absolute http(s) URL")
		return
	}
	if in.Method == "" {
		in.Method = "POST"
	}
	in.Method = strings.ToUpper(in.Method)
	if in.Method != "POST" && in.Method != "PUT" && in.Method != "PATCH" {
		errorJSON(w, 400, "method must be POST, PUT or PATCH")
		return
	}
	if in.RetryMax == 0 {
		in.RetryMax = 5
	}
	if in.RetryMax < 1 || in.RetryMax > 20 {
		errorJSON(w, 400, "retry_max must be between 1 and 20")
		return
	}
	if in.TimeoutSecs == 0 {
		in.TimeoutSecs = 10
	}
	if in.TimeoutSecs < 1 || in.TimeoutSecs > 60 {
		errorJSON(w, 400, "timeout_seconds must be between 1 and 60")
		return
	}
	in.ID = id("route")
	in.CreatedAt = time.Now().UTC()
	if err := s.store.AddRoute(in); err != nil {
		errorJSON(w, 507, err.Error())
		return
	}
	writeJSON(w, 201, in)
}
func (s *Server) routeDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteRoute(r.PathValue("id")); err != nil {
		errorJSON(w, 404, "route not found")
		return
	}
	w.WriteHeader(204)
}
func (s *Server) devices(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, asJSONList(s.store.Devices()))
}
func (s *Server) twins(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, asJSONList(s.store.Snapshot().Twins))
}
func (s *Server) twinDesired(w http.ResponseWriter, r *http.Request) {
	dev, ok := s.store.Device(r.PathValue("id"))
	if !ok {
		errorJSON(w, 404, "device not found")
		return
	}
	var in struct {
		Desired map[string]any `json:"desired"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	tw, _ := s.store.Twin(dev.ID)
	tw.DeviceID = dev.ID
	tw.SiteID = dev.SiteID
	tw.Desired = in.Desired
	tw.DesiredVersion++
	tw.UpdatedAt = time.Now().UTC()
	if err := s.store.SetTwin(tw); err != nil {
		errorJSON(w, 507, err.Error())
		return
	}
	writeJSON(w, 200, tw)
}
func (s *Server) agentTwins(w http.ResponseWriter, r *http.Request) {
	siteID := r.URL.Query().Get("site_id")
	if _, ok := s.agentSite(r, siteID); !ok {
		errorJSON(w, 401, "unauthorized agent")
		return
	}
	writeJSON(w, 200, s.store.TwinsForSite(siteID))
}
func (s *Server) agentTwinReported(w http.ResponseWriter, r *http.Request) {
	var in struct {
		SiteID   string         `json:"site_id"`
		Reported map[string]any `json:"reported"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	if _, ok := s.agentSite(r, in.SiteID); !ok {
		errorJSON(w, 401, "unauthorized agent")
		return
	}
	dev, ok := s.store.Device(r.PathValue("id"))
	if !ok || dev.SiteID != in.SiteID {
		errorJSON(w, 404, "device not found")
		return
	}
	tw, _ := s.store.Twin(dev.ID)
	tw.DeviceID = dev.ID
	tw.SiteID = in.SiteID
	tw.Reported = in.Reported
	tw.ReportedVersion++
	tw.UpdatedAt = time.Now().UTC()
	if err := s.store.SetTwin(tw); err != nil {
		errorJSON(w, 507, err.Error())
		return
	}
	writeJSON(w, 200, tw)
}
func (s *Server) deployments(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, asJSONList(s.store.Deployments()))
}
func (s *Server) deploymentCreate(w http.ResponseWriter, r *http.Request) {
	var in model.Deployment
	if !s.decode(w, r, &in) {
		return
	}
	if in.SiteID == "" || in.Name == "" || in.Version == "" || in.Image == "" {
		errorJSON(w, 400, "site_id, name, version and image are required")
		return
	}
	if _, ok := s.store.Site(in.SiteID); !ok {
		errorJSON(w, 404, "site not found")
		return
	}
	now := time.Now().UTC()
	in.ID = id("dep")
	in.Status = "queued"
	if in.DesiredState == "" {
		in.DesiredState = "running"
	}
	in.ActualState = "unknown"
	in.CreatedAt = now
	in.UpdatedAt = now
	if err := s.store.AddDeployment(in); err != nil {
		errorJSON(w, 507, err.Error())
		return
	}
	writeJSON(w, 201, in)
}
func (s *Server) deploymentPatch(w http.ResponseWriter, r *http.Request) {
	idv := r.PathValue("id")
	var in struct {
		Version      *string `json:"version"`
		Image        *string `json:"image"`
		DesiredState *string `json:"desired_state"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	if in.Version == nil && in.Image == nil && in.DesiredState == nil {
		errorJSON(w, 400, "version, image or desired_state is required")
		return
	}
	if in.DesiredState != nil {
		ds := strings.ToLower(strings.TrimSpace(*in.DesiredState))
		if ds != "running" && ds != "stopped" {
			errorJSON(w, 400, "desired_state must be running or stopped")
			return
		}
		*in.DesiredState = ds
	}
	if err := s.store.UpdateDeployment(idv, func(d *model.Deployment) {
		if in.Version != nil && *in.Version != "" {
			d.Version = *in.Version
		}
		if in.Image != nil && *in.Image != "" {
			d.Image = *in.Image
		}
		if in.DesiredState != nil {
			d.DesiredState = *in.DesiredState
			d.Status = "queued"
		}
		d.UpdatedAt = time.Now().UTC()
	}); err != nil {
		errorJSON(w, 404, "deployment not found")
		return
	}
	dep, _ := s.store.Deployment(idv)
	writeJSON(w, 200, dep)
}
func (s *Server) deploymentDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteDeployment(r.PathValue("id")); err != nil {
		errorJSON(w, 404, "deployment not found")
		return
	}
	w.WriteHeader(204)
}
func (s *Server) alerts(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, asJSONList(s.store.Alerts()))
}
func (s *Server) alertResolve(w http.ResponseWriter, r *http.Request) {
	if err := s.store.ResolveAlert(r.PathValue("id")); err != nil {
		errorJSON(w, 404, "alert not found")
		return
	}
	writeJSON(w, 200, map[string]bool{"resolved": true})
}
func (s *Server) eventsList(w http.ResponseWriter, r *http.Request) {
	mins, _ := strconv.Atoi(r.URL.Query().Get("minutes"))
	if mins <= 0 {
		mins = 60
	}
	writeJSON(w, 200, asJSONList(s.store.EventsSince(time.Now().Add(-time.Duration(mins)*time.Minute))))
}
func (s *Server) agentDeployments(w http.ResponseWriter, r *http.Request) {
	siteID := r.URL.Query().Get("site_id")
	if _, ok := s.agentSite(r, siteID); !ok {
		errorJSON(w, 401, "unauthorized agent")
		return
	}
	var out []model.Deployment
	for _, d := range s.store.Deployments() {
		if d.SiteID == siteID {
			out = append(out, d)
		}
	}
	writeJSON(w, 200, out)
}
func (s *Server) agentDeploymentStatus(w http.ResponseWriter, r *http.Request) {
	var in struct {
		SiteID      string `json:"site_id"`
		Status      string `json:"status"`
		ActualState string `json:"actual_state"`
		Message     string `json:"message"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	if _, ok := s.agentSite(r, in.SiteID); !ok {
		errorJSON(w, 401, "unauthorized agent")
		return
	}
	idv := r.PathValue("id")
	dep, exists := s.store.Deployment(idv)
	if !exists {
		errorJSON(w, 404, "deployment not found")
		return
	}
	if dep.SiteID != in.SiteID {
		errorJSON(w, 403, "deployment belongs to another site")
		return
	}
	if err := s.store.UpdateDeployment(idv, func(d *model.Deployment) {
		d.Status = in.Status
		if in.ActualState != "" {
			d.ActualState = in.ActualState
		}
		d.UpdatedAt = time.Now().UTC()
	}); err != nil {
		errorJSON(w, 404, "deployment not found")
		return
	}
	if in.Status == "failed" {
		_ = s.store.AddAlert(model.Alert{ID: id("alert"), SiteID: in.SiteID, Severity: "high", Type: "deployment_failed", Message: in.Message, CreatedAt: time.Now().UTC()})
	}
	writeJSON(w, 200, map[string]bool{"updated": true})
}

func (s *Server) deadletters(w http.ResponseWriter, r *http.Request) {
	items, _ := s.dlq.List()
	writeJSON(w, 200, asJSONList(items))
}
func (s *Server) deadletterReplay(w http.ResponseWriter, r *http.Request) {
	idv := r.PathValue("id")
	dl, ok := s.dlq.Get(idv)
	if !ok {
		errorJSON(w, 404, "dead letter not found")
		return
	}
	d := dl.Delivery
	d.Attempts = 0
	d.LastError = ""
	d.NextAttempt = time.Now().UTC()
	if err := s.deliveries.Put(d.ID, d); err != nil {
		errorJSON(w, 503, err.Error())
		return
	}
	if err := s.dlq.Delete(idv); err != nil {
		errorJSON(w, 500, err.Error())
		return
	}
	writeJSON(w, 202, map[string]any{"replayed": true, "delivery_id": d.ID})
}
func (s *Server) deadletterDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.dlq.Delete(r.PathValue("id")); err != nil {
		errorJSON(w, 500, err.Error())
		return
	}
	w.WriteHeader(204)
}

func (s *Server) worker(ctx context.Context) {
	defer s.wg.Done()
	ticker := time.NewTicker(s.cfg.WorkerInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.processDeliveries(ctx)
		}
	}
}
func (s *Server) processDeliveries(ctx context.Context) {
	items, err := s.deliveries.List()
	if err != nil {
		return
	}
	now := time.Now()
	sem := make(chan struct{}, s.cfg.WorkerConcurrency)
	var wg sync.WaitGroup
	for _, d := range items {
		if d.NextAttempt.After(now) {
			continue
		}
		if _, loaded := s.inflight.LoadOrStore(d.ID, struct{}{}); loaded {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(d model.Delivery) {
			defer wg.Done()
			defer func() { <-sem; s.inflight.Delete(d.ID) }()
			s.processOne(ctx, d)
		}(d)
	}
	wg.Wait()
}
func (s *Server) processOne(ctx context.Context, d model.Delivery) {
	err := s.deliver(ctx, d)
	if err == nil {
		if er := s.deliveries.Delete(d.ID); er != nil {
			slog.Error("delete completed delivery", "error", er)
		}
		s.metrics.Deliveries.Add(1)
		return
	}
	s.metrics.DeliveryFailures.Add(1)
	d.Attempts++
	d.LastError = err.Error()
	if d.Attempts >= d.MaxAttempts {
		dl := model.DeadLetter{Delivery: d, FailedAt: time.Now().UTC(), Reason: err.Error()}
		if er := s.dlq.Put(d.ID, dl); er != nil {
			slog.Error("persist dead letter", "error", er)
			return
		}
		if er := s.deliveries.Delete(d.ID); er != nil {
			slog.Error("delete failed delivery", "error", er)
			return
		}
		_ = s.store.AddAlert(model.Alert{ID: id("alert"), SiteID: d.SiteID, Severity: "high", Type: "delivery_failed", Message: fmt.Sprintf("route %s moved to dead letter queue: %s", d.RouteID, err), CreatedAt: time.Now().UTC()})
		s.note("error", "control-plane", "", d.SiteID, "", "delivery.dlq", "delivery exhausted retries → dead letter", map[string]any{
			"route_id": d.RouteID, "event_id": d.EventID, "topic": d.Topic, "reason": err.Error(), "attempts": d.Attempts,
		})
		return
	}
	delay := time.Duration(1<<min(d.Attempts, 6)) * time.Second
	d.NextAttempt = time.Now().Add(delay)
	if er := s.deliveries.Put(d.ID, d); er != nil {
		slog.Error("reschedule delivery", "error", er)
	}
}
func (s *Server) deliver(ctx context.Context, d model.Delivery) error {
	body, _ := json.Marshal(map[string]any{"event_id": d.EventID, "site_id": d.SiteID, "topic": d.Topic, "payload": json.RawMessage(d.Payload), "headers": d.Headers, "delivery_id": d.ID, "event_time": d.EventTime})
	timeout := time.Duration(d.TimeoutSecs) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	dctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(dctx, d.Method, d.TargetURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Nodra/"+version.Version)
	for k, v := range d.Headers {
		if strings.EqualFold(k, "Host") || strings.EqualFold(k, "Content-Length") {
			continue
		}
		req.Header.Set(k, v)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("target returned %s", resp.Status)
	}
	return nil
}

func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	b, err := webassets.Assets.ReadFile("index.html")
	if err != nil {
		http.Error(w, "dashboard unavailable", 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(b)
}
func (s *Server) asset(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if strings.Contains(name, "/") || strings.Contains(name, "..") {
		http.NotFound(w, r)
		return
	}
	b, err := webassets.Assets.ReadFile(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	switch filepath.Ext(name) {
	case ".css":
		w.Header().Set("Content-Type", "text/css")
	case ".js":
		w.Header().Set("Content-Type", "application/javascript")
	case ".svg":
		w.Header().Set("Content-Type", "image/svg+xml")
	}
	w.Header().Set("Cache-Control", "public, max-age=60")
	_, _ = w.Write(b)
}

func asJSONList[T any](v []T) []T {
	if v == nil {
		return []T{}
	}
	return v
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func errorJSON(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
func (s *Server) ProcessOnce(ctx context.Context) { s.processDeliveries(ctx) }
