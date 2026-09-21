// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

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
	"math/big"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zyvorai/nodra/internal/audit"
	"github.com/zyvorai/nodra/internal/auth"
	"github.com/zyvorai/nodra/internal/leader"
	"github.com/zyvorai/nodra/internal/model"
	"github.com/zyvorai/nodra/internal/oidc"
	"github.com/zyvorai/nodra/internal/pki"
	"github.com/zyvorai/nodra/internal/policy"
	"github.com/zyvorai/nodra/internal/queue"
	"github.com/zyvorai/nodra/internal/router"
	"github.com/zyvorai/nodra/internal/store"
	"github.com/zyvorai/nodra/internal/telemetry"
	"github.com/zyvorai/nodra/internal/version"
	webassets "github.com/zyvorai/nodra/web"
)

// deliveryWorkerLockKey is a fixed pg_try_advisory_lock key retained for
// leader.NewPostgresLock's Close()/lifecycle plumbing. It no longer gates
// delivery processing itself: since queue.PostgresQueue[T] implements
// queue.Claimer, processDeliveries claims deliveries per-item instead of
// requiring a single elected leader, so every Postgres-mode replica
// processes concurrently (true multi-writer HA, not single-active-writer
// failover). Arbitrary but stable — kept in case a future singleton task
// needs a leader lock again.
const deliveryWorkerLockKey int64 = 0x6e6f647261645f31 // "nodrad_1"

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
	// DeliveryClaimLease only applies when StoreDriver=="postgres": how stale
	// another replica's claim on a delivery must be before this replica will
	// steal it. Must exceed the slowest possible processOne (bounded by the
	// largest per-delivery TimeoutSecs, 60s) with headroom, or a live worker's
	// in-progress claim could be stolen out from under it.
	DeliveryClaimLease time.Duration
	PKIEnabled         bool
	PKIDir             string
	TLSCertFile        string
	TLSKeyFile         string
	ClientCAFile       string
	StoreDriver        string // file|postgres
	DatabaseURL        string
	AuditRetentionDays int
	// PublicBaseURL, when set, is embedded as a CRLDistributionPoint in
	// issued site certificates (e.g. "https://cp.example.com"). Optional —
	// revocation is still enforced app-side via agentSite regardless.
	PublicBaseURL string
	// CertExpiryWarnBefore controls how far ahead of a site certificate's
	// expiry a certificate_expiring alert is raised. Default 30 days.
	CertExpiryWarnBefore time.Duration
	// OIDC console login: a third way to obtain the existing admin/viewer
	// bearer tokens via a configured IdP, not a user directory and not
	// multi-tenant orgs. Enabled when OIDCIssuerURL and OIDCClientID are
	// both set.
	OIDCEnabled      bool
	OIDCIssuerURL    string
	OIDCClientID     string
	OIDCClientSecret string
	OIDCRedirectURL  string
	OIDCGroupsClaim  string
	OIDCAdminGroup   string
	OIDCViewerGroup  string
}

type Server struct {
	cfg Config
	// instanceID identifies this process as a delivery claim owner when
	// deliveries is a queue.Claimer (Postgres multi-writer mode) — see
	// processDeliveries. Unused in file-WAL/single-process mode.
	instanceID    string
	store         store.Backend
	deliveries    queue.QueueLike[model.Delivery]
	dlq           queue.QueueLike[model.DeadLetter]
	leader        leader.Elector
	isLeader      atomic.Bool
	metrics       *telemetry.Metrics
	activity      *activityLog
	audit         audit.Store
	client        *http.Client
	http          *http.Server
	wg            sync.WaitGroup
	cancel        context.CancelFunc
	inflight      sync.Map
	ca            *pki.CA
	crlMu         sync.RWMutex
	crlPEM        []byte
	crlNextUpdate time.Time
	oidc          oidc.Config
	oidcDiscMu    sync.Mutex
	oidcDisc      *oidc.Discovery
	oidcJWKS      *oidc.JWKSet
	oidcStatesMu  sync.Mutex
	oidcStates    map[string]oidcState
}

