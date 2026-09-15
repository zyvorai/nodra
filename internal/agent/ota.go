// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/zyvorai/nodra/pkg/ota"
)

// localOTAStatus is the local (device/OTA-agent -> nodrad) counterpart to
// localTwinReported, specifically for OTA: it validates the reported
// ota.Status at the transport level before forwarding to the control
// plane's typed POST /api/v1/agent/devices/{id}/ota/status, which layers
// pkg/ota's lifecycle state-machine check on top. nodrad itself never
// interprets the status beyond validating and forwarding it — staging,
// activation, health verification, and rollback stay the OTA agent's job,
// per pkg/ota's package doc.
func (a *Agent) localOTAStatus(w http.ResponseWriter, r *http.Request) {
	if !a.localAuth(r) {
		a.unauthorizedLocal(w, r, "local.ota_status")
		return
	}
	var in struct {
		Status ota.Status `json:"status"`
	}
	if !a.decode(w, r, &in) {
		return
	}
	idv := r.PathValue("id")
	in.Status.DeviceID = idv
	if err := in.Status.Validate(); err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	b, _ := json.Marshal(map[string]any{"site_id": a.cfg.SiteID, "status": in.Status})
	resp, err := a.do(r.Context(), "POST", "/api/v1/agent/devices/"+idv+"/ota/status", b)
	if err != nil {
		writeJSON(w, 502, map[string]string{"error": err.Error()})
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(body)
}
