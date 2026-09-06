// Copyright 2026 Zyvor
// SPDX-License-Identifier: Apache-2.0

// Command nodra-sim seeds a customer-demo fleet and continuously publishes
// heartbeats + telemetry so a Nodra console looks live in pods or locally.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

type siteSpec struct {
	Name     string
	Region   string
	Device   string
	Protocol string
	Topic    string
}

var defaultSites = []siteSpec{
	{Name: "Demo Factory Floor", Region: "plant-a", Device: "Line-1 PLC", Protocol: "mqtt", Topic: "factory/line1/telemetry"},
	{Name: "Demo Warehouse Dock", Region: "dc-west", Device: "Dock Scanner", Protocol: "http", Topic: "warehouse/dock/scans"},
	{Name: "Demo Retail Edge", Region: "store-42", Device: "POS Gateway", Protocol: "mqtt", Topic: "retail/pos/events"},
}

type enrolledSite struct {
	Name       string `json:"name"`
	SiteID     string `json:"site_id"`
	AgentToken string `json:"agent_token"`
	DeviceID   string `json:"device_id"`
	Topic      string `json:"topic"`
}

type stateFile struct {
	Sites []enrolledSite `json:"sites"`
}

type client struct {
	base  string
	admin string
	http  *http.Client
}

func main() {
	server := flag.String("server", env("NODRA_SERVER", "http://127.0.0.1:8080"), "control plane base URL")
	admin := flag.String("admin-token", env("NODRA_ADMIN_TOKEN", ""), "admin bearer token")
	enroll := flag.String("enrollment-token", env("NODRA_ENROLLMENT_TOKEN", ""), "enrollment token")
	statePath := flag.String("state", env("NODRA_SIM_STATE", "/tmp/nodra-sim-state.json"), "persistent sim state path")
	interval := flag.Duration("interval", 12*time.Second, "heartbeat/event loop interval")
	once := flag.Bool("once", false, "seed fleet then exit (no continuous simulation)")
	sitesN := flag.Int("sites", len(defaultSites), "number of demo sites to simulate (1-3)")
	flag.Parse()

	if *admin == "" || *enroll == "" {
		slog.Error("NODRA_ADMIN_TOKEN and NODRA_ENROLLMENT_TOKEN are required")
		os.Exit(2)
	}
	if *sitesN < 1 {
		*sitesN = 1
	}
	if *sitesN > len(defaultSites) {
		*sitesN = len(defaultSites)
	}

	c := &client{base: trimSlash(*server), admin: *admin, http: &http.Client{Timeout: 15 * time.Second}}
	if err := c.waitReady(2 * time.Minute); err != nil {
		slog.Error("control plane not ready", "error", err)
		os.Exit(1)
	}

	st, err := loadState(*statePath)
	if err != nil || len(st.Sites) == 0 {
		st, err = c.seed(defaultSites[:*sitesN], *enroll)
		if err != nil {
			slog.Error("seed failed", "error", err)
			os.Exit(1)
		}
		if err := saveState(*statePath, st); err != nil {
			slog.Warn("could not persist sim state", "error", err)
		}
		slog.Info("customer demo fleet seeded", "sites", len(st.Sites))
	} else {
		slog.Info("resuming simulated fleet", "sites", len(st.Sites), "state", *statePath)
	}

	if *once {
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ticker := time.NewTicker(*interval)
	defer ticker.Stop()
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))

	c.tick(st.Sites, rng)
	for {
		select {
		case <-ctx.Done():
			slog.Info("simulator stopped")
			return
		case <-ticker.C:
			c.tick(st.Sites, rng)
		}
	}
}

func (c *client) waitReady(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := c.http.Get(c.base + "/readyz")
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == 200 {
				return nil
			}
		}
		time.Sleep(400 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for %s/readyz", c.base)
}

func (c *client) seed(specs []siteSpec, enrollTok string) (*stateFile, error) {
	out := &stateFile{}
	for _, spec := range specs {
		site, err := c.enroll(spec, enrollTok)
		if err != nil {
			return nil, fmt.Errorf("enroll %s: %w", spec.Name, err)
		}
		devID, err := c.registerDevice(site.SiteID, site.AgentToken, spec)
		if err != nil {
			return nil, fmt.Errorf("device %s: %w", spec.Name, err)
		}
		site.DeviceID = devID
		site.Topic = spec.Topic
		if err := c.putTwin(site.DeviceID, map[string]any{"mode": "auto", "demo": true}); err != nil {
			slog.Warn("twin desired", "site", spec.Name, "error", err)
		}
		out.Sites = append(out.Sites, *site)
		slog.Info("site online", "name", spec.Name, "site_id", site.SiteID, "device", devID)
	}

	if err := c.ensureRoute("Demo MES webhook", "demo/+/telemetry", "http://127.0.0.1:9/hook"); err != nil {
		slog.Warn("demo route", "error", err)
	}
	if err := c.ensureRoute("Factory telemetry", "factory/+/telemetry", "http://127.0.0.1:9/mes"); err != nil {
		slog.Warn("factory route", "error", err)
	}
	if len(out.Sites) > 0 {
		if err := c.ensureDeployment(out.Sites[0].SiteID, "demo-vision", "example/vision:1.0"); err != nil {
			slog.Warn("demo deployment", "error", err)
		}
	}
	return out, nil
}

