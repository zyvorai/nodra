// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"encoding/json"
	"time"

	"github.com/zyvorai/nodra/pkg/ota"
)

type Site struct {
	ID                   string            `json:"id"`
	Name                 string            `json:"name"`
	TokenHash            string            `json:"token_hash,omitempty"`
	Status               string            `json:"status"`
	Version              string            `json:"version,omitempty"`
	LastSeen             time.Time         `json:"last_seen,omitempty"`
	CreatedAt            time.Time         `json:"created_at"`
	Metadata             map[string]string `json:"metadata,omitempty"`
	Metrics              map[string]any    `json:"metrics,omitempty"`
	CertificateSerial    string            `json:"certificate_serial,omitempty"`
	CertificateExpiresAt time.Time         `json:"certificate_expires_at,omitempty"`
	Revoked              bool              `json:"revoked,omitempty"`
	RevokedAt            time.Time         `json:"revoked_at,omitempty"`
	// OrgID is empty for a site enrolled against the global enrollment
	// token (or before orgs existed) — such sites are visible only to
	// global admin/viewer tokens, never to an org-scoped token. See
	// docs/ARCHITECTURE.md's "Multi-tenant orgs" section for exactly which
	// endpoints are org-filtered in v1 (sites list + single-site
	// revoke/rotate only — everything else is fleet-wide regardless of
	// which token, global or org-scoped, is used).
	OrgID string `json:"org_id,omitempty"`
}

// Org is a v1, deliberately partial tenant boundary: an org has its own
// enrollment token (so its sites are distinguishable from the global fleet
// and from other orgs) and its own admin/viewer bearer tokens (scoped to
// "admin"/"viewer" role semantics identical to the global tokens). Only
// EnrollmentTokenHash/AdminTokenHash/ViewerTokenHash are persisted — the
// plaintext tokens are returned exactly once, at creation, like a site's
// agent token.
type Org struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	// *TokenHash fields are marshaled for durable storage (like
	// Site.TokenHash) but must never reach an HTTP response — callers
	// redact them (set to "") on a copy before writeJSON, the same
	// convention server.enroll() uses for Site.TokenHash.
	EnrollmentTokenHash string `json:"enrollment_token_hash,omitempty"`
	AdminTokenHash      string `json:"admin_token_hash,omitempty"`
	ViewerTokenHash     string `json:"viewer_token_hash,omitempty"`
}

