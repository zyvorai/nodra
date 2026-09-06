// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"encoding/json"
	"time"
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

type State struct {
	Sites       []Site       `json:"sites"`
	Devices     []Device     `json:"devices"`
	Twins       []Twin       `json:"twins,omitempty"`
	Routes      []Route      `json:"routes"`
	Deployments []Deployment `json:"deployments"`
	Alerts      []Alert      `json:"alerts"`
	Events      []Event      `json:"events"`
	SeenEvents  []string     `json:"seen_event_ids,omitempty"`
}
