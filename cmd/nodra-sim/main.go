// Copyright 2026 Zyvor
// SPDX-License-Identifier: Apache-2.0

// Command nodra-sim seeds an A–Z customer-demo fleet and continuously publishes
// heartbeats, telemetry, twin sync, and detailed activity logs for the console.
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
	"strings"
	"syscall"
	"time"
)

type siteSpec struct {
	Letter   string
	Name     string
	Region   string
	Device   string
	Protocol string
	Topic    string
	Story    string
}

// azSites is the full A–Z customer demo catalog (26 lettered simulations).
var azSites = []siteSpec{
	{Letter: "A", Name: "Site A — Assembly", Region: "plant-a", Device: "Assembler PLC", Protocol: "mqtt", Topic: "factory/a/telemetry", Story: "Assemble line telemetry"},
	{Letter: "B", Name: "Site B — Bottling", Region: "plant-b", Device: "Filler Contoller", Protocol: "mqtt", Topic: "factory/b/fill", Story: "Bottling fill levels"},
	{Letter: "C", Name: "Site C — Cold chain", Region: "cold-01", Device: "Reefer Probe", Protocol: "http", Topic: "cold/c/temp", Story: "Cold-chain temperature"},
	{Letter: "D", Name: "Site D — Dispatch", Region: "dc-east", Device: "Yard Gate", Protocol: "http", Topic: "yard/d/gate", Story: "Dispatch gate scans"},
	{Letter: "E", Name: "Site E — Energy", Region: "util-1", Device: "Meter Gateway", Protocol: "mqtt", Topic: "energy/e/kw", Story: "Energy demand"},
	{Letter: "F", Name: "Site F — Fabrication", Region: "fab-2", Device: "CNC Twin", Protocol: "mqtt", Topic: "fab/f/cnc", Story: "CNC spindle load"},
	{Letter: "G", Name: "Site G — Greenhouse", Region: "agri-g", Device: "Climate Hub", Protocol: "mqtt", Topic: "agri/g/climate", Story: "Greenhouse climate"},
	{Letter: "H", Name: "Site H — Harbor", Region: "port-h", Device: "Crane PLC", Protocol: "modbus", Topic: "harbor/h/crane", Story: "Harbor crane status"},
	{Letter: "I", Name: "Site I — Inspection", Region: "qa-i", Device: "Vision Cam", Protocol: "http", Topic: "qa/i/vision", Story: "Inline inspection"},
	{Letter: "J", Name: "Site J — Junction", Region: "rail-j", Device: "Signal Box", Protocol: "mqtt", Topic: "rail/j/signal", Story: "Rail junction signals"},
	{Letter: "K", Name: "Site K — Kiln", Region: "plant-k", Device: "Kiln Sensor", Protocol: "mqtt", Topic: "plant/k/kiln", Story: "Kiln temperature"},
	{Letter: "L", Name: "Site L — Logistics", Region: "dc-west", Device: "Sorter Hub", Protocol: "http", Topic: "logistics/l/sort", Story: "Parcel sorter"},
	{Letter: "M", Name: "Site M — Mining", Region: "mine-m", Device: "Haul Truck", Protocol: "mqtt", Topic: "mine/m/haul", Story: "Haul truck telemetry"},
	{Letter: "N", Name: "Site N — Nuclear edge", Region: "lab-n", Device: "Safety PLC", Protocol: "mqtt", Topic: "lab/n/safety", Story: "Safety interlocks"},
	{Letter: "O", Name: "Site O — Oilfield", Region: "field-o", Device: "Wellhead RTU", Protocol: "modbus", Topic: "oil/o/well", Story: "Wellhead pressure"},
	{Letter: "P", Name: "Site P — Pharmacy", Region: "pharma-p", Device: "Cleanroom Hub", Protocol: "mqtt", Topic: "pharma/p/room", Story: "Cleanroom pressure"},
	{Letter: "Q", Name: "Site Q — Quarry", Region: "quarry-q", Device: "Crusher PLC", Protocol: "mqtt", Topic: "quarry/q/crush", Story: "Crusher load"},
	{Letter: "R", Name: "Site R — Retail", Region: "store-42", Device: "POS Gateway", Protocol: "mqtt", Topic: "retail/r/pos", Story: "POS events"},
	{Letter: "S", Name: "Site S — Steel", Region: "mill-s", Device: "Caster PLC", Protocol: "mqtt", Topic: "steel/s/caster", Story: "Caster speed"},
	{Letter: "T", Name: "Site T — Transit", Region: "metro-t", Device: "Platform Hub", Protocol: "http", Topic: "transit/t/platform", Story: "Platform occupancy"},
	{Letter: "U", Name: "Site U — Utilities", Region: "grid-u", Device: "Substation", Protocol: "mqtt", Topic: "grid/u/sub", Story: "Substation health"},
	{Letter: "V", Name: "Site V — Vision edge", Region: "edge-v", Device: "GPU Box", Protocol: "http", Topic: "vision/v/infer", Story: "Edge inference"},
	{Letter: "W", Name: "Site W — Warehouse", Region: "dc-w", Device: "Dock Scanner", Protocol: "http", Topic: "warehouse/w/dock", Story: "Dock scans"},
	{Letter: "X", Name: "Site X — X-ray line", Region: "qa-x", Device: "XRay Controller", Protocol: "mqtt", Topic: "qa/x/xray", Story: "X-ray rejects"},
	{Letter: "Y", Name: "Site Y — Yard", Region: "yard-y", Device: "RFID Portal", Protocol: "http", Topic: "yard/y/rfid", Story: "Yard RFID"},
	{Letter: "Z", Name: "Site Z — Zinc plant", Region: "smelt-z", Device: "Smelter PLC", Protocol: "mqtt", Topic: "smelt/z/temp", Story: "Smelter temperature"},
}

