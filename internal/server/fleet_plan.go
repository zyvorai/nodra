// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-2.0

package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sort"

	"github.com/zyvorai/nodra/internal/model"
	"github.com/zyvorai/nodra/internal/twinschema"
)

// maxPlanDevices bounds one fleet plan (memory and response size).
const maxPlanDevices = 5000

// Plan statuses for one device.
const (
	planChange    = "change"
	planUnchanged = "unchanged"
	planInvalid   = "invalid"
	planUnknown   = "unknown_device"
)

type fleetPlanRequest struct {
	// Defaults is applied (top-level keys) to every listed device and to every
	// device of SiteIDs; a per-device entry overrides it key by key.
	Defaults map[string]any            `json:"defaults,omitempty"`
	SiteIDs  []string                  `json:"site_ids,omitempty"`
	Devices  map[string]map[string]any `json:"devices,omitempty"`
}

type fleetPlanDevice struct {
	DeviceID       string                 `json:"device_id"`
	SiteID         string                 `json:"site_id,omitempty"`
	Status         string                 `json:"status"`
	DesiredVersion uint64                 `json:"desired_version"`
	Changes        []twinschema.Change    `json:"changes,omitempty"`
	Violations     []twinschema.Violation `json:"violations,omitempty"`
	Error          string                 `json:"error,omitempty"`
}

type fleetPlanSummary struct {
	Devices   int `json:"devices"`
	Changed   int `json:"changed"`
	Unchanged int `json:"unchanged"`
	Invalid   int `json:"invalid"`
	Unknown   int `json:"unknown"`
}

type fleetPlan struct {
	DryRun  bool              `json:"dry_run"`
	Valid   bool              `json:"valid"`
	Hash    string            `json:"plan_hash"`
	Summary fleetPlanSummary  `json:"summary"`
	Devices []fleetPlanDevice `json:"devices"`
}

// planEntry is what the plan hash covers: the device, the twin version the
// plan was computed against, and the exact desired document it would store.
type planEntry struct {
	DeviceID       string         `json:"device_id"`
	Status         string         `json:"status"`
	DesiredVersion uint64         `json:"desired_version"`
	Desired        map[string]any `json:"desired,omitempty"`
}

// buildFleetPlan computes the per-device result for a desired fleet config.
// canMutate decides which sites the caller may touch; devices outside it are
// reported as unknown_device so existence is not leaked across orgs. The
// result is a pure function of (request, fleet state): no clocks, no map
// order, so plan_hash is stable and a later apply can refuse a plan whose
// base versions have moved.
func buildFleetPlan(req fleetPlanRequest, devices []model.Device, twin func(string) (model.Twin, bool), canMutate func(siteID string) bool) fleetPlan {
	byID := map[string]model.Device{}
	for _, d := range devices {
		if canMutate(d.SiteID) {
			byID[d.ID] = d
		}
	}
	siteSel := map[string]bool{}
	for _, s := range req.SiteIDs {
		siteSel[s] = true
	}
	targets := map[string]map[string]any{} // device -> device-specific desired
	for id, d := range req.Devices {
		targets[id] = d
	}
	if len(req.Defaults) > 0 {
		for id, d := range byID {
			if siteSel[d.SiteID] {
				if _, ok := targets[id]; !ok {
					targets[id] = map[string]any{}
				}
			}
		}
	}
	ids := make([]string, 0, len(targets))
	for id := range targets {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	plan := fleetPlan{DryRun: true, Devices: []fleetPlanDevice{}}
	var entries []planEntry
	for _, id := range ids {
		pd := fleetPlanDevice{DeviceID: id}
		dev, ok := byID[id]
		if !ok {
			pd.Status = planUnknown
			pd.Error = "device not found"
			plan.Devices = append(plan.Devices, pd)
			plan.Summary.Unknown++
			entries = append(entries, planEntry{DeviceID: id, Status: planUnknown})
			continue
		}
		pd.SiteID = dev.SiteID
		cur, _ := twin(id)
		pd.DesiredVersion = cur.DesiredVersion
		next := map[string]any{}
		for k, v := range req.Defaults {
			next[k] = v
		}
		for k, v := range targets[id] {
			next[k] = v
		}
		if _, reserved := next["ota"]; reserved {
			pd.Status = planInvalid
			pd.Error = "the ota key is reserved; use the OTA endpoints"
			plan.Summary.Invalid++
			plan.Devices = append(plan.Devices, pd)
			entries = append(entries, planEntry{DeviceID: id, Status: planInvalid, DesiredVersion: cur.DesiredVersion})
			continue
		}
		if cur.Schema != nil {
			pd.Violations = twinschema.Validate(cur.Schema, next)
		}
		// An in-flight OTA request lives under desired.ota (outside the config
		// schema); a config plan must never wipe it, so it is carried over.
		if o, has := cur.Desired["ota"]; has {
			next["ota"] = o
		}
		pd.Changes = twinschema.Diff(cur.Desired, next)
		switch {
		case len(pd.Violations) > 0:
			pd.Status = planInvalid
			plan.Summary.Invalid++
		case len(pd.Changes) == 0:
			pd.Status = planUnchanged
			plan.Summary.Unchanged++
		default:
			pd.Status = planChange
			plan.Summary.Changed++
		}
		plan.Devices = append(plan.Devices, pd)
		entries = append(entries, planEntry{DeviceID: id, Status: pd.Status, DesiredVersion: cur.DesiredVersion, Desired: next})
	}
	plan.Summary.Devices = len(plan.Devices)
	plan.Valid = plan.Summary.Invalid == 0 && plan.Summary.Unknown == 0
	b, _ := json.Marshal(entries) // map keys are sorted by encoding/json
	sum := sha256.Sum256(b)
	plan.Hash = "sha256:" + hex.EncodeToString(sum[:])
	return plan
}

// fleetPlanHandler serves POST /api/v1/twins/plan: validate and diff a desired
// fleet configuration without storing anything.
func (s *Server) fleetPlanHandler(w http.ResponseWriter, r *http.Request) {
	var in fleetPlanRequest
	if !s.decode(w, r, &in) {
		return
	}
	if len(in.Devices) == 0 && !(len(in.Defaults) > 0 && len(in.SiteIDs) > 0) {
		errorJSON(w, 400, "provide devices, or defaults together with site_ids")
		return
	}
	if len(in.Devices) > maxPlanDevices {
		errorJSON(w, 400, "too many devices in one plan")
		return
	}
	plan := buildFleetPlan(in, s.store.Devices(), s.store.Twin, func(site string) bool { return s.callerCanMutateSite(r, site) })
	if len(plan.Devices) > maxPlanDevices {
		errorJSON(w, 400, "plan exceeds the device limit; narrow site_ids")
		return
	}
	writeJSON(w, 200, plan)
}
