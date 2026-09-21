// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"flag"
	"github.com/zyvorai/nodra/internal/server"
	"log/slog"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

func main() {
	listen := flag.String("listen", env("NODRA_LISTEN", ":8080"), "listen address")
	data := flag.String("data", env("NODRA_DATA_DIR", "./data"), "data directory")
	admin := flag.String("admin-token", os.Getenv("NODRA_ADMIN_TOKEN"), "admin bearer token")
	adminUser := flag.String("admin-user", env("NODRA_ADMIN_USER", "admin"), "console login username")
	adminPass := flag.String("admin-password", os.Getenv("NODRA_ADMIN_PASSWORD"), "console login password (defaults to admin-token)")
	viewer := flag.String("viewer-token", os.Getenv("NODRA_VIEWER_TOKEN"), "optional viewer bearer token (read-only)")
	viewerUser := flag.String("viewer-user", env("NODRA_VIEWER_USER", "viewer"), "viewer console username")
	viewerPass := flag.String("viewer-password", os.Getenv("NODRA_VIEWER_PASSWORD"), "viewer password (defaults to viewer-token)")
	enroll := flag.String("enrollment-token", os.Getenv("NODRA_ENROLLMENT_TOKEN"), "site enrollment token")
	storeDriver := flag.String("store", env("NODRA_STORE", "file"), "fleet store: file|postgres")
	databaseURL := flag.String("database-url", os.Getenv("NODRA_DATABASE_URL"), "postgres DSN when --store=postgres")
	public := flag.Bool("public-read", false, "allow unauthenticated management GET requests")
	workers := flag.Int("workers", envInt("NODRA_DELIVERY_WORKERS", 8), "delivery worker concurrency")
	pkiEnabled := flag.Bool("pki", envBool("NODRA_PKI_ENABLED", false), "enable CSR signing for site identities")
	tlsCert := flag.String("tls-cert", os.Getenv("NODRA_TLS_CERT"), "TLS server certificate")
	tlsKey := flag.String("tls-key", os.Getenv("NODRA_TLS_KEY"), "TLS server key")
	clientCA := flag.String("client-ca", os.Getenv("NODRA_CLIENT_CA"), "optional client CA for mTLS")
	requireClientCert := flag.Bool("require-client-cert", envBool("NODRA_REQUIRE_CLIENT_CERT", false), "require a client certificate when --client-ca is set")
	publicBaseURL := flag.String("public-base-url", os.Getenv("NODRA_PUBLIC_BASE_URL"), "externally-reachable base URL, embedded as a CRLDistributionPoint in issued certs")
	oidcIssuerURL := flag.String("oidc-issuer-url", os.Getenv("NODRA_OIDC_ISSUER_URL"), "OIDC issuer URL; enables SSO console login when set with --oidc-client-id")
	oidcClientID := flag.String("oidc-client-id", os.Getenv("NODRA_OIDC_CLIENT_ID"), "OIDC client ID")
	oidcClientSecret := flag.String("oidc-client-secret", os.Getenv("NODRA_OIDC_CLIENT_SECRET"), "OIDC client secret")
	oidcRedirectURL := flag.String("oidc-redirect-url", os.Getenv("NODRA_OIDC_REDIRECT_URL"), "OIDC redirect URL registered with the IdP")
	oidcGroupsClaim := flag.String("oidc-groups-claim", env("NODRA_OIDC_GROUPS_CLAIM", "groups"), "ID token claim carrying the caller's groups")
	oidcAdminGroup := flag.String("oidc-admin-group", os.Getenv("NODRA_OIDC_ADMIN_GROUP"), "group value granting the admin role via OIDC")
	oidcViewerGroup := flag.String("oidc-viewer-group", os.Getenv("NODRA_OIDC_VIEWER_GROUP"), "group value granting the viewer role via OIDC")
	sessionTTL := flag.Duration("session-ttl", envDuration("NODRA_SESSION_TTL", time.Hour), "TTL for password/OIDC console sessions; static admin/viewer tokens do not expire")
	enrollTTL := flag.Duration("enrollment-token-ttl", envDuration("NODRA_ENROLLMENT_TOKEN_TTL", 0), "expire the bootstrap enrollment token after this duration; 0 means no expiry")
	otlpEndpoint := flag.String("otlp-endpoint", os.Getenv("NODRA_OTLP_ENDPOINT"), "OTLP/HTTP collector base URL (metrics pushed to /v1/metrics)")
	otlpInterval := flag.Duration("otlp-interval", envDuration("NODRA_OTLP_INTERVAL", 30*time.Second), "how often to push OTLP metrics when --otlp-endpoint is set")
	flag.Parse()
	if *admin == "" || *enroll == "" {
		slog.Warn("authentication token missing; management or enrollment APIs will be unavailable")
	}
	srv, err := server.New(server.Config{
		Listen: *listen, DataDir: *data,
		AdminToken: *admin, AdminUser: *adminUser, AdminPassword: *adminPass,
		ViewerToken: *viewer, ViewerUser: *viewerUser, ViewerPassword: *viewerPass,
		EnrollmentToken: *enroll, PublicRead: *public, WorkerConcurrency: *workers,
		PKIEnabled: *pkiEnabled, TLSCertFile: *tlsCert, TLSKeyFile: *tlsKey, ClientCAFile: *clientCA, RequireClientCert: *requireClientCert,
		StoreDriver: *storeDriver, DatabaseURL: *databaseURL, PublicBaseURL: *publicBaseURL,
		OIDCIssuerURL: *oidcIssuerURL, OIDCClientID: *oidcClientID, OIDCClientSecret: *oidcClientSecret,
		OIDCRedirectURL: *oidcRedirectURL, OIDCGroupsClaim: *oidcGroupsClaim,
		OIDCAdminGroup: *oidcAdminGroup, OIDCViewerGroup: *oidcViewerGroup,
		SessionTTL: *sessionTTL, EnrollmentTokenTTL: *enrollTTL,
		OTLPEndpoint: *otlpEndpoint, OTLPInterval: *otlpInterval,
	})
	if err != nil {
		slog.Error("init failed", "error", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(c)
	}()
	if addr := os.Getenv("NODRA_PPROF"); addr != "" {
		go func() {
			slog.Info("pprof listening", "addr", addr)
			if err := http.ListenAndServe(addr, nil); err != nil {
				slog.Error("pprof stopped", "error", err)
			}
		}()
	}
	slog.Info("Nodra control plane starting", "listen", *listen, "workers", *workers, "pki", *pkiEnabled)
	if err = srv.Start(context.Background()); err != nil {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}
func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
func envInt(k string, d int) int {
	if v := os.Getenv(k); v != "" {
		if n, e := strconv.Atoi(v); e == nil {
			return n
		}
	}
	return d
}
func envBool(k string, d bool) bool {
	if v := os.Getenv(k); v != "" {
		if b, e := strconv.ParseBool(v); e == nil {
			return b
		}
	}
	return d
}
func envDuration(k string, d time.Duration) time.Duration {
	if v := os.Getenv(k); v != "" {
		if n, e := time.ParseDuration(v); e == nil {
			return n
		}
	}
	return d
}
