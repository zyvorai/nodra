// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/zyvorai/nodra/internal/model"
	"github.com/zyvorai/nodra/pkg/ota"
)

// otaTwinKey is the reserved key an OTA request/status lives under inside a
// device Twin's Desired/Reported maps — see pkg/ota's package doc: Nodra
// owns transport, durable delivery, desired/reported state and status
// propagation for an external OTA agent, and Twin is already exactly that
// generic desired/reported mechanism for a device. Wiring OTA through it
// (rather than a new parallel entity) reuses the existing durable storage,
// polling delivery to nodrad, and admin/agent auth this control plane
// already has for every other twin.
const otaTwinKey = "ota"

// otaRequestFromDesired extracts and validates the ota.Request currently
// stored in a twin's Desired map, if any.
func otaRequestFromDesired(desired map[string]any) (ota.Request, bool, error) {
	raw, ok := desired[otaTwinKey]
	if !ok {
		return ota.Request{}, false, nil
	}
	var req ota.Request
	b, err := json.Marshal(raw)
	if err != nil {
		return ota.Request{}, true, err
	}
	if err := json.Unmarshal(b, &req); err != nil {
		return ota.Request{}, true, err
	}
	return req, true, nil
}

// otaStatusFromReported extracts and validates the ota.Status currently
// stored in a twin's Reported map, if any.
func otaStatusFromReported(reported map[string]any) (ota.Status, bool, error) {
	raw, ok := reported[otaTwinKey]
	if !ok {
		return ota.Status{}, false, nil
	}
	var st ota.Status
	b, err := json.Marshal(raw)
	if err != nil {
		return ota.Status{}, true, err
	}
	if err := json.Unmarshal(b, &st); err != nil {
		return ota.Status{}, true, err
	}
	return st, true, nil
}

// setDeviceOTADesired writes req into the device's Twin.Desired["ota"] and
// bumps DesiredVersion — the same durable path otaDeviceRequest and OTA
// campaigns both use. Caller must have already authorized the device.
// Validation failures are returned as-is (callers map them to HTTP 400);
// store errors are returned as-is (callers map them to HTTP 507).
func (s *Server) setDeviceOTADesired(dev model.Device, req ota.Request) (model.Twin, error) {
	req.DeviceID = dev.ID
	if req.SiteID == "" {
		req.SiteID = dev.SiteID
	}
	if err := req.Validate(); err != nil {
		return model.Twin{}, err
	}
	var out model.Twin
	err := s.store.UpdateTwin(dev.ID, func(tw *model.Twin) {
		tw.DeviceID = dev.ID
		tw.SiteID = dev.SiteID
		if tw.Desired == nil {
			tw.Desired = map[string]any{}
		}
		tw.Desired[otaTwinKey] = req
		tw.DesiredVersion++
		tw.UpdatedAt = time.Now().UTC()
		out = *tw
	})
	if err != nil {
		return model.Twin{}, err
	}
	return out, nil
}

// otaDeviceRequest sets a device's desired OTA state (admin-only): the
// control plane validates the request at the transport level (pkg/ota.
// Request.Validate — required fields, manifest shape, sha256/signature
// presence) and durably stores it as the device's Twin.Desired["ota"],
// bumping DesiredVersion so it's delivered the same way every other twin
// desired-state change already is — nodrad's existing twin-polling loop,
// not a new delivery path.
func (s *Server) otaDeviceRequest(w http.ResponseWriter, r *http.Request) {
	dev, ok := s.store.Device(r.PathValue("id"))
	if !ok || !s.callerCanMutateSite(r, dev.SiteID) {
		errorJSON(w, 404, "device not found")
		return
	}
	var req ota.Request
	if !s.decode(w, r, &req) {
		return
	}
	tw, err := s.setDeviceOTADesired(dev, req)
	if err != nil {
		if strings.HasPrefix(err.Error(), "ota:") {
			errorJSON(w, 400, err.Error())
			return
		}
		errorJSON(w, 507, err.Error())
		return
	}
	s.note("ok", "control-plane", s.actorForBearer(bearer(r)), "", dev.SiteID, dev.ID, "ota.request", "OTA update requested: "+req.Manifest.ArtifactID+" "+req.Manifest.Version, map[string]any{"device_id": dev.ID, "update_id": req.UpdateID, "artifact_id": req.Manifest.ArtifactID, "version": req.Manifest.Version})
	writeJSON(w, 200, tw)
}