// oidcState is one in-flight OIDC login attempt's server-side state,
// single-use (deleted on lookup) and short-lived.
type oidcState struct {
	Nonce     string
	ExpiresAt time.Time
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
	if cfg.AuditRetentionDays <= 0 {
		cfg.AuditRetentionDays = 90
	}
	if cfg.CertExpiryWarnBefore <= 0 {
		cfg.CertExpiryWarnBefore = 30 * 24 * time.Hour
	}
	if cfg.OIDCIssuerURL != "" && cfg.OIDCClientID != "" {
		cfg.OIDCEnabled = true
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
	if cfg.DeliveryClaimLease <= 0 {
		cfg.DeliveryClaimLease = 90 * time.Second
	}
	if err := os.MkdirAll(cfg.DataDir, 0o750); err != nil {
		return nil, err
	}
	st, err := store.OpenBackend(cfg.StoreDriver, filepath.Join(cfg.DataDir, "state.json"), cfg.DatabaseURL)
	if err != nil {
		return nil, err
	}
	var q queue.QueueLike[model.Delivery]
	var dlq queue.QueueLike[model.DeadLetter]
	var lead leader.Elector
	deliveryOpts := queue.Options{MaxItems: cfg.DeliveryMaxItems, MaxBytes: cfg.DeliveryMaxBytes, Policy: "reject"}
	if cfg.StoreDriver == "postgres" && cfg.DatabaseURL != "" {
		// Postgres-backed delivery/DLQ: readable from every replica, and
		// every replica actively claims and processes items concurrently
		// (queue.Claimer, see processDeliveries) — true multi-writer HA, not
		// single-active-writer failover. See docs/ARCHITECTURE.md.
		q, err = queue.OpenPostgres[model.Delivery](cfg.DatabaseURL, "nodra_deliveries", deliveryOpts)
		if err != nil {
			return nil, err
		}
		dlq, err = queue.OpenPostgres[model.DeadLetter](cfg.DatabaseURL, "nodra_deadletters", deliveryOpts)
		if err != nil {
			return nil, err
		}
		lead, err = leader.NewPostgresLock(cfg.DatabaseURL, deliveryWorkerLockKey)
		if err != nil {
			return nil, err
		}
	} else {
		q, err = queue.OpenWithOptions[model.Delivery](filepath.Join(cfg.DataDir, "deliveries"), deliveryOpts)
		if err != nil {
			return nil, err
		}
		dlq, err = queue.OpenWithOptions[model.DeadLetter](filepath.Join(cfg.DataDir, "deadletters"), deliveryOpts)
		if err != nil {
			return nil, err
		}
		lead = leader.AlwaysLeader{}
	}
	au, err := audit.Open(cfg.StoreDriver, cfg.DataDir, cfg.DatabaseURL, audit.Options{RetentionDays: cfg.AuditRetentionDays})
	if err != nil {
		return nil, err
	}
	instanceID, err := auth.NewToken(8)
	if err != nil {
		return nil, err
	}
	s := &Server{cfg: cfg, instanceID: instanceID, store: st, deliveries: q, dlq: dlq, leader: lead, metrics: &telemetry.Metrics{}, activity: &activityLog{}, audit: au, client: &http.Client{Transport: &http.Transport{MaxIdleConns: 128, MaxIdleConnsPerHost: 16, IdleConnTimeout: 90 * time.Second}}, oidcStates: map[string]oidcState{}}
	s.oidc = oidc.Config{
		IssuerURL: cfg.OIDCIssuerURL, ClientID: cfg.OIDCClientID, ClientSecret: cfg.OIDCClientSecret,
		RedirectURL: cfg.OIDCRedirectURL, GroupsClaim: cfg.OIDCGroupsClaim,
		AdminGroup: cfg.OIDCAdminGroup, ViewerGroup: cfg.OIDCViewerGroup,
	}
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
	s.wg.Add(3)
	go s.worker(ctx)
	go s.auditRetentionLoop(ctx)
	go s.certLifecycleLoop(ctx)
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
	_ = s.audit.Close()
	_ = s.leader.Close()
	return err
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })
	mux.HandleFunc("GET /readyz", s.readyz)
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		ds := s.deliveries.Stats()
		qs := s.dlq.Stats()
		// nodra_delivery_leader: 1 iff this replica is actively processing
		// deliveries. In file-WAL mode that means "the elected leader"; in
		// Postgres multi-writer mode every replica processes concurrently,
		// so this is always 1 there once the worker loop has ticked once.
		leaderGauge := 0
		if s.isLeader.Load() {
			leaderGauge = 1
		}
		_, _ = io.WriteString(w, s.metrics.Prometheus()+fmt.Sprintf("# TYPE nodra_pending_deliveries gauge\nnodra_pending_deliveries %d\n# TYPE nodra_delivery_queue_bytes gauge\nnodra_delivery_queue_bytes %d\n# TYPE nodra_dead_letters gauge\nnodra_dead_letters %d\n# TYPE nodra_delivery_leader gauge\nnodra_delivery_leader %d\n", ds.Items, ds.Bytes, qs.Items, leaderGauge))
	})
	mux.HandleFunc("GET /api/v1/version", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]string{"name": "nodra", "version": version.Version, "commit": version.Commit, "build_date": version.BuildDate})
	})
	mux.HandleFunc("POST /api/v1/auth/login", s.login)
	mux.HandleFunc("GET /api/v1/auth/me", s.authMe)
	mux.HandleFunc("GET /api/v1/auth/oidc/login", s.oidcLogin)
	mux.HandleFunc("GET /api/v1/auth/oidc/callback", s.oidcCallback)
	mux.HandleFunc("POST /api/v1/enroll", s.enroll)
	mux.HandleFunc("POST /api/v1/heartbeat", s.heartbeat)
	mux.HandleFunc("POST /api/v1/events", s.events)
	mux.HandleFunc("POST /api/v1/devices/register", s.registerDevice)
	mux.HandleFunc("GET /api/v1/agent/deployments", s.agentDeployments)
	mux.HandleFunc("POST /api/v1/agent/deployments/{id}/status", s.agentDeploymentStatus)
	mux.HandleFunc("POST /api/v1/agent/deployments/{id}/rollback", s.agentDeploymentRollback)
	mux.HandleFunc("GET /api/v1/agent/twins", s.agentTwins)
	mux.HandleFunc("POST /api/v1/agent/twins/{id}/reported", s.agentTwinReported)
	mux.HandleFunc("POST /api/v1/agent/devices/{id}/ota/status", s.agentOTAStatus)
	mux.Handle("GET /api/v1/overview", s.admin(http.HandlerFunc(s.overview)))
	mux.Handle("GET /api/v1/sites", s.admin(http.HandlerFunc(s.sites)))
	mux.Handle("POST /api/v1/sites/{id}/revoke", s.admin(http.HandlerFunc(s.siteRevoke)))
	mux.HandleFunc("POST /api/v1/sites/{id}/rotate", s.siteRotate)
	mux.HandleFunc("GET /api/v1/ca/crl", s.caCRL)
	mux.Handle("GET /api/v1/routes", s.admin(http.HandlerFunc(s.routesList)))
	mux.Handle("POST /api/v1/routes", s.admin(http.HandlerFunc(s.routeCreate)))
	mux.Handle("DELETE /api/v1/routes/{id}", s.admin(http.HandlerFunc(s.routeDelete)))
	mux.Handle("GET /api/v1/devices", s.admin(http.HandlerFunc(s.devices)))
	mux.Handle("GET /api/v1/twins", s.admin(http.HandlerFunc(s.twins)))
	mux.Handle("PUT /api/v1/twins/{id}/desired", s.admin(http.HandlerFunc(s.twinDesired)))
	mux.Handle("POST /api/v1/devices/{id}/ota", s.admin(http.HandlerFunc(s.otaDeviceRequest)))
	mux.Handle("GET /api/v1/devices/{id}/ota", s.admin(http.HandlerFunc(s.otaDeviceGet)))
	mux.Handle("GET /api/v1/ota/campaigns", s.admin(http.HandlerFunc(s.otaCampaigns)))
	mux.Handle("POST /api/v1/ota/campaigns", s.admin(http.HandlerFunc(s.otaCampaignCreate)))
	mux.Handle("GET /api/v1/ota/campaigns/{id}", s.admin(http.HandlerFunc(s.otaCampaignGet)))
	mux.Handle("POST /api/v1/ota/campaigns/{id}/start", s.admin(http.HandlerFunc(s.otaCampaignStart)))
	mux.Handle("POST /api/v1/ota/campaigns/{id}/promote", s.admin(http.HandlerFunc(s.otaCampaignPromote)))
	mux.Handle("POST /api/v1/ota/campaigns/{id}/abort", s.admin(http.HandlerFunc(s.otaCampaignAbort)))
	mux.Handle("GET /api/v1/deployments", s.admin(http.HandlerFunc(s.deployments)))
	mux.Handle("POST /api/v1/deployments", s.admin(http.HandlerFunc(s.deploymentCreate)))
	mux.Handle("PATCH /api/v1/deployments/{id}", s.admin(http.HandlerFunc(s.deploymentPatch)))
	mux.Handle("DELETE /api/v1/deployments/{id}", s.admin(http.HandlerFunc(s.deploymentDelete)))
	mux.Handle("GET /api/v1/orgs", s.admin(http.HandlerFunc(s.orgs)))
	mux.Handle("POST /api/v1/orgs", s.admin(http.HandlerFunc(s.orgCreate)))
	mux.Handle("GET /api/v1/orgs/{id}", s.admin(http.HandlerFunc(s.orgGet)))
	mux.Handle("DELETE /api/v1/orgs/{id}", s.admin(http.HandlerFunc(s.orgDelete)))
	mux.Handle("GET /api/v1/policy-packs", s.admin(http.HandlerFunc(s.policyPacks)))
	mux.Handle("POST /api/v1/policy-packs", s.admin(http.HandlerFunc(s.policyPackCreate)))
	mux.Handle("PATCH /api/v1/policy-packs/{id}", s.admin(http.HandlerFunc(s.policyPackPatch)))
	mux.Handle("DELETE /api/v1/policy-packs/{id}", s.admin(http.HandlerFunc(s.policyPackDelete)))
	mux.Handle("GET /api/v1/alerts", s.admin(http.HandlerFunc(s.alerts)))
	mux.Handle("POST /api/v1/alerts/{id}/resolve", s.admin(http.HandlerFunc(s.alertResolve)))
	mux.Handle("GET /api/v1/events", s.admin(http.HandlerFunc(s.eventsList)))
	mux.Handle("GET /api/v1/deadletters", s.admin(http.HandlerFunc(s.deadletters)))
	mux.Handle("POST /api/v1/deadletters/{id}/replay", s.admin(http.HandlerFunc(s.deadletterReplay)))
	mux.Handle("DELETE /api/v1/deadletters/{id}", s.admin(http.HandlerFunc(s.deadletterDelete)))
	mux.Handle("GET /api/v1/activity", s.admin(http.HandlerFunc(s.activityList)))
	mux.Handle("POST /api/v1/activity", s.admin(http.HandlerFunc(s.activityPost)))
	mux.Handle("GET /api/v1/audit", s.admin(http.HandlerFunc(s.auditList)))
	mux.Handle("GET /api/v1/audit/export", s.admin(http.HandlerFunc(s.auditExport)))
	mux.HandleFunc("GET /assets/{name}", s.asset)
	mux.HandleFunc("GET /", s.index)
	return s.securityHeaders(s.requestLog(mux))
}