type enrolledSite struct {
	Letter     string `json:"letter"`
	Name       string `json:"name"`
	SiteID     string `json:"site_id"`
	AgentToken string `json:"agent_token"`
	DeviceID   string `json:"device_id"`
	Topic      string `json:"topic"`
	Story      string `json:"story"`
	Protocol   string `json:"protocol"`
}

type stateFile struct {
	Sites   []enrolledSite `json:"sites"`
	Chapter int            `json:"chapter"`
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
	interval := flag.Duration("interval", 8*time.Second, "heartbeat/event/chapter loop interval")
	once := flag.Bool("once", false, "seed fleet then exit (no continuous simulation)")
	sitesN := flag.Int("sites", 26, "number of A–Z sites to simulate (1-26)")
	flag.Parse()

	if *admin == "" || *enroll == "" {
		slog.Error("NODRA_ADMIN_TOKEN and NODRA_ENROLLMENT_TOKEN are required")
		os.Exit(2)
	}
	if *sitesN < 1 {
		*sitesN = 1
	}
	if *sitesN > len(azSites) {
		*sitesN = len(azSites)
	}

	c := &client{base: trimSlash(*server), admin: *admin, http: &http.Client{Timeout: 20 * time.Second}}
	if err := c.waitReady(2 * time.Minute); err != nil {
		slog.Error("control plane not ready", "error", err)
		os.Exit(1)
	}

	st, err := loadState(*statePath)
	if err != nil || len(st.Sites) == 0 {
		st, err = c.seed(azSites[:*sitesN], *enroll)
		if err != nil {
			slog.Error("seed failed", "error", err)
			os.Exit(1)
		}
		if err := saveState(*statePath, st); err != nil {
			slog.Warn("could not persist sim state", "error", err)
		}
		slog.Info("A-Z customer demo fleet seeded", "sites", len(st.Sites))
	} else {
		slog.Info("resuming A-Z simulated fleet", "sites", len(st.Sites), "state", *statePath)
		_ = c.logActivity([]map[string]any{{
			"level": "chapter", "source": "sim", "chapter": "*", "action": "resume",
			"message": fmt.Sprintf("resuming A–Z simulation (%d sites)", len(st.Sites)),
		}})
	}

	if *once {
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ticker := time.NewTicker(*interval)
	defer ticker.Stop()
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))

	c.tick(st, rng)
	for {
		select {
		case <-ctx.Done():
			slog.Info("simulator stopped")
			return
		case <-ticker.C:
			c.tick(st, rng)
			_ = saveState(*statePath, st)
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
	_ = c.logActivity([]map[string]any{{
		"level": "chapter", "source": "sim", "chapter": "*", "action": "seed.start",
		"message": fmt.Sprintf("A–Z simulation seed starting (%d sites)", len(specs)),
		"detail":  map[string]any{"letters": lettersOf(specs)},
	}})
	out := &stateFile{}
	for _, spec := range specs {
		_ = c.logActivity([]map[string]any{{
			"level": "chapter", "source": "sim", "chapter": spec.Letter, "action": "seed.site",
			"site": spec.Name, "message": fmt.Sprintf("[%s] %s — %s", spec.Letter, spec.Name, spec.Story),
			"detail": map[string]any{"region": spec.Region, "protocol": spec.Protocol, "topic": spec.Topic},
		}})
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
		site.Letter = spec.Letter
		site.Story = spec.Story
		site.Protocol = spec.Protocol
		desired := map[string]any{"mode": "auto", "demo": true, "letter": spec.Letter, "setpoint": 40 + int(spec.Letter[0]-'A')}
		if err := c.putTwin(site.DeviceID, desired); err != nil {
			slog.Warn("twin desired", "site", spec.Name, "error", err)
		}
		_, _ = c.do("POST", "/api/v1/agent/twins/"+site.DeviceID+"/reported", site.AgentToken, map[string]any{
			"site_id": site.SiteID, "reported": map[string]any{"mode": "auto", "letter": spec.Letter, "healthy": true},
		})
		out.Sites = append(out.Sites, *site)
		_ = c.logActivity([]map[string]any{{
			"level": "ok", "source": "sim", "chapter": spec.Letter, "site_id": site.SiteID, "site": spec.Name,
			"action": "seed.ready", "message": fmt.Sprintf("[%s] online site=%s device=%s topic=%s", spec.Letter, site.SiteID, devID, spec.Topic),
		}})
		slog.Info("site online", "letter", spec.Letter, "name", spec.Name, "site_id", site.SiteID)
	}

	if err := c.ensureRoute("A-Z MES webhook", "factory/+/telemetry", "http://127.0.0.1:9/mes"); err != nil {
		slog.Warn("mes route", "error", err)
	}
	if err := c.ensureRoute("A-Z Cloud fan-out", "+/+/+", "http://127.0.0.1:9/cloud"); err != nil {
		slog.Warn("cloud route", "error", err)
	}
	if err := c.ensureRoute("Retail POS", "retail/+/pos", "http://127.0.0.1:9/pos"); err != nil {
		slog.Warn("retail route", "error", err)
	}
	_ = c.logActivity([]map[string]any{{
		"level": "ok", "source": "sim", "chapter": "*", "action": "seed.routes",
		"message": "stream routes ready (MES + cloud fan-out + retail)",
	}})

	if len(out.Sites) > 0 {
		for _, letter := range []string{"A", "V", "W"} {
			site := findLetter(out.Sites, letter)
			if site == nil {
				site = &out.Sites[0]
			}
			name := "demo-app-" + strings.ToLower(site.Letter)
			if err := c.ensureDeployment(site.SiteID, name, "example/"+name+":1.0"); err != nil {
				slog.Warn("deployment", "name", name, "error", err)
			} else {
				_ = c.logActivity([]map[string]any{{
					"level": "ok", "source": "sim", "chapter": site.Letter, "site_id": site.SiteID, "site": site.Name,
					"action": "seed.app", "message": fmt.Sprintf("[%s] edge app desired running: %s", site.Letter, name),
				}})
			}
		}
	}

	_ = c.logActivity([]map[string]any{{
		"level": "chapter", "source": "sim", "chapter": "*", "action": "seed.done",
		"message": fmt.Sprintf("A–Z seed complete — %d sites ready for live simulation", len(out.Sites)),
	}})
	return out, nil
}

func (c *client) tick(st *stateFile, rng *rand.Rand) {
	if len(st.Sites) == 0 {
		return
	}
	// Rotate A–Z chapter focus each tick.
	ch := st.Sites[st.Chapter%len(st.Sites)]
	st.Chapter++

	_ = c.logActivity([]map[string]any{{
		"level": "chapter", "source": "sim", "chapter": ch.Letter, "site_id": ch.SiteID, "site": ch.Name,
		"action":  "chapter.focus",
		"message": fmt.Sprintf("── Chapter %s ── %s · %s", ch.Letter, ch.Name, ch.Story),
		"detail":  map[string]any{"topic": ch.Topic, "protocol": ch.Protocol},
	}})

	var batch []map[string]any
	for _, site := range st.Sites {
		q := rng.Intn(12)
		_, err := c.do("POST", "/api/v1/heartbeat", site.AgentToken, map[string]any{
			"site_id": site.SiteID,
			"version": "0.2.0-sim-az",
			"metrics": map[string]any{
				"queue_depth": q, "queue_bytes": q * 2048, "sim": true, "letter": site.Letter,
			},
		})
		if err != nil {
			batch = append(batch, map[string]any{
				"level": "warn", "source": "sim", "chapter": site.Letter, "site_id": site.SiteID, "site": site.Name,
				"action": "heartbeat.fail", "message": fmt.Sprintf("[%s] heartbeat failed: %v", site.Letter, err),
			})
			continue
		}
		val := round1(20 + rng.Float64()*80)
		payload := map[string]any{
			"letter": site.Letter, "value": val, "ok": true, "source": "nodra-sim",
			"story": site.Story, "ts": time.Now().UTC().Format(time.RFC3339Nano),
		}
		_, err = c.do("POST", "/api/v1/events", site.AgentToken, map[string]any{
			"site_id": site.SiteID, "topic": site.Topic, "payload": payload,
		})
		if err != nil {
			batch = append(batch, map[string]any{
				"level": "warn", "source": "sim", "chapter": site.Letter, "site_id": site.SiteID, "site": site.Name,
				"action": "event.fail", "message": fmt.Sprintf("[%s] event failed: %v", site.Letter, err),
			})
			continue
		}
		if site.Letter == ch.Letter {
			batch = append(batch, map[string]any{
				"level": "info", "source": "sim", "chapter": site.Letter, "site_id": site.SiteID, "site": site.Name,
				"action":  "telemetry",
				"message": fmt.Sprintf("[%s] published %s value=%.1f queue_depth=%d protocol=%s", site.Letter, site.Topic, val, q, site.Protocol),
				"detail":  payload,
			})
			_, _ = c.do("POST", "/api/v1/agent/twins/"+site.DeviceID+"/reported", site.AgentToken, map[string]any{
				"site_id": site.SiteID,
				"reported": map[string]any{
					"mode": "auto", "letter": site.Letter, "value": val, "healthy": true,
					"last_story": site.Story,
				},
			})
			batch = append(batch, map[string]any{
				"level": "ok", "source": "sim", "chapter": site.Letter, "site_id": site.SiteID, "site": site.Name,
				"action":  "twin.reported",
				"message": fmt.Sprintf("[%s] twin reported synced value=%.1f", site.Letter, val),
			})
		}
	}
	_ = c.logActivity(batch)
}

func findLetter(sites []enrolledSite, letter string) *enrolledSite {
	for i := range sites {
		if sites[i].Letter == letter {
			return &sites[i]
		}
	}
	return nil
}

func lettersOf(specs []siteSpec) string {
	parts := make([]string, len(specs))
	for i, s := range specs {
		parts[i] = s.Letter
	}
	return strings.Join(parts, "")
}

func (c *client) logActivity(entries []map[string]any) error {
	if len(entries) == 0 {
		return nil
	}
	_, err := c.do("POST", "/api/v1/activity", c.admin, map[string]any{"entries": entries})
	return err
}

func (c *client) enroll(spec siteSpec, enrollTok string) (*enrolledSite, error) {
	body, err := c.do("POST", "/api/v1/enroll", "", map[string]any{
		"name":             spec.Name,
		"enrollment_token": enrollTok,
		"metadata": map[string]string{
			"region": spec.Region, "demo": "true", "sim": "true", "letter": spec.Letter, "story": spec.Story,
		},
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
		"tags": map[string]string{"letter": spec.Letter, "story": spec.Story},
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
		return body, fmt.Errorf("%s %s -> %d %s", method, path, resp.StatusCode, truncate(string(body), 180))
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

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