func (c *client) tick(sites []enrolledSite, rng *rand.Rand) {
	for _, site := range sites {
		q := rng.Intn(8)
		_, err := c.do("POST", "/api/v1/heartbeat", site.AgentToken, map[string]any{
			"site_id": site.SiteID,
			"version": "0.2.0-sim",
			"metrics": map[string]any{"queue_depth": q, "queue_bytes": q * 1024, "sim": true},
		})
		if err != nil {
			slog.Warn("heartbeat", "site", site.Name, "error", err)
			continue
		}
		temp := 35.0 + rng.Float64()*12
		_, err = c.do("POST", "/api/v1/events", site.AgentToken, map[string]any{
			"site_id": site.SiteID,
			"topic":   site.Topic,
			"payload": map[string]any{
				"temperature_c": round1(temp),
				"ok":            true,
				"source":        "nodra-sim",
				"ts":            time.Now().UTC().Format(time.RFC3339),
			},
		})
		if err != nil {
			slog.Warn("event", "site", site.Name, "error", err)
		}
	}
}

func (c *client) enroll(spec siteSpec, enrollTok string) (*enrolledSite, error) {
	body, err := c.do("POST", "/api/v1/enroll", "", map[string]any{
		"name":             spec.Name,
		"enrollment_token": enrollTok,
		"metadata":         map[string]string{"region": spec.Region, "demo": "true", "sim": "true"},
	})
	if err != nil {
		return nil, err
	}
	var resp struct {
		SiteID     string `json:"site_id"`
		AgentToken string `json:"agent_token"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	if resp.SiteID == "" || resp.AgentToken == "" {
		return nil, fmt.Errorf("missing enroll credentials: %s", body)
	}
	return &enrolledSite{Name: spec.Name, SiteID: resp.SiteID, AgentToken: resp.AgentToken}, nil
}

func (c *client) registerDevice(siteID, agentTok string, spec siteSpec) (string, error) {
	body, err := c.do("POST", "/api/v1/devices/register", agentTok, map[string]any{
		"site_id": siteID, "name": spec.Device, "protocol": spec.Protocol,
	})
	if err != nil {
		return "", err
	}
	var resp struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", err
	}
	if resp.ID == "" {
		return "", fmt.Errorf("device id missing: %s", body)
	}
	return resp.ID, nil
}

func (c *client) putTwin(deviceID string, desired map[string]any) error {
	_, err := c.do("PUT", "/api/v1/twins/"+deviceID+"/desired", c.admin, map[string]any{"desired": desired})
	return err
}

func (c *client) ensureRoute(name, topic, target string) error {
	body, err := c.do("GET", "/api/v1/routes", c.admin, nil)
	if err != nil {
		return err
	}
	var routes []map[string]any
	_ = json.Unmarshal(body, &routes)
	for _, r := range routes {
		if r["name"] == name {
			return nil
		}
	}
	_, err = c.do("POST", "/api/v1/routes", c.admin, map[string]any{
		"name": name, "topic": topic, "target_url": target,
		"method": "POST", "enabled": true, "retry_max": 2, "timeout_seconds": 2,
	})
	return err
}

func (c *client) ensureDeployment(siteID, name, image string) error {
	body, err := c.do("GET", "/api/v1/deployments", c.admin, nil)
	if err != nil {
		return err
	}
	var deps []map[string]any
	_ = json.Unmarshal(body, &deps)
	for _, d := range deps {
		if d["name"] == name {
			return nil
		}
	}
	_, err = c.do("POST", "/api/v1/deployments", c.admin, map[string]any{
		"site_id": siteID, "name": name, "version": "1.0.0",
		"image": image, "desired_state": "running",
	})
	return err
}

func (c *client) do(method, path, token string, payload any) ([]byte, error) {
	var rdr io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.base+path, rdr)
	if err != nil {
		return nil, err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		return body, fmt.Errorf("%s %s -> %d %s", method, path, resp.StatusCode, body)
	}
	return body, nil
}

func loadState(path string) (*stateFile, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return &stateFile{}, err
	}
	var st stateFile
	if err := json.Unmarshal(b, &st); err != nil {
		return &stateFile{}, err
	}
	return &st, nil
}

func saveState(path string, st *stateFile) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func trimSlash(s string) string {
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}

func round1(v float64) float64 {
	return float64(int(v*10+0.5)) / 10
}
