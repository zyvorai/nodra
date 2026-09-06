package main

import (
	"context"
	"flag"
	"github.com/zyvorai/nodra/internal/server"
	"log/slog"
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
	enroll := flag.String("enrollment-token", os.Getenv("NODRA_ENROLLMENT_TOKEN"), "site enrollment token")
	public := flag.Bool("public-read", false, "allow unauthenticated management GET requests")
	workers := flag.Int("workers", envInt("NODRA_DELIVERY_WORKERS", 8), "delivery worker concurrency")
	pkiEnabled := flag.Bool("pki", envBool("NODRA_PKI_ENABLED", false), "enable CSR signing for site identities")
	tlsCert := flag.String("tls-cert", os.Getenv("NODRA_TLS_CERT"), "TLS server certificate")
	tlsKey := flag.String("tls-key", os.Getenv("NODRA_TLS_KEY"), "TLS server key")
	clientCA := flag.String("client-ca", os.Getenv("NODRA_CLIENT_CA"), "optional client CA for mTLS")
	flag.Parse()
	if *admin == "" || *enroll == "" {
		slog.Warn("authentication token missing; management or enrollment APIs will be unavailable")
	}
	srv, err := server.New(server.Config{
		Listen: *listen, DataDir: *data,
		AdminToken: *admin, AdminUser: *adminUser, AdminPassword: *adminPass,
		EnrollmentToken: *enroll, PublicRead: *public, WorkerConcurrency: *workers,
		PKIEnabled: *pkiEnabled, TLSCertFile: *tlsCert, TLSKeyFile: *tlsKey, ClientCAFile: *clientCA,
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