type Device struct {
	ID        string            `json:"id"`
	SiteID    string            `json:"site_id"`
	Name      string            `json:"name"`
	Protocol  string            `json:"protocol"`
	Status    string            `json:"status"`
	LastSeen  time.Time         `json:"last_seen,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
	Tags      map[string]string `json:"tags,omitempty"`
}

type Twin struct {
	DeviceID        string         `json:"device_id"`
	SiteID          string         `json:"site_id"`
	Desired         map[string]any `json:"desired,omitempty"`
	Reported        map[string]any `json:"reported,omitempty"`
	DesiredVersion  uint64         `json:"desired_version"`
	ReportedVersion uint64         `json:"reported_version"`
	UpdatedAt       time.Time      `json:"updated_at"`
}

type Route struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	SiteID      string            `json:"site_id,omitempty"`
	Topic       string            `json:"topic"`
	TargetURL   string            `json:"target_url"`
	Method      string            `json:"method"`
	Enabled     bool              `json:"enabled"`
	RetryMax    int               `json:"retry_max"`
	TimeoutSecs int               `json:"timeout_seconds"`
	Headers     map[string]string `json:"headers,omitempty"`
	CreatedAt   time.Time         `json:"created_at"`
}

type Event struct {
	ID         string            `json:"id"`
	SiteID     string            `json:"site_id"`
	Topic      string            `json:"topic"`
	Payload    json.RawMessage   `json:"payload"`
	Headers    map[string]string `json:"headers,omitempty"`
	EventTime  time.Time         `json:"event_time"`
	IngestedAt time.Time         `json:"ingested_at,omitempty"`
	CreatedAt  time.Time         `json:"created_at,omitempty"` // compatibility alias for v0.1 clients
}

type Delivery struct {
	ID          string            `json:"id"`
	RouteID     string            `json:"route_id"`
	EventID     string            `json:"event_id"`
	SiteID      string            `json:"site_id"`
	Topic       string            `json:"topic"`
	TargetURL   string            `json:"target_url"`
	Method      string            `json:"method"`
	Payload     json.RawMessage   `json:"payload"`
	Headers     map[string]string `json:"headers,omitempty"`
	TimeoutSecs int               `json:"timeout_seconds"`
	Attempts    int               `json:"attempts"`
	MaxAttempts int               `json:"max_attempts"`
	NextAttempt time.Time         `json:"next_attempt"`
	EventTime   time.Time         `json:"event_time,omitempty"`
	CreatedAt   time.Time         `json:"created_at"`
	LastError   string            `json:"last_error,omitempty"`
}

type DeadLetter struct {
	Delivery Delivery  `json:"delivery"`
	FailedAt time.Time `json:"failed_at"`
	Reason   string    `json:"reason"`
}

type Deployment struct {
	ID           string            `json:"id"`
	SiteID       string            `json:"site_id"`
	Name         string            `json:"name"`
	Version      string            `json:"version"`
	Image        string            `json:"image"`
	Status       string            `json:"status"`
	DesiredState string            `json:"desired_state,omitempty"`
	ActualState  string            `json:"actual_state,omitempty"`
	Env          map[string]string `json:"env,omitempty"`
	Ports        []string          `json:"ports,omitempty"`
	Volumes      []string          `json:"volumes,omitempty"`
	Command      []string          `json:"command,omitempty"`
	CreatedAt    time.Time         `json:"created_at"`
	UpdatedAt    time.Time         `json:"updated_at"`
	// LastGoodImage/LastGoodVersion snapshot the previously-running
	// image/version whenever a healthy deployment is patched to a new one,
	// so a health-gated rollback has a known-good target. Cleared once
	// consumed by a rollback so the same stale target can't be reused twice.
	LastGoodImage   string `json:"last_good_image,omitempty"`
	LastGoodVersion string `json:"last_good_version,omitempty"`
	// SignatureMode overrides the agent's default cosign verification mode
	// for this deployment: ""(=agent default)|"enforce"|"warn"|"skip".
	SignatureMode string    `json:"signature_mode,omitempty"`
	DeployedAt    time.Time `json:"deployed_at,omitempty"`
}

type Alert struct {
	ID        string    `json:"id"`
	SiteID    string    `json:"site_id,omitempty"`
	Severity  string    `json:"severity"`
	Type      string    `json:"type"`
	Message   string    `json:"message"`
	Resolved  bool      `json:"resolved"`
	CreatedAt time.Time `json:"created_at"`
}

// PolicyPack is a named, versioned constraint on which deployment images are
// allowed. Global when SiteID is empty, else scoped to one site. v1 only
// constrains images (AllowedImages) — no RBAC-rule or alert-threshold packs.
type PolicyPack struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Version       int       `json:"version"`
	SiteID        string    `json:"site_id,omitempty"`
	Enabled       bool      `json:"enabled"`
	AllowedImages []string  `json:"allowed_images,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// OTA campaign status values.
const (
	OTACampaignDraft     = "draft"
	OTACampaignRunning   = "running"
	OTACampaignPaused    = "paused"
	OTACampaignCompleted = "completed"
	OTACampaignAborted   = "aborted"
)

// OTACampaignStage is one canary wave. CanaryPercent is cumulative: by the
// end of this stage, that percentage of TargetDeviceIDs should have received
// the campaign's Desired["ota"] write (ceil, at least one when the target
// set is non-empty). Stages must be strictly increasing and the last stage
// must be 100.
type OTACampaignStage struct {
	CanaryPercent int `json:"canary_percent"`
}

// OTACampaignDevice tracks one device selected into a campaign wave and the
// last observed pkg/ota lifecycle state from its twin (empty until reported).
type OTACampaignDevice struct {
	DeviceID string    `json:"device_id"`
	SiteID   string    `json:"site_id"`
	UpdateID string    `json:"update_id"`
	Stage    int       `json:"stage"`
	State    ota.State `json:"state,omitempty"`
}

// OTACampaign is a staged, multi-site OTA canary rollout. Delivery still
// goes through Twin.Desired["ota"] per device — the campaign only owns
// targeting, wave selection, promote/abort, and outcome aggregation.
type OTACampaign struct {
	ID                      string              `json:"id"`
	Name                    string              `json:"name"`
	SiteIDs                 []string            `json:"site_ids,omitempty"`
	DeviceIDs               []string            `json:"device_ids,omitempty"`
	Stages                  []OTACampaignStage  `json:"stages"`
	Manifest                ota.Manifest        `json:"manifest"`
	Policy                  ota.Policy          `json:"policy"`
	FailureThresholdPercent int                 `json:"failure_threshold_percent"`
	Status                  string              `json:"status"`
	CurrentStage            int                 `json:"current_stage"` // -1 while draft; else index into Stages
	TargetDeviceIDs         []string            `json:"target_device_ids,omitempty"`
	Devices                 []OTACampaignDevice `json:"devices,omitempty"`
	CreatedAt               time.Time           `json:"created_at"`
	UpdatedAt               time.Time           `json:"updated_at"`
}

type State struct {
	Sites        []Site        `json:"sites"`
	Devices      []Device      `json:"devices"`
	Twins        []Twin        `json:"twins,omitempty"`
	Routes       []Route       `json:"routes"`
	Deployments  []Deployment  `json:"deployments"`
	Alerts       []Alert       `json:"alerts"`
	Events       []Event       `json:"events"`
	SeenEvents   []string      `json:"seen_event_ids,omitempty"`
	PolicyPacks  []PolicyPack  `json:"policy_packs,omitempty"`
	Orgs         []Org         `json:"orgs,omitempty"`
	OTACampaigns []OTACampaign `json:"ota_campaigns,omitempty"`
}
