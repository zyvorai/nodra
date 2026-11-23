// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-2.0

package rules

import (
	"encoding/json"
	"fmt"

	"github.com/zyvorai/nodra/internal/transform"
)

// AlertTopicPrefix is where alert actions publish.
const AlertTopicPrefix = "nodra/rules/alert/"

// Event is a rendered action: what would be ingested.
type Event struct {
	Topic   string            `json:"topic"`
	Payload json.RawMessage   `json:"payload"`
	Headers map[string]string `json:"headers,omitempty"`
}

// Render turns one action of an Outcome into the event to ingest. emit
// actions are shaped by the existing transform rules (set_fields, drop_fields,
// wrap_as, set_headers) applied to the base document.
func Render(o Outcome, a Action) (Event, error) {
	base := map[string]any{"rule": o.Rule, "key": o.Key, "kind": o.Kind, "value": o.Value, "at": o.At.UTC().Format("2006-01-02T15:04:05.000Z07:00")}
	ev := Event{Headers: map[string]string{"x-nodra-ingress": "rule", "x-nodra-rule": o.Rule}}
	switch a.Type {
	case "alert":
		sev := a.Severity
		if sev == "" {
			sev = "warning"
		}
		base["severity"] = sev
		if a.Message != "" {
			base["message"] = a.Message
		}
		ev.Topic = AlertTopicPrefix + o.Rule
	case "emit":
		ev.Topic = a.Topic
	default:
		return Event{}, fmt.Errorf("unsupported action %q", a.Type)
	}
	b, err := json.Marshal(base)
	if err != nil {
		return Event{}, err
	}
	if a.Transform != nil {
		res, err := transform.Apply(ev.Topic, b, ev.Headers, nil, a.Transform)
		if err != nil {
			return Event{}, err
		}
		b, ev.Headers = res.Payload, res.Headers
		ev.Headers["x-nodra-ingress"], ev.Headers["x-nodra-rule"] = "rule", o.Rule
	}
	ev.Payload = b
	return ev, nil
}
