// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/zyvorai/nodra/internal/durable"
	"github.com/zyvorai/nodra/internal/transform"
)

type LocalRoute struct {
	Name      string               `json:"name"`
	Topic     string               `json:"topic"`
	TargetURL string               `json:"target_url"`
	Method    string               `json:"method,omitempty"`
	Timeout   string               `json:"timeout,omitempty"`
	Headers   map[string]string    `json:"headers,omitempty"`
	Filter    *transform.Filter    `json:"filter,omitempty"`
	Transform *transform.Transform `json:"transform,omitempty"`
}

// ConnectorSpec configures an optional protocol adapter (e.g. modbus poller).
type ConnectorSpec struct {
	Type   string          `json:"type"`
	Name   string          `json:"name,omitempty"`
	Config json.RawMessage `json:"config"`
}

type Config struct {
	ServerURL            string            `json:"server_url"`
	SiteName             string            `json:"site_name"`
	EnrollmentToken      string            `json:"enrollment_token,omitempty"`
	SiteID               string            `json:"site_id,omitempty"`
	AgentToken           string            `json:"agent_token,omitempty"`
	DataDir              string            `json:"data_dir"`
	Listen               string            `json:"listen"`
	MQTTListen           string            `json:"mqtt_listen,omitempty"`
	LocalToken           string            `json:"local_token,omitempty"`
	Heartbeat            time.Duration     `json:"-"`
	HeartbeatText        string            `json:"heartbeat"`
	Flush                time.Duration     `json:"-"`
	FlushText            string            `json:"flush_interval"`
	Metadata             map[string]string `json:"metadata,omitempty"`
	Runner               string            `json:"runner"`
	MaxSpoolBytes        int64             `json:"max_spool_bytes"`
	MaxSpoolEvents       int               `json:"max_spool_events"`
	SpoolPolicy          string            `json:"spool_policy"`
	LocalRoutes          []LocalRoute      `json:"local_routes,omitempty"`
	Connectors           []ConnectorSpec   `json:"connectors,omitempty"`
	RequestCertificate   bool              `json:"request_certificate,omitempty"`
	ClientCertFile       string            `json:"client_cert_file,omitempty"`
	ClientKeyFile        string            `json:"client_key_file,omitempty"`
	CAFile               string            `json:"ca_file,omitempty"`
	InsecureSkipVerify   bool              `json:"insecure_skip_verify,omitempty"`
	AuditRetentionDays   int               `json:"audit_retention_days,omitempty"`
	CertRotateBefore     time.Duration     `json:"-"`
	CertRotateBeforeText string            `json:"cert_rotate_before,omitempty"`
	// SignatureMode is the default cosign verification mode applied before
	// docker pull: "enforce" refuses an unverifiable image, "warn" logs and
	// continues (the default, so existing unsigned deployments keep
	// working), "skip" disables verification entirely.
	SignatureMode            string        `json:"signature_mode,omitempty"`
	CosignCertIdentityRegexp string        `json:"cosign_certificate_identity_regexp,omitempty"`
	CosignCertOIDCIssuer     string        `json:"cosign_certificate_oidc_issuer,omitempty"`
	DeployHealthGrace        time.Duration `json:"-"`
	DeployHealthGraceText    string        `json:"deploy_health_grace,omitempty"`
	// MQTTSessionMaxItems/MQTTSessionMaxBytes bound the durable per-ClientID
	// message queue used for persistent MQTT sessions (CleanSession=0,
	// QoS0/1 only).
	MQTTSessionMaxItems int   `json:"mqtt_session_max_items,omitempty"`
	MQTTSessionMaxBytes int64 `json:"mqtt_session_max_bytes,omitempty"`
}

func DefaultConfig() Config {
	return Config{ServerURL: "http://127.0.0.1:8080", SiteName: "edge-site", DataDir: "./nodra-agent-data", Listen: "127.0.0.1:9091", MQTTListen: "127.0.0.1:1883", HeartbeatText: "30s", FlushText: "2s", Runner: "none", MaxSpoolBytes: 2 << 30, MaxSpoolEvents: 1000000, SpoolPolicy: "reject", AuditRetentionDays: 30, CertRotateBeforeText: "720h", SignatureMode: "warn", DeployHealthGraceText: "60s", MQTTSessionMaxItems: 20000, MQTTSessionMaxBytes: 64 << 20}
}
func LoadConfig(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	cfg := DefaultConfig()
	if err = json.Unmarshal(b, &cfg); err != nil {
		return Config{}, err
	}
	if err = cfg.normalize(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}
func (c *Config) normalize() error {
	if c.ServerURL == "" || c.SiteName == "" || c.DataDir == "" {
		return errors.New("server_url, site_name and data_dir are required")
	}
	var err error
	c.Heartbeat, err = time.ParseDuration(c.HeartbeatText)
	if err != nil {
		return errors.New("invalid heartbeat duration")
	}
	c.Flush, err = time.ParseDuration(c.FlushText)
	if err != nil {
		return errors.New("invalid flush_interval duration")
	}
	if c.Listen == "" {
		c.Listen = "127.0.0.1:9091"
	}
	if c.Runner == "" {
		c.Runner = "none"
	}
	if c.MaxSpoolBytes == 0 {
		c.MaxSpoolBytes = 2 << 30
	}
	if c.MaxSpoolEvents == 0 {
		c.MaxSpoolEvents = 1000000
	}
	if c.SpoolPolicy == "" {
		c.SpoolPolicy = "reject"
	}
	if c.AuditRetentionDays <= 0 {
		c.AuditRetentionDays = 30
	}
	if c.MQTTSessionMaxItems <= 0 {
		c.MQTTSessionMaxItems = 20000
	}
	if c.MQTTSessionMaxBytes <= 0 {
		c.MQTTSessionMaxBytes = 64 << 20
	}
	if c.CertRotateBeforeText == "" {
		c.CertRotateBeforeText = "720h"
	}
	c.CertRotateBefore, err = time.ParseDuration(c.CertRotateBeforeText)
	if err != nil {
		return errors.New("invalid cert_rotate_before duration")
	}
	if c.SignatureMode == "" {
		c.SignatureMode = "warn"
	}
	switch c.SignatureMode {
	case "enforce", "warn", "skip":
	default:
		return errors.New("signature_mode must be enforce, warn or skip")
	}
	if c.DeployHealthGraceText == "" {
		c.DeployHealthGraceText = "60s"
	}
	c.DeployHealthGrace, err = time.ParseDuration(c.DeployHealthGraceText)
	if err != nil {
		return errors.New("invalid deploy_health_grace duration")
	}
	switch c.SpoolPolicy {
	case "reject", "drop-oldest", "drop-newest":
	default:
		return errors.New("spool_policy must be reject, drop-oldest or drop-newest")
	}
	return nil
}
func SaveConfig(path string, c Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return durable.AtomicWrite(path, b, 0o600)
}
