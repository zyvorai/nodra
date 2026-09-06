// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

// Package relaybridge maps Nodra cloud-route webhooks to Zyvor Relay Accept events.
package relaybridge

import (
	"encoding/json"
	"strings"
	"time"
)

// NodraDelivery is the JSON body Nodra POSTs for a cloud route delivery.
type NodraDelivery struct {
	EventID    string            `json:"event_id"`
	SiteID     string            `json:"site_id"`
	Topic      string            `json:"topic"`
	Payload    json.RawMessage   `json:"payload"`
	Headers    map[string]string `json:"headers"`
	DeliveryID string            `json:"delivery_id"`
	EventTime  time.Time         `json:"event_time"`
}

// RelayEvent is the Accept payload for POST /v1/events.
type RelayEvent struct {
	Type           string         `json:"type"`
	Severity       string         `json:"severity"`
	Source         string         `json:"source"`
	IdempotencyKey string         `json:"idempotency_key"`
	Data           map[string]any `json:"data"`
}

// MapDelivery converts a Nodra delivery webhook into a Relay Accept event.
func MapDelivery(d NodraDelivery) RelayEvent {
	if d.Headers == nil {
		d.Headers = map[string]string{}
	}
	idem := strings.TrimSpace(d.DeliveryID)
	if idem == "" {
		idem = strings.TrimSpace(d.EventID)
	}
	eventType := strings.TrimSpace(d.Headers["x-nodra-relay-type"])
	if eventType == "" {
		eventType = topicToType(d.Topic)
	}
	if eventType == "" {
		eventType = "nodra.event"
	}
	severity := strings.ToLower(strings.TrimSpace(d.Headers["x-nodra-relay-severity"]))
	if severity == "" {
		severity = severityFromPayload(d.Payload)
	}
	if severity == "" {
		severity = "medium"
	}
	data := map[string]any{
		"site_id":     d.SiteID,
		"topic":       d.Topic,
		"event_id":    d.EventID,
		"delivery_id": d.DeliveryID,
		"headers":     d.Headers,
	}
	if !d.EventTime.IsZero() {
		data["event_time"] = d.EventTime.UTC().Format(time.RFC3339Nano)
	}
	if len(d.Payload) > 0 && json.Valid(d.Payload) {
		var payload any
		if err := json.Unmarshal(d.Payload, &payload); err == nil {
			data["payload"] = payload
			if m, ok := payload.(map[string]any); ok {
				for k, v := range m {
					if _, exists := data[k]; !exists {
						data[k] = v
					}
				}
			}
		} else {
			data["payload"] = string(d.Payload)
		}
	}
	return RelayEvent{
		Type:           eventType,
		Severity:       normalizeSeverity(severity),
		Source:         "nodra",
		IdempotencyKey: idem,
		Data:           data,
	}
}

func topicToType(topic string) string {
	t := strings.Trim(strings.ReplaceAll(topic, "/", "."), ".")
	for strings.Contains(t, "..") {
		t = strings.ReplaceAll(t, "..", ".")
	}
	return t
}

func severityFromPayload(raw json.RawMessage) string {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return ""
	}
	for _, k := range []string{"severity", "level", "priority"} {
		if v, ok := m[k]; ok {
			if s, ok := v.(string); ok {
				return s
			}
		}
	}
	return ""
}

func normalizeSeverity(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "info", "low":
		return "info"
	case "medium", "warn", "warning":
		return "medium"
	case "high", "error":
		return "high"
	case "critical", "fatal", "emergency":
		return "critical"
	default:
		return "medium"
	}
}