// readyz fails closed when the fleet store cannot be pinged or delivery queues are unavailable.
func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if s.store == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "not_ready", "reason": "store_nil"})
		return
	}
	if err := s.store.Ping(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "not_ready", "reason": "store", "error": err.Error()})
		return
	}
	if s.deliveries == nil || s.dlq == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "not_ready", "reason": "queues"})
		return
	}
	_ = s.deliveries.Stats()
	_ = s.dlq.Stats()
	driver := s.cfg.StoreDriver
	if driver == "" {
		driver = "file"
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready", "store": driver})
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
	role, _ := s.orgRoleForBearer(tok)
	return role
}

// orgRoleForBearer checks tok against every org's admin/viewer token hash,
// returning the role and that org's ID. An org-scoped token grants exactly
// the same "admin"/"viewer" capability as the matching global token on
// every endpoint except the few explicitly org-filtered ones (sites list,
// single-site revoke — see orgForBearer and docs/ARCHITECTURE.md's
// "Multi-tenant orgs" section); it is not a general per-org permission
// system in v1.
func (s *Server) orgRoleForBearer(tok string) (role, orgID string) {
	if tok == "" {
		return "", ""
	}
	for _, org := range s.store.Orgs() {
		if org.AdminTokenHash != "" && auth.EqualHash(org.AdminTokenHash, tok) {
			return "admin", org.ID
		}
		if org.ViewerTokenHash != "" && auth.EqualHash(org.ViewerTokenHash, tok) {
			return "viewer", org.ID
		}
	}
	return "", ""
}

// orgForBearer reports which org (if any) tok is scoped to. An empty orgID
// with scoped=false means tok is a global admin/viewer token (or doesn't
// resolve to any role at all) and sees the whole fleet, unfiltered.
func (s *Server) orgForBearer(tok string) (orgID string, scoped bool) {
	if tok == "" {
		return "", false
	}
	if s.cfg.AdminToken != "" && auth.EqualToken(tok, s.cfg.AdminToken) {
		return "", false
	}
	if s.cfg.ViewerToken != "" && auth.EqualToken(tok, s.cfg.ViewerToken) {
		return "", false
	}
	_, org := s.orgRoleForBearer(tok)
	if org == "" {
		return "", false
	}
	return org, true
}

// siteOrgID resolves siteID's org, "" if siteID is empty (a fleet-wide
// entity) or the site doesn't exist.
func (s *Server) siteOrgID(siteID string) string {
	if siteID == "" {
		return ""
	}
	site, ok := s.store.Site(siteID)
	if !ok {
		return ""
	}
	return site.OrgID
}

// callerCanSeeSite reports whether a request's bearer token may VIEW data
// scoped to siteID: always true for a global (unscoped) caller, and for a
// fleet-wide entity (siteID==""), since fleet-wide config (a global route
// or policy pack) still applies to every org's sites and hiding it would
// mislead rather than protect. An org-scoped caller otherwise sees siteID
// only when it belongs to their own org. Used by every org-filtered list
// handler in this file — see docs/ARCHITECTURE.md's "Multi-tenant orgs".
func (s *Server) callerCanSeeSite(r *http.Request, siteID string) bool {
	orgID, scoped := s.orgForBearer(bearer(r))
	if !scoped || siteID == "" {
		return true
	}
	return s.siteOrgID(siteID) == orgID
}

// callerCanMutateSite reports whether a request's bearer token may CREATE
// or CHANGE a resource scoped to siteID: always true for a global caller.
// An org-scoped caller may mutate only a resource scoped to one of its own
// sites — never a fleet-wide resource (siteID=="", unlike the read-side
// callerCanSeeSite) and never another org's site, since either would let
// one tenant's admin token affect every other tenant.
func (s *Server) callerCanMutateSite(r *http.Request, siteID string) bool {
	orgID, scoped := s.orgForBearer(bearer(r))
	if !scoped {
		return true
	}
	if siteID == "" {
		return false
	}
	return s.siteOrgID(siteID) == orgID
}

// requireGlobalAdmin gates org-management endpoints to the platform
// operator's own global admin token — an org-scoped admin token (a
// tenant's own admin) must not be able to create, list, or delete orgs,
// only the fleet-wide admin configured via Config.AdminToken. Writes a 403
// and returns false when the caller doesn't qualify.
func (s *Server) requireGlobalAdmin(w http.ResponseWriter, r *http.Request) bool {
	tok := bearer(r)
	if _, scoped := s.orgForBearer(tok); scoped || s.roleForBearer(tok) != "admin" {
		errorJSON(w, 403, "global admin role required")
		return false
	}
	return true
}

