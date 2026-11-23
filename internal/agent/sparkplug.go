// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-2.0

package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/zyvorai/nodra/internal/sparkplug"
)

// handleMQTT is the MQTT ingress handler. Sparkplug B traffic (spBv1.0/...)
// is decoded when sparkplug_enabled is set; everything else keeps the
// original behaviour (JSON passthrough, non-JSON wrapped as a string).
func (a *Agent) handleMQTT(ctx context.Context, topic string, payload []byte) error {
	if a.sparkplug != nil && sparkplug.IsSparkplug(topic) {
		return a.ingestSparkplug(ctx, topic, payload)
	}
	p := payload
	if !json.Valid(p) {
		p, _ = json.Marshal(string(p))
	}
	_, err := a.ingest(ctx, topic, p, map[string]string{"x-nodra-ingress": "mqtt"})
	return err
}

func (a *Agent) ingestSparkplug(ctx context.Context, topic string, payload []byte) error {
	tp, err := sparkplug.ParseTopic(topic)
	if err != nil {
		a.sparkplugInvalid.Add(1)
		return err
	}
	headers := map[string]string{"x-nodra-ingress": "mqtt-sparkplug", "x-nodra-sparkplug-type": tp.Type}
	if tp.Type == sparkplug.STATE {
		if on, ok := sparkplug.ParseHostState(payload); ok {
			a.sparkplug.SetHost(tp.Host, on)
		}
		p := payload
		if !json.Valid(p) {
			p, _ = json.Marshal(string(p))
		}
		_, err = a.ingest(ctx, topic, p, headers)
		return err
	}
	pl, err := sparkplug.DecodePayload(payload)
	if err != nil {
		a.sparkplugInvalid.Add(1)
		return err
	}
	obs := a.sparkplug.Observe(tp, pl, time.Now().UTC())
	if len(obs.Anomalies) > 0 {
		a.sparkplugAnomalies.Add(uint64(len(obs.Anomalies)))
	}
	doc, err := obs.JSON()
	if err != nil {
		return err
	}
	var evTime time.Time
	if pl.Timestamp > 0 && pl.Timestamp < 1<<53 {
		// Sparkplug timestamps are UTC milliseconds since the epoch. Only
		// trust values that are not in the future, so a bad clock cannot
		// reorder the spool.
		if t := time.UnixMilli(int64(pl.Timestamp)).UTC(); t.Year() >= 2000 && !t.After(time.Now().Add(time.Minute)) {
			evTime = t
		}
	}
	if _, err = a.ingestWithTime(ctx, topic, doc, headers, evTime); err != nil {
		return err
	}
	if obs.Changed && obs.State != nil {
		st, _ := json.Marshal(obs.State)
		stTopic := "nodra/sparkplug/state/" + obs.State.Group + "/" + obs.State.Node
		if obs.State.Device != "" {
			stTopic += "/" + obs.State.Device
		}
		_, err = a.ingest(ctx, stTopic, st, map[string]string{"x-nodra-ingress": "sparkplug-state"})
	}
	return err
}

func (a *Agent) localSparkplug(w http.ResponseWriter, r *http.Request) {
	if !a.localAuth(r) {
		a.unauthorizedLocal(w, r, "local.sparkplug_list")
		return
	}
	if a.sparkplug == nil {
		writeJSON(w, 404, map[string]string{"error": "sparkplug is not enabled (set sparkplug_enabled)"})
		return
	}
	writeJSON(w, 200, map[string]any{
		"entities":      a.sparkplug.Snapshot(),
		"hosts":         a.sparkplug.Hosts(),
		"invalid_total": a.sparkplugInvalid.Load(),
		"anomaly_total": a.sparkplugAnomalies.Load(),
	})
}
