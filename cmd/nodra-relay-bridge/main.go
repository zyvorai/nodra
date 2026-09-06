// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

// Command nodra-relay-bridge accepts Nodra cloud-route webhooks and POSTs them
// into Zyvor Relay Accept (direct or via relay-pubsub gateway).
package main

import (
	"encoding/json"
	"flag"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/zyvorai/nodra/internal/relaybridge"
)

func main() {
	listen := flag.String("listen", env("NODRA_RELAY_BRIDGE_LISTEN", ":8095"), "listen address")
	relayBase := flag.String("relay-url", os.Getenv("RELAY_BASE_URL"), "Zyvor Relay base URL")
	relayToken := flag.String("relay-token", os.Getenv("RELAY_AUTH_TOKEN"), "Relay bearer JWT")
	gatewayBase := flag.String("gateway-url", os.Getenv("GATEWAY_BASE_URL"), "optional relay-pubsub base URL")
	gatewayToken := flag.String("gateway-token", os.Getenv("GATEWAY_AUTH_TOKEN"), "optional gateway bearer")
	project := flag.String("project", env("FASAL_GCP_PROJECT", "fasal-onprem"), "pubsub project id for gateway path")
	tlsInsecure := flag.Bool("tls-insecure", envBool("RELAY_TLS_INSECURE", false), "skip TLS verify for lab certs")
	flag.Parse()

	if strings.TrimSpace(*relayBase) == "" && strings.TrimSpace(*gatewayBase) == "" {
		slog.Error("set RELAY_BASE_URL and/or GATEWAY_BASE_URL")
		os.Exit(2)
	}

	client := &relaybridge.Client{
		RelayBase:    *relayBase,
		RelayToken:   *relayToken,
		GatewayBase:  *gatewayBase,
		GatewayToken: *gatewayToken,
		Project:      *project,
		TLSInsecure:  *tlsInsecure,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","service":"nodra-relay-bridge"}`))
	})
	mux.HandleFunc("POST /v1/nodra/delivery", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			http.Error(w, `{"error":"read body"}`, http.StatusBadRequest)
			return
		}
		var d relaybridge.NodraDelivery
		if err := json.Unmarshal(body, &d); err != nil {
			http.Error(w, `{"error":"invalid json"}`, http.StatusBadRequest)
			return
		}
		if d.EventID == "" && d.DeliveryID == "" {
			http.Error(w, `{"error":"event_id or delivery_id required"}`, http.StatusBadRequest)
			return
		}
		ev := relaybridge.MapDelivery(d)
		res, err := client.Publish(ev)
		if err != nil {
			slog.Warn("relay publish failed", "error", err, "type", ev.Type, "idempotency_key", ev.IdempotencyKey)
			http.Error(w, `{"error":`+strconv.Quote(err.Error())+`}`, http.StatusBadGateway)
			return
		}
		slog.Info("relay accepted", "path", res.Path, "type", ev.Type, "relay_event_id", res.EventID, "idempotency_key", ev.IdempotencyKey)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"accepted": true,
			"path":     res.Path,
			"event_id": res.EventID,
			"type":     ev.Type,
		})
	})

	srv := &http.Server{Addr: *listen, Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second}
	slog.Info("nodra-relay-bridge listening", "listen", *listen, "relay", *relayBase, "gateway", *gatewayBase)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
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

func envBool(k string, d bool) bool {
	if v := os.Getenv(k); v != "" {
		b, err := strconv.ParseBool(v)
		if err == nil {
			return b
		}
	}
	return d
}
