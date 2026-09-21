// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	_ "github.com/zyvorai/nodra/connectors/j1939"  // register Device Agent J1939 connector factory
	_ "github.com/zyvorai/nodra/connectors/modbus" // register modbus connector factory
	_ "github.com/zyvorai/nodra/connectors/nats"   // register NATS bridge connector factory
	_ "github.com/zyvorai/nodra/connectors/opcua"  // register OPC-UA connector factory
	_ "github.com/zyvorai/nodra/connectors/serial" // register generic serial connector factory
	"github.com/zyvorai/nodra/internal/agent"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "init" {
		initConfig(os.Args[2:])
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "ztp" {
		ztpBootstrap(os.Args[2:])
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
	localToken := fs.String("local-token", "", "local publish bearer; required unless --allow-unauthenticated-local")
	allowOpen := fs.Bool("allow-unauthenticated-local", false, "allow local HTTP ingest with no bearer (development only)")
	mqttUser := fs.String("mqtt-username", "", "MQTT CONNECT username; requires --mqtt-password")
	mqttPass := fs.String("mqtt-password", "", "MQTT CONNECT password")
	mqttListen := fs.String("mqtt-listen", "127.0.0.1:1883", "MQTT 3.1.1 edge ingress address; empty disables")
	mqttCert := fs.String("mqtt-tls-cert", "", "PEM certificate for MQTT TLS; requires --mqtt-tls-key")
	mqttKey := fs.String("mqtt-tls-key", "", "PEM private key for MQTT TLS")
	mqttClientCA := fs.String("mqtt-client-ca", "", "PEM CA that may issue MQTT client certificates")
	mqttRequireClient := fs.Bool("mqtt-require-client-cert", false, "refuse MQTT clients that do not present a certificate")
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
	cfg.AllowUnauthenticatedLocal = *allowOpen
	cfg.MQTTUsername = *mqttUser
	cfg.MQTTPassword = *mqttPass
	cfg.MQTTListen = *mqttListen
	cfg.MQTTCertFile = *mqttCert
	cfg.MQTTKeyFile = *mqttKey
	cfg.MQTTClientCAFile = *mqttClientCA
	cfg.MQTTRequireClientCert = *mqttRequireClient
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

func ztpBootstrap(args []string) {
	fs := flag.NewFlagSet("ztp", flag.ExitOnError)
	path := fs.String("config", "./nodrad.json", "config path to write")
	serverURL := fs.String("server", "http://127.0.0.1:8080", "control-plane URL")
	site := fs.String("site", "", "site name")
	token := fs.String("enrollment-token", "", "enrollment token")
	data := fs.String("data", "./nodra-agent-data", "data directory")
	listen := fs.String("listen", "127.0.0.1:9091", "local ingest listen address")
	mqttListen := fs.String("mqtt-listen", "127.0.0.1:1883", "MQTT listen address")
	insecure := fs.Bool("insecure", false, "skip TLS verify for lab HTTPS")
	_ = fs.Parse(args)
	if strings.TrimSpace(*site) == "" || *token == "" {
		fmt.Fprintln(os.Stderr, "ztp requires --site and --enrollment-token")
		os.Exit(2)
	}
	body, _ := json.Marshal(map[string]any{
		"name": *site, "enrollment_token": *token,
		"listen": *listen, "mqtt_listen": *mqttListen,
	})
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(*serverURL, "/")+"/api/v1/ztp/bootstrap", bytes.NewReader(body))
	if err != nil {
		panic(err)
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 30 * time.Second}
	if *insecure {
		client.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}} //nolint:gosec
	}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		fmt.Fprintf(os.Stderr, "ztp bootstrap failed: %s\n%s\n", resp.Status, raw)
		os.Exit(1)
	}
	var out struct {
		AgentConfig json.RawMessage `json:"agent_config"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || len(out.AgentConfig) == 0 {
		fmt.Fprintln(os.Stderr, "invalid ztp response:", string(raw))
		os.Exit(1)
	}
	var cfg agent.Config
	if err := json.Unmarshal(out.AgentConfig, &cfg); err != nil {
		panic(err)
	}
	cfg.DataDir = *data
	cfg.ServerURL = strings.TrimRight(*serverURL, "/")
	if err := os.MkdirAll(filepath.Dir(*path), 0o750); err != nil {
		panic(err)
	}
	if err := agent.SaveConfig(*path, cfg); err != nil {
		panic(err)
	}
	b, _ := json.MarshalIndent(cfg, "", "  ")
	fmt.Println(string(b))
	fmt.Printf("\nZTP wrote %s (site_id=%s)\n", *path, cfg.SiteID)
}

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