// actorForBearer resolves the audit-log actor (the console username) for a
// request's bearer token, empty if it doesn't match a configured role.
func (s *Server) actorForBearer(tok string) string {
	if role, org := s.orgRoleForBearer(tok); role != "" {
		return org + ":" + role
	}
	switch s.roleForBearer(tok) {
	case "admin":
		return s.cfg.AdminUser
	case "viewer":
		return s.cfg.ViewerUser
	default:
		return ""
	}
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
		s.note("ok", "control-plane", s.cfg.AdminUser, "", "", "", "login", "console login", map[string]any{"role": "admin"})
		writeJSON(w, 200, map[string]any{
			"token": s.cfg.AdminToken,
			"user":  map[string]string{"username": s.cfg.AdminUser, "role": "admin"},
		})
		return
	}
	if s.cfg.ViewerToken != "" && s.cfg.ViewerPassword != "" &&
		auth.EqualToken(user, s.cfg.ViewerUser) && auth.EqualToken(in.Password, s.cfg.ViewerPassword) {
		s.note("ok", "control-plane", s.cfg.ViewerUser, "", "", "", "login", "console login", map[string]any{"role": "viewer"})
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
	if user != "" {
		s.note("warn", "control-plane", user, "", "", "", "login", "invalid username or password", nil)
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

// oidcDiscover lazily resolves and caches the provider's discovery
// document and JWKS on first use.
func (s *Server) oidcDiscover(ctx context.Context) (*oidc.Discovery, *oidc.JWKSet, error) {
	s.oidcDiscMu.Lock()
	defer s.oidcDiscMu.Unlock()
	if s.oidcDisc != nil && s.oidcJWKS != nil {
		return s.oidcDisc, s.oidcJWKS, nil
	}
	disc, err := oidc.Discover(ctx, s.client, s.oidc.IssuerURL)
	if err != nil {
		return nil, nil, err
	}
	jwks, err := oidc.FetchJWKS(ctx, s.client, disc.JWKSURI)
	if err != nil {
		return nil, nil, err
	}
	s.oidcDisc, s.oidcJWKS = disc, jwks
	return disc, jwks, nil
}

// oidcLogin redirects the browser to the configured IdP's authorization
// endpoint, starting an OIDC login for the console's existing admin/viewer
// roles (not a user directory, not multi-tenant orgs).
func (s *Server) oidcLogin(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.OIDCEnabled {
		errorJSON(w, 503, "oidc login is not configured")
		return
	}
	disc, _, err := s.oidcDiscover(r.Context())
	if err != nil {
		errorJSON(w, 502, "oidc discovery failed: "+err.Error())
		return
	}
	state, err := auth.NewToken(16)
	if err != nil {
		errorJSON(w, 500, "state generation failed")
		return
	}
	nonce, err := auth.NewToken(16)
	if err != nil {
		errorJSON(w, 500, "nonce generation failed")
		return
	}
	s.oidcStatesMu.Lock()
	s.oidcStates[state] = oidcState{Nonce: nonce, ExpiresAt: time.Now().Add(10 * time.Minute)}
	s.oidcStatesMu.Unlock()
	http.Redirect(w, r, oidc.BuildAuthURL(disc, s.oidc, state, nonce), http.StatusFound)
}

// oidcCallback completes the login: exchanges the code, verifies the ID
// token, resolves the caller's role via the configured groups claim, and
// mints the SAME static bearer token login() already issues for that role
// — OIDC is a third way to obtain the existing two roles, not a new
// identity/session mechanism. The token travels back to the SPA via the
// URL fragment (never a logged query string), matching this codebase's
// lack of any cookie/session middleware.
func (s *Server) oidcCallback(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.OIDCEnabled {
		errorJSON(w, 503, "oidc login is not configured")
		return
	}
	q := r.URL.Query()
	code, stateParam := q.Get("code"), q.Get("state")
	if code == "" || stateParam == "" {
		errorJSON(w, 400, "missing code or state")
		return
	}
	s.oidcStatesMu.Lock()
	st, ok := s.oidcStates[stateParam]
	delete(s.oidcStates, stateParam)
	s.oidcStatesMu.Unlock()
	if !ok || time.Now().After(st.ExpiresAt) {
		errorJSON(w, 400, "invalid or expired oidc state")
		return
	}
	disc, jwks, err := s.oidcDiscover(r.Context())
	if err != nil {
		errorJSON(w, 502, "oidc discovery failed: "+err.Error())
		return
	}
	idToken, err := oidc.ExchangeCode(r.Context(), s.client, disc, s.oidc, code)
	if err != nil {
		errorJSON(w, 502, "oidc code exchange failed: "+err.Error())
		return
	}
	claims, err := oidc.VerifyIDToken(idToken, jwks, disc.Issuer, s.oidc.ClientID, st.Nonce, time.Now())
	if err != nil {
		errorJSON(w, 401, "oidc token verification failed: "+err.Error())
		return
	}
	role := oidc.RoleForClaims(claims, s.oidc)
	if role == "" {
		s.note("warn", "control-plane", "oidc:"+claims.String("sub"), "", "", "", "oidc.login", "oidc login denied: no matching admin/viewer group", nil)
		errorJSON(w, 403, "oidc login denied: no matching admin or viewer group")
		return
	}
	tok := s.cfg.ViewerToken
	if role == "admin" {
		tok = s.cfg.AdminToken
	}
	s.note("ok", "control-plane", "oidc:"+claims.String("sub"), "", "", "", "oidc.login", "console login via OIDC", map[string]any{"role": role, "email": claims.String("email")})
	dest := "/#oidc_token=" + url.QueryEscape(tok) + "&role=" + url.QueryEscape(role)
	http.Redirect(w, r, dest, http.StatusFound)
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
	orgID := ""
	switch {
	case s.cfg.EnrollmentToken != "" && auth.EqualToken(in.EnrollmentToken, s.cfg.EnrollmentToken):
		// global fleet enrollment, orgID stays ""
	default:
		matched := false
		for _, org := range s.store.Orgs() {
			if org.EnrollmentTokenHash != "" && auth.EqualHash(org.EnrollmentTokenHash, in.EnrollmentToken) {
				orgID, matched = org.ID, true
				break
			}
		}
		if !matched {
			errorJSON(w, 401, "invalid enrollment token")
			return
		}
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
	site := model.Site{ID: id("site"), Name: in.Name, TokenHash: auth.Hash(tok), Status: "online", CreatedAt: now, LastSeen: now, Metadata: in.Metadata, OrgID: orgID}
	out := map[string]any{"agent_token": tok}
	if s.ca != nil && in.CSRPem != "" {
		signed, er := pki.SignCSR(s.ca, []byte(in.CSRPem), site.ID, 90*24*time.Hour, s.crlDistributionPoints()...)
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
	s.note("ok", "control-plane", site.ID, "", site.ID, site.Name, "enroll", "site enrolled and agent token issued", map[string]any{"metadata": in.Metadata})
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
		if site, exists := s.store.Site(in.SiteID); exists && site.Revoked {
			writeJSON(w, 403, map[string]string{"error": "site_revoked"})
			return
		}
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
		s.metrics.Duplicates.Add(1)
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
	// Not durably audited: too high-frequency for a compliance trail.
	if matched > 0 {
		s.note("info", "agent", "", "", in.SiteID, "", "event", "telemetry accepted on "+in.Topic, map[string]any{
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
	s.note("ok", "agent", in.SiteID, "", in.SiteID, "", "device.register", "device registered: "+in.Name, map[string]any{"device_id": in.ID, "protocol": in.Protocol})
	writeJSON(w, 201, in)
}
func (s *Server) overview(w http.ResponseWriter, r *http.Request) {
	st := s.store.Snapshot()
	orgID, scoped := s.orgForBearer(bearer(r))
	sites, online := 0, 0
	cut := time.Now().Add(-90 * time.Second)
	for _, v := range st.Sites {
		if scoped && v.OrgID != orgID {
			continue
		}
		sites++
		if !v.Revoked && v.LastSeen.After(cut) {
			online++
		}
	}
	devices, twins, routes, deployments, openAlerts, events := 0, 0, 0, 0, 0, 0
	for _, v := range st.Devices {
		if s.callerCanSeeSite(r, v.SiteID) {
			devices++
		}
	}
	for _, v := range st.Twins {
		if s.callerCanSeeSite(r, v.SiteID) {
			twins++
		}
	}
	for _, v := range st.Routes {
		if s.callerCanSeeSite(r, v.SiteID) {
			routes++
		}
	}
	for _, v := range st.Deployments {
		if s.callerCanSeeSite(r, v.SiteID) {
			deployments++
		}
	}
	for _, v := range st.Alerts {
		if !v.Resolved && s.callerCanSeeSite(r, v.SiteID) {
			openAlerts++
		}
	}
	for _, v := range st.Events {
		if s.callerCanSeeSite(r, v.SiteID) {
			events++
		}
	}
	// pending_deliveries/delivery_queue_bytes/dead_letters come from
	// queue.Stats(), which has no per-site breakdown — these three remain
	// fleet-wide totals even for an org-scoped caller. See
	// docs/ARCHITECTURE.md's "Multi-tenant orgs" section.
	ds := s.deliveries.Stats()
	qs := s.dlq.Stats()
	writeJSON(w, 200, map[string]any{"sites": sites, "online_sites": online, "devices": devices, "twins": twins, "routes": routes, "deployments": deployments, "open_alerts": openAlerts, "pending_deliveries": ds.Items, "delivery_queue_bytes": ds.Bytes, "dead_letters": qs.Items, "events": events})
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
	// An org-scoped token only ever sees its own org's sites — a site
	// enrolled outside any org (OrgID=="") is invisible to it too, same as
	// a different org's sites. A global admin/viewer token is unfiltered,
	// unchanged from before orgs existed.
	if orgID, scoped := s.orgForBearer(bearer(r)); scoped {
		filtered := v[:0]
		for _, site := range v {
			if site.OrgID == orgID {
				filtered = append(filtered, site)
			}
		}
		v = filtered
	}
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
	if orgID, scoped := s.orgForBearer(bearer(r)); scoped {
		site, ok := s.store.Site(idv)
		if !ok || site.OrgID != orgID {
			errorJSON(w, 404, "site not found")
			return
		}
	}
	now := time.Now().UTC()
	if err := s.store.UpdateSite(idv, func(v *model.Site) { v.Revoked = true; v.Status = "revoked"; v.RevokedAt = now }); err != nil {
		errorJSON(w, 404, "site not found")
		return
	}
	s.refreshCRL()
	s.note("ok", "control-plane", s.actorForBearer(bearer(r)), "", idv, idv, "site.revoke", "site revoked", nil)
	writeJSON(w, 200, map[string]bool{"revoked": true})
}

// siteRotate re-signs a fresh CSR for an already-enrolled, non-revoked site
// — the edge generates and keeps a new keypair; the server never sees the
// private key, mirroring enroll's trust model.
func (s *Server) siteRotate(w http.ResponseWriter, r *http.Request) {
	idv := r.PathValue("id")
	if s.ca == nil {
		errorJSON(w, 503, "PKI is not enabled on this control plane")
		return
	}
	site, ok := s.agentSite(r, idv)
	if !ok {
		if revokedSite, exists := s.store.Site(idv); exists && revokedSite.Revoked {
			writeJSON(w, 403, map[string]string{"error": "site_revoked"})
			return
		}
		errorJSON(w, 401, "unauthorized agent")
		return
	}
	var in struct {
		CSRPem string `json:"csr_pem"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	if strings.TrimSpace(in.CSRPem) == "" {
		errorJSON(w, 400, "csr_pem is required")
		return
	}
	signed, err := pki.SignCSR(s.ca, []byte(in.CSRPem), site.ID, 90*24*time.Hour, s.crlDistributionPoints()...)
	if err != nil {
		errorJSON(w, 400, "invalid certificate request: "+err.Error())
		return
	}
	oldSerial := site.CertificateSerial
	if err := s.store.UpdateSite(idv, func(v *model.Site) {
		v.CertificateSerial = signed.Serial
		v.CertificateExpiresAt = signed.ExpiresAt
	}); err != nil {
		errorJSON(w, 507, "site persistence failed: "+err.Error())
		return
	}
	// Resolve any open certificate_expiring alert now that the cert has a
	// fresh expiry — otherwise it lingers until an operator clears it.
	for _, al := range s.store.Alerts() {
		if al.SiteID == idv && al.Type == "certificate_expiring" && !al.Resolved {
			_ = s.store.ResolveAlert(al.ID)
		}
	}
	s.note("ok", "control-plane", idv, "", idv, site.Name, "site.rotate", "certificate rotated", map[string]any{
		"old_serial": oldSerial, "new_serial": signed.Serial, "expires_at": signed.ExpiresAt,
	})
	writeJSON(w, 200, map[string]any{"client_certificate": string(signed.CertPEM), "ca_certificate": string(s.ca.CertPEM), "certificate_serial": signed.Serial, "certificate_expires_at": signed.ExpiresAt})
}

// caCRL serves the cached CRL. Unauthenticated — a CRL carries only serials
// and timestamps, the same public trust tier as the CA certificate already
// returned unauthenticated from /api/v1/enroll.
func (s *Server) caCRL(w http.ResponseWriter, r *http.Request) {
	if s.ca == nil {
		errorJSON(w, 503, "PKI is not enabled on this control plane")
		return
	}
	s.crlMu.RLock()
	pemBytes := s.crlPEM
	s.crlMu.RUnlock()
	if pemBytes == nil {
		s.refreshCRL()
		s.crlMu.RLock()
		pemBytes = s.crlPEM
		s.crlMu.RUnlock()
	}
	w.Header().Set("Content-Type", "application/pkix-crl")
	_, _ = w.Write(pemBytes)
}

// crlDistributionPoints returns the CRLDistributionPoints to embed in newly
// signed certificates, or nil when PublicBaseURL isn't configured.
func (s *Server) crlDistributionPoints() []string {
	if s.cfg.PublicBaseURL == "" {
		return nil
	}
	return []string{strings.TrimRight(s.cfg.PublicBaseURL, "/") + "/api/v1/ca/crl"}
}

// refreshCRL regenerates the cached CRL from every currently-revoked site
// with an issued certificate. Called synchronously right after a revoke and
// periodically from certLifecycleLoop so nextUpdate stays fresh even absent
// new revocations.
func (s *Server) refreshCRL() {
	if s.ca == nil {
		return
	}
	var revoked []pki.RevokedCert
	for _, site := range s.store.Sites() {
		if !site.Revoked || site.CertificateSerial == "" {
			continue
		}
		serial, ok := new(big.Int).SetString(site.CertificateSerial, 16)
		if !ok {
			continue
		}
		revokedAt := site.RevokedAt
		if revokedAt.IsZero() {
			revokedAt = time.Now().UTC()
		}
		revoked = append(revoked, pki.RevokedCert{Serial: serial, RevokedAt: revokedAt})
	}
	now := time.Now().UTC()
	next := now.Add(15 * time.Minute)
	crlPEM, err := pki.GenerateCRL(s.ca, revoked, now, next)
	if err != nil {
		slog.Error("crl generation failed", "error", err)
		return
	}
	s.crlMu.Lock()
	s.crlPEM = crlPEM
	s.crlNextUpdate = next
	s.crlMu.Unlock()
}

// certLifecycleLoop keeps the cached CRL's nextUpdate fresh and raises
// certificate_expiring alerts ahead of a site certificate's expiry.
// Deliberately its own low-frequency goroutine, like auditRetentionLoop.
func (s *Server) certLifecycleLoop(ctx context.Context) {
	defer s.wg.Done()
	scan := func() {
		if s.ca == nil {
			return
		}
		s.refreshCRL()
		now := time.Now().UTC()
		// Dedup: don't re-raise while an unresolved certificate_expiring
		// alert for the same site is already open.
		open := map[string]bool{}
		for _, al := range s.store.Alerts() {
			if al.Type == "certificate_expiring" && !al.Resolved {
				open[al.SiteID] = true
			}
		}
		for _, site := range s.store.Sites() {
			if site.Revoked || site.CertificateExpiresAt.IsZero() || open[site.ID] {
				continue
			}
			if !pki.ShouldRotate(site.CertificateExpiresAt, now, s.cfg.CertExpiryWarnBefore) {
				continue
			}
			_ = s.store.AddAlert(model.Alert{ID: id("alert"), SiteID: site.ID, Severity: "medium", Type: "certificate_expiring", Message: fmt.Sprintf("site %s certificate expires at %s", site.Name, site.CertificateExpiresAt.Format(time.RFC3339)), CreatedAt: now})
		}
	}
	scan()
	t := time.NewTicker(15 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			scan()
		}
	}
}
func (s *Server) routesList(w http.ResponseWriter, r *http.Request) {
	v := s.store.Routes()
	out := v[:0]
	for _, rt := range v {
		if s.callerCanSeeSite(r, rt.SiteID) {
			out = append(out, rt)
		}
	}
	writeJSON(w, 200, asJSONList(out))
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
	if !s.callerCanMutateSite(r, in.SiteID) {
		errorJSON(w, 403, "an org-scoped token may only create routes scoped to its own org's sites")
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
	s.note("ok", "control-plane", s.actorForBearer(bearer(r)), "", in.SiteID, in.Name, "route.create", "route created: "+in.Name, map[string]any{"route_id": in.ID, "topic": in.Topic, "target_url": in.TargetURL})
	writeJSON(w, 201, in)
}
func (s *Server) routeDelete(w http.ResponseWriter, r *http.Request) {
	idv := r.PathValue("id")
	found := false
	for _, rt := range s.store.Routes() {
		if rt.ID == idv {
			if !s.callerCanMutateSite(r, rt.SiteID) {
				errorJSON(w, 404, "route not found")
				return
			}
			found = true
			break
		}
	}
	if !found {
		errorJSON(w, 404, "route not found")
		return
	}
	if err := s.store.DeleteRoute(idv); err != nil {
		errorJSON(w, 404, "route not found")
		return
	}
	s.note("ok", "control-plane", s.actorForBearer(bearer(r)), "", "", idv, "route.delete", "route deleted: "+idv, map[string]any{"route_id": idv})
	w.WriteHeader(204)
}
func (s *Server) devices(w http.ResponseWriter, r *http.Request) {
	v := s.store.Devices()
	out := v[:0]
	for _, d := range v {
		if s.callerCanSeeSite(r, d.SiteID) {
			out = append(out, d)
		}
	}
	writeJSON(w, 200, asJSONList(out))
}
func (s *Server) twins(w http.ResponseWriter, r *http.Request) {
	v := s.store.Snapshot().Twins
	out := v[:0]
	for _, t := range v {
		if s.callerCanSeeSite(r, t.SiteID) {
			out = append(out, t)
		}
	}
	writeJSON(w, 200, asJSONList(out))
}
func (s *Server) twinDesired(w http.ResponseWriter, r *http.Request) {
	dev, ok := s.store.Device(r.PathValue("id"))
	if !ok || !s.callerCanMutateSite(r, dev.SiteID) {
		errorJSON(w, 404, "device not found")
		return
	}
	var in struct {
		Desired map[string]any `json:"desired"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	var tw model.Twin
	if err := s.store.UpdateTwin(dev.ID, func(cur *model.Twin) {
		cur.DeviceID = dev.ID
		cur.SiteID = dev.SiteID
		cur.Desired = in.Desired
		cur.DesiredVersion++
		cur.UpdatedAt = time.Now().UTC()
		tw = *cur
	}); err != nil {
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
	var tw model.Twin
	if err := s.store.UpdateTwin(dev.ID, func(cur *model.Twin) {
		cur.DeviceID = dev.ID
		cur.SiteID = in.SiteID
		cur.Reported = in.Reported
		cur.ReportedVersion++
		cur.UpdatedAt = time.Now().UTC()
		tw = *cur
	}); err != nil {
		errorJSON(w, 507, err.Error())
		return
	}
	writeJSON(w, 200, tw)
}
func (s *Server) deployments(w http.ResponseWriter, r *http.Request) {
	v := s.store.Deployments()
	out := v[:0]
	for _, d := range v {
		if s.callerCanSeeSite(r, d.SiteID) {
			out = append(out, d)
		}
	}
	writeJSON(w, 200, asJSONList(out))
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
	if !s.callerCanMutateSite(r, in.SiteID) {
		errorJSON(w, 403, "an org-scoped token may only deploy to its own org's sites")
		return
	}
	if ok, deniedBy := policy.Allowed(s.store.PolicyPacks(), in.SiteID, in.Image); !ok {
		s.note("warn", "control-plane", s.actorForBearer(bearer(r)), "", in.SiteID, in.Name, "policy.deny", "image denied by policy pack "+deniedBy, map[string]any{"image": in.Image, "policy": deniedBy})
		errorJSON(w, 403, fmt.Sprintf("image %q denied by policy pack %q", in.Image, deniedBy))
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
	in.DeployedAt = now
	if err := s.store.AddDeployment(in); err != nil {
		errorJSON(w, 507, err.Error())
		return
	}
	s.note("ok", "control-plane", s.actorForBearer(bearer(r)), "", in.SiteID, in.Name, "deployment.create", "deployment created: "+in.Name, map[string]any{"deployment_id": in.ID, "image": in.Image, "version": in.Version})
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
	existing, ok := s.store.Deployment(idv)
	if !ok || !s.callerCanMutateSite(r, existing.SiteID) {
		errorJSON(w, 404, "deployment not found")
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
	if in.Image != nil && *in.Image != "" {
		if ok, deniedBy := policy.Allowed(s.store.PolicyPacks(), existing.SiteID, *in.Image); !ok {
			s.note("warn", "control-plane", s.actorForBearer(bearer(r)), "", existing.SiteID, existing.Name, "policy.deny", "image denied by policy pack "+deniedBy, map[string]any{"image": *in.Image, "policy": deniedBy})
			errorJSON(w, 403, fmt.Sprintf("image %q denied by policy pack %q", *in.Image, deniedBy))
			return
		}
	}
	if err := s.store.UpdateDeployment(idv, func(d *model.Deployment) {
		changingImage := in.Image != nil && *in.Image != "" && *in.Image != d.Image
		changingVersion := in.Version != nil && *in.Version != "" && *in.Version != d.Version
		if (changingImage || changingVersion) && d.ActualState == "running" {
			// Snapshot the currently-running, healthy target so a later
			// health-gated rollback has somewhere known-good to revert to.
			d.LastGoodImage, d.LastGoodVersion = d.Image, d.Version
		}
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
		if changingImage || changingVersion {
			d.DeployedAt = time.Now().UTC()
		}
		d.UpdatedAt = time.Now().UTC()
	}); err != nil {
		errorJSON(w, 404, "deployment not found")
		return
	}
	dep, _ := s.store.Deployment(idv)
	s.note("ok", "control-plane", s.actorForBearer(bearer(r)), "", dep.SiteID, dep.Name, "deployment.patch", "deployment updated: "+idv, map[string]any{"deployment_id": idv})
	writeJSON(w, 200, dep)
}
func (s *Server) deploymentDelete(w http.ResponseWriter, r *http.Request) {
	idv := r.PathValue("id")
	existing, ok := s.store.Deployment(idv)
	if !ok || !s.callerCanMutateSite(r, existing.SiteID) {
		errorJSON(w, 404, "deployment not found")
		return
	}
	if err := s.store.DeleteDeployment(idv); err != nil {
		errorJSON(w, 404, "deployment not found")
		return
	}
	s.note("ok", "control-plane", s.actorForBearer(bearer(r)), "", "", idv, "deployment.delete", "deployment deleted: "+idv, map[string]any{"deployment_id": idv})
	w.WriteHeader(204)
}
func (s *Server) policyPacks(w http.ResponseWriter, r *http.Request) {
	v := s.store.PolicyPacks()
	out := v[:0]
	for _, p := range v {
		if s.callerCanSeeSite(r, p.SiteID) {
			out = append(out, p)
		}
	}
	writeJSON(w, 200, asJSONList(out))
}
func (s *Server) policyPackCreate(w http.ResponseWriter, r *http.Request) {
	var in model.PolicyPack
	if !s.decode(w, r, &in) {
		return
	}
	if in.Name == "" {
		errorJSON(w, 400, "name is required")
		return
	}
	if !s.callerCanMutateSite(r, in.SiteID) {
		errorJSON(w, 403, "an org-scoped token may only create policy packs scoped to its own org's sites")
		return
	}
	now := time.Now().UTC()
	in.ID = id("policy")
	in.Version = 1
	in.CreatedAt = now
	in.UpdatedAt = now
	if err := s.store.AddPolicyPack(in); err != nil {
		errorJSON(w, 507, err.Error())
		return
	}
	s.note("ok", "control-plane", s.actorForBearer(bearer(r)), "", in.SiteID, in.Name, "policy.create", "policy pack created: "+in.Name, map[string]any{"policy_id": in.ID, "allowed_images": in.AllowedImages})
	writeJSON(w, 201, in)
}
func (s *Server) policyPackPatch(w http.ResponseWriter, r *http.Request) {
	idv := r.PathValue("id")
	var in struct {
		Name          *string  `json:"name"`
		Enabled       *bool    `json:"enabled"`
		AllowedImages []string `json:"allowed_images"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	existing, ok := s.store.PolicyPack(idv)
	if !ok || !s.callerCanMutateSite(r, existing.SiteID) {
		errorJSON(w, 404, "policy pack not found")
		return
	}
	if err := s.store.UpdatePolicyPack(idv, func(p *model.PolicyPack) {
		if in.Name != nil && *in.Name != "" {
			p.Name = *in.Name
		}
		if in.Enabled != nil {
			p.Enabled = *in.Enabled
		}
		if in.AllowedImages != nil {
			p.AllowedImages = in.AllowedImages
		}
		p.Version++
		p.UpdatedAt = time.Now().UTC()
	}); err != nil {
		errorJSON(w, 404, "policy pack not found")
		return
	}
	pack, _ := s.store.PolicyPack(idv)
	s.note("ok", "control-plane", s.actorForBearer(bearer(r)), "", pack.SiteID, pack.Name, "policy.patch", "policy pack updated: "+idv, map[string]any{"policy_id": idv, "allowed_images": pack.AllowedImages})
	writeJSON(w, 200, pack)
}
func (s *Server) policyPackDelete(w http.ResponseWriter, r *http.Request) {
	idv := r.PathValue("id")
	existing, ok := s.store.PolicyPack(idv)
	if !ok || !s.callerCanMutateSite(r, existing.SiteID) {
		errorJSON(w, 404, "policy pack not found")
		return
	}
	if err := s.store.DeletePolicyPack(idv); err != nil {
		errorJSON(w, 404, "policy pack not found")
		return
	}
	s.note("ok", "control-plane", s.actorForBearer(bearer(r)), "", "", idv, "policy.delete", "policy pack deleted: "+idv, map[string]any{"policy_id": idv})
	w.WriteHeader(204)
}

// redactedOrg strips the token hashes before an org ever reaches an HTTP
// response — they're durable-storage-only (see model.Org), never exposed,
// not even to the global admin that created them (the plaintext is
// returned exactly once, at orgCreate time).
func redactedOrg(o model.Org) model.Org {
	o.EnrollmentTokenHash, o.AdminTokenHash, o.ViewerTokenHash = "", "", ""
	return o
}

// orgs, orgCreate, and orgDelete are gated to the global admin token only
// (requireGlobalAdmin) — org management is a platform-operator action, not
// something a tenant's own org-scoped admin token can do to itself or to
// other orgs. See docs/ARCHITECTURE.md's "Multi-tenant orgs" section for
// the full, deliberately partial v1 scope: only sites-list and
// single-site-revoke are org-filtered — every other endpoint (devices,
// twins, routes, deployments, alerts, policy-packs, audit, deliveries)
// remains fleet-wide regardless of which token, global or org-scoped, is
// used to call it.
func (s *Server) orgs(w http.ResponseWriter, r *http.Request) {
	if !s.requireGlobalAdmin(w, r) {
		return
	}
	v := s.store.Orgs()
	out := make([]model.Org, len(v))
	for i, o := range v {
		out[i] = redactedOrg(o)
	}
	writeJSON(w, 200, asJSONList(out))
}
func (s *Server) orgCreate(w http.ResponseWriter, r *http.Request) {
	if !s.requireGlobalAdmin(w, r) {
		return
	}
	var in struct {
		Name string `json:"name"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	if strings.TrimSpace(in.Name) == "" {
		errorJSON(w, 400, "name is required")
		return
	}
	enrollTok, err := auth.NewToken(24)
	if err != nil {
		errorJSON(w, 500, "token generation failed")
		return
	}
	adminTok, err := auth.NewToken(24)
	if err != nil {
		errorJSON(w, 500, "token generation failed")
		return
	}
	viewerTok, err := auth.NewToken(24)
	if err != nil {
		errorJSON(w, 500, "token generation failed")
		return
	}
	org := model.Org{
		ID: id("org"), Name: in.Name, CreatedAt: time.Now().UTC(),
		EnrollmentTokenHash: auth.Hash(enrollTok), AdminTokenHash: auth.Hash(adminTok), ViewerTokenHash: auth.Hash(viewerTok),
	}
	if err := s.store.AddOrg(org); err != nil {
		errorJSON(w, 507, err.Error())
		return
	}
	s.note("ok", "control-plane", s.actorForBearer(bearer(r)), "", "", org.Name, "org.create", "org created: "+org.Name, map[string]any{"org_id": org.ID})
	writeJSON(w, 201, map[string]any{
		"org": redactedOrg(org), "enrollment_token": enrollTok, "admin_token": adminTok, "viewer_token": viewerTok,
	})
}
func (s *Server) orgGet(w http.ResponseWriter, r *http.Request) {
	if !s.requireGlobalAdmin(w, r) {
		return
	}
	org, ok := s.store.Org(r.PathValue("id"))
	if !ok {
		errorJSON(w, 404, "org not found")
		return
	}
	writeJSON(w, 200, redactedOrg(org))
}
func (s *Server) orgDelete(w http.ResponseWriter, r *http.Request) {
	if !s.requireGlobalAdmin(w, r) {
		return
	}
	idv := r.PathValue("id")
	if err := s.store.DeleteOrg(idv); err != nil {
		errorJSON(w, 404, "org not found")
		return
	}
	// Sites already enrolled under this org keep their OrgID — they don't
	// get deleted or reassigned — but with no org left to match, only a
	// global admin/viewer token can see them from here on (see sites()).
	s.note("ok", "control-plane", s.actorForBearer(bearer(r)), "", "", idv, "org.delete", "org deleted: "+idv, map[string]any{"org_id": idv})
	w.WriteHeader(204)
}
func (s *Server) alerts(w http.ResponseWriter, r *http.Request) {
	v := s.store.Alerts()
	out := v[:0]
	for _, a := range v {
		if s.callerCanSeeSite(r, a.SiteID) {
			out = append(out, a)
		}
	}
	writeJSON(w, 200, asJSONList(out))
}
func (s *Server) alertResolve(w http.ResponseWriter, r *http.Request) {
	idv := r.PathValue("id")
	found := false
	for _, a := range s.store.Alerts() {
		if a.ID == idv {
			if !s.callerCanMutateSite(r, a.SiteID) {
				errorJSON(w, 404, "alert not found")
				return
			}
			found = true
			break
		}
	}
	if !found {
		errorJSON(w, 404, "alert not found")
		return
	}
	if err := s.store.ResolveAlert(idv); err != nil {
		errorJSON(w, 404, "alert not found")
		return
	}
	s.note("ok", "control-plane", s.actorForBearer(bearer(r)), "", "", idv, "alert.resolve", "alert resolved: "+idv, map[string]any{"alert_id": idv})
	writeJSON(w, 200, map[string]bool{"resolved": true})
}
func (s *Server) eventsList(w http.ResponseWriter, r *http.Request) {
	mins, _ := strconv.Atoi(r.URL.Query().Get("minutes"))
	if mins <= 0 {
		mins = 60
	}
	v := s.store.EventsSince(time.Now().Add(-time.Duration(mins) * time.Minute))
	out := v[:0]
	for _, e := range v {
		if s.callerCanSeeSite(r, e.SiteID) {
			out = append(out, e)
		}
	}
	writeJSON(w, 200, asJSONList(out))
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

// agentDeploymentRollback reverts a deployment to its last known-good
// image/version — the health-gating counterpart to reconcileDocker's
// cosign-verify-before-pull on the agent side. Binary health only (the
// deployment stayed Running past the agent's configured grace period), not
// real app-level health checks, and not a staged/canary campaign.
func (s *Server) agentDeploymentRollback(w http.ResponseWriter, r *http.Request) {
	var in struct {
		SiteID string `json:"site_id"`
		Reason string `json:"reason"`
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
	if dep.LastGoodImage == "" {
		_ = s.store.AddAlert(model.Alert{ID: id("alert"), SiteID: in.SiteID, Severity: "high", Type: "deployment_rollback", Message: "deployment " + dep.Name + " is unhealthy but has no known-good version to roll back to: " + in.Reason, CreatedAt: time.Now().UTC()})
		s.note("warn", "agent", in.SiteID, "", in.SiteID, dep.Name, "deployment.rollback", "no known-good version to roll back to", map[string]any{"deployment_id": idv, "reason": in.Reason})
		writeJSON(w, 200, map[string]bool{"rolled_back": false})
		return
	}
	fromImage, toImage := dep.Image, dep.LastGoodImage
	if err := s.store.UpdateDeployment(idv, func(d *model.Deployment) {
		d.Image = d.LastGoodImage
		d.Version = d.LastGoodVersion
		d.LastGoodImage = ""
		d.LastGoodVersion = ""
		d.DesiredState = "running"
		d.Status = "queued"
		d.DeployedAt = time.Now().UTC()
		d.UpdatedAt = time.Now().UTC()
	}); err != nil {
		errorJSON(w, 404, "deployment not found")
		return
	}
	_ = s.store.AddAlert(model.Alert{ID: id("alert"), SiteID: in.SiteID, Severity: "high", Type: "deployment_rollback", Message: fmt.Sprintf("deployment %s rolled back from %s to %s: %s", dep.Name, fromImage, toImage, in.Reason), CreatedAt: time.Now().UTC()})
	s.note("warn", "agent", in.SiteID, "", in.SiteID, dep.Name, "deployment.rollback", "deployment rolled back", map[string]any{"deployment_id": idv, "from_image": fromImage, "to_image": toImage, "reason": in.Reason})
	writeJSON(w, 200, map[string]bool{"rolled_back": true})
}

func (s *Server) deadletters(w http.ResponseWriter, r *http.Request) {
	items, _ := s.dlq.List()
	out := items[:0]
	for _, dl := range items {
		if s.callerCanSeeSite(r, dl.Delivery.SiteID) {
			out = append(out, dl)
		}
	}
	writeJSON(w, 200, asJSONList(out))
}
func (s *Server) deadletterReplay(w http.ResponseWriter, r *http.Request) {
	idv := r.PathValue("id")
	dl, ok := s.dlq.Get(idv)
	if !ok || !s.callerCanMutateSite(r, dl.Delivery.SiteID) {
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
	idv := r.PathValue("id")
	if dl, ok := s.dlq.Get(idv); ok && !s.callerCanMutateSite(r, dl.Delivery.SiteID) {
		errorJSON(w, 404, "dead letter not found")
		return
	}
	if err := s.dlq.Delete(idv); err != nil {
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

// tryLead reports whether this replica may process deliveries right now,
// updating isLeader for /metrics. Only used in file-WAL/single-process mode
// (see processDeliveries) — Postgres multi-writer mode bypasses this
// entirely in favor of per-item claiming. Non-blocking: on any error it
// treats this tick as not-leader rather than stalling the worker loop.
func (s *Server) tryLead(ctx context.Context) bool {
	held, err := s.leader.TryAcquire(ctx)
	if err != nil {
		slog.Warn("delivery leadership check failed", "error", err)
		held = false
	}
	s.isLeader.Store(held)
	return held
}

// processDeliveries dispatches ready deliveries for this tick. When
// s.deliveries is a queue.Claimer (Postgres, multi-writer HA), every replica
// runs this loop concurrently and claims items individually — there is no
// single elected leader for delivery processing in that mode, so isLeader is
// forced true (every participating replica is "leading" its own claimed
// share). In file-WAL/single-process mode, deliveries doesn't implement
// Claimer, so this falls back to the original single-leader gate via
// tryLead (a no-op check against leader.AlwaysLeader{}).
func (s *Server) processDeliveries(ctx context.Context) {
	claimer, multiWriter := s.deliveries.(queue.Claimer)
	if multiWriter {
		s.isLeader.Store(true)
	} else if !s.tryLead(ctx) {
		return
	}
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
		if multiWriter {
			ok, err := claimer.TryClaim(d.ID, s.instanceID, s.cfg.DeliveryClaimLease)
			if err != nil || !ok {
				continue
			}
		}
		if _, loaded := s.inflight.LoadOrStore(d.ID, struct{}{}); loaded {
			if multiWriter {
				_ = claimer.ReleaseClaim(d.ID)
			}
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
		s.note("error", "control-plane", "system", "", d.SiteID, "", "delivery.dlq", "delivery exhausted retries → dead letter", map[string]any{
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