// otaDeviceGet is an admin convenience read of a device's current OTA
// desired/reported state, extracted from its Twin — everything here is
// already visible via GET /api/v1/twins, this just saves the caller from
// picking the "ota" key back out of two generic maps itself.
func (s *Server) otaDeviceGet(w http.ResponseWriter, r *http.Request) {
	dev, ok := s.store.Device(r.PathValue("id"))
	if !ok || !s.callerCanSeeSite(r, dev.SiteID) {
		errorJSON(w, 404, "device not found")
		return
	}
	tw, _ := s.store.Twin(dev.ID)
	req, hasReq, err := otaRequestFromDesired(tw.Desired)
	if err != nil {
		errorJSON(w, 500, "corrupt desired ota state: "+err.Error())
		return
	}
	status, hasStatus, err := otaStatusFromReported(tw.Reported)
	if err != nil {
		errorJSON(w, 500, "corrupt reported ota state: "+err.Error())
		return
	}
	out := map[string]any{"device_id": dev.ID}
	if hasReq {
		out["request"] = req
	}
	if hasStatus {
		out["status"] = status
	}
	writeJSON(w, 200, out)
}

// agentOTAStatus is the agent-authenticated counterpart to
// agentTwinReported, specifically for OTA: it validates the reported
// ota.Status at the transport level AND against pkg/ota's lifecycle state
// machine (rejecting an illegal jump, e.g. "pending" -> "committed"), then
// stores it as Twin.Reported["ota"] the same way agentTwinReported stores
// an arbitrary reported map — durable, versioned, visible via the existing
// GET /api/v1/twins. A terminal "failed" or "rolled-back" status raises an
// alert, mirroring how a failed deployment status already does.
func (s *Server) agentOTAStatus(w http.ResponseWriter, r *http.Request) {
	var in struct {
		SiteID string     `json:"site_id"`
		Status ota.Status `json:"status"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	if _, ok := s.agentSite(r, in.SiteID); !ok {
		errorJSON(w, 401, "unauthorized agent")
		return
	}
	dev, ok := s.store.Device(r.PathValue("id"))
	if !ok || dev.SiteID != in.SiteID {
		errorJSON(w, 404, "device not found")
		return
	}
	in.Status.DeviceID = dev.ID
	if err := in.Status.Validate(); err != nil {
		errorJSON(w, 400, err.Error())
		return
	}
	var tw model.Twin
	var transitionErr error
	err := s.store.UpdateTwin(dev.ID, func(cur *model.Twin) {
		transitionErr = nil
		if prev, ok, err := otaStatusFromReported(cur.Reported); err == nil && ok {
			if err := ota.ValidateTransition(prev.State, in.Status.State); err != nil {
				transitionErr = err
				return
			}
		}
		if cur.Reported == nil {
			cur.Reported = map[string]any{}
		}
		cur.DeviceID = dev.ID
		cur.SiteID = in.SiteID
		cur.Reported[otaTwinKey] = in.Status
		cur.ReportedVersion++
		cur.UpdatedAt = time.Now().UTC()
		tw = *cur
	})
	if transitionErr != nil {
		errorJSON(w, 409, transitionErr.Error())
		return
	}
	if err != nil {
		errorJSON(w, 507, err.Error())
		return
	}
	if in.Status.State == ota.StateFailed || in.Status.State == ota.StateRolledBack {
		_ = s.store.AddAlert(model.Alert{ID: id("alert"), SiteID: in.SiteID, Severity: "high", Type: "ota_" + string(in.Status.State), Message: "OTA update " + in.Status.UpdateID + " for device " + dev.ID + ": " + string(in.Status.State) + " (" + in.Status.LastError + ")", CreatedAt: time.Now().UTC()})
	}
	s.note("ok", "agent", in.SiteID, "", in.SiteID, dev.ID, "ota.status", "OTA status: "+string(in.Status.State), map[string]any{"device_id": dev.ID, "update_id": in.Status.UpdateID, "state": in.Status.State})
	s.syncOTACampaignsForDevice(dev.ID, in.Status)
	writeJSON(w, 200, tw)
}
