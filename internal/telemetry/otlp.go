// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

// OTLPExporter pushes a minimal OTLP/HTTP JSON metrics payload to an
// OpenTelemetry collector. It does not pull in the full OpenTelemetry SDK.
type OTLPExporter struct {
	Endpoint string
	Interval time.Duration
	Client   *http.Client
	Source   func() map[string]float64
}

func (e *OTLPExporter) Run(ctx context.Context) {
	if e.Endpoint == "" || e.Source == nil {
		return
	}
	interval := e.Interval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	client := e.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := e.push(ctx, client); err != nil {
				slog.Warn("otlp export failed", "error", err)
			}
		}
	}
}

func (e *OTLPExporter) push(ctx context.Context, client *http.Client) error {
	gauges := e.Source()
	now := time.Now().UnixNano()
	metrics := make([]map[string]any, 0, len(gauges))
	for name, v := range gauges {
		metrics = append(metrics, map[string]any{
			"name": name,
			"gauge": map[string]any{
				"dataPoints": []map[string]any{{
					"asDouble":          v,
					"timeUnixNano":      fmt.Sprintf("%d", now),
					"startTimeUnixNano": fmt.Sprintf("%d", now),
				}},
			},
		})
	}
	body := map[string]any{
		"resourceMetrics": []map[string]any{{
			"resource": map[string]any{
				"attributes": []map[string]any{{
					"key": "service.name", "value": map[string]any{"stringValue": "nodra"},
				}},
			},
			"scopeMetrics": []map[string]any{{
				"scope":   map[string]any{"name": "nodra"},
				"metrics": metrics,
			}},
		}},
	}
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	url := stringsTrimSlash(e.Endpoint) + "/v1/metrics"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("otlp status %d", resp.StatusCode)
	}
	return nil
}

func stringsTrimSlash(s string) string {
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}
