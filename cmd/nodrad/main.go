// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	_ "github.com/zyvorai/nodra/connectors/modbus" // register modbus connector factory
	"github.com/zyvorai/nodra/internal/agent"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "init" {
		initConfig(os.Args[2:])
		return
	}
	fs := flag.NewFlagSet("nodrad", flag.ExitOnError)
	config := fs.String("config", env("NODRA_AGENT_CONFIG", "./nodrad.json"), "agent config path")
	_ = fs.Parse(os.Args[1:])
	cfg, err := agent.LoadConfig(*config)
	if err != nil {
		slog.Error("load config", "error", err)
		os.Exit(1)
	}
	a, err := agent.New(cfg, *config)
	if err != nil {
		slog.Error("init agent", "error", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = a.Shutdown(c)
	}()
	if err = a.Run(context.Background()); err != nil {
		slog.Error("agent stopped", "error", err)
		os.Exit(1)
	}
}
func initConfig(args []string) {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	path := fs.String("config", "./nodrad.json", "config path")
	serverURL := fs.String("server", "http://127.0.0.1:8080", "control-plane URL")
	site := fs.String("site", "edge-site", "site name")
	token := fs.String("enrollment-token", "", "enrollment token")
	data := fs.String("data", "./nodra-agent-data", "data directory")
	listen := fs.String("listen", "127.0.0.1:9091", "local ingest listen address")
	localToken := fs.String("local-token", "", "optional local publish token")
	mqttListen := fs.String("mqtt-listen", "127.0.0.1:1883", "MQTT 3.1.1 edge ingress address; empty disables")
	spoolBytes := fs.Int64("max-spool-bytes", 2<<30, "maximum durable cloud spool bytes")
	spoolEvents := fs.Int("max-spool-events", 1000000, "maximum durable cloud spool events")
	spoolPolicy := fs.String("spool-policy", "reject", "reject|drop-oldest|drop-newest")
	requestCert := fs.Bool("request-certificate", false, "request a site client certificate during enrollment")
	runner := fs.String("runner", "none", "app runner: none|docker")
	_ = fs.Parse(args)
	cfg := agent.DefaultConfig()
	cfg.ServerURL = *serverURL
	cfg.SiteName = *site
	cfg.EnrollmentToken = *token
	cfg.DataDir = *data
	cfg.Listen = *listen
	cfg.LocalToken = *localToken
	cfg.MQTTListen = *mqttListen
	cfg.MaxSpoolBytes = *spoolBytes
	cfg.MaxSpoolEvents = *spoolEvents
	cfg.SpoolPolicy = *spoolPolicy
	cfg.RequestCertificate = *requestCert
	cfg.Runner = *runner
	if err := os.MkdirAll(filepath.Dir(*path), 0o750); err != nil {
		panic(err)
	}
	if err := agent.SaveConfig(*path, cfg); err != nil {
		panic(err)
	}
	b, _ := json.MarshalIndent(cfg, "", "  ")
	fmt.Println(string(b))
	fmt.Printf("\nCreated %s\n", *path)
}
func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
