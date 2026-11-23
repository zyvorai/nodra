// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-2.0

package server

import (
	"fmt"
	"net/http"
	"time"

	"github.com/zyvorai/nodra/internal/model"
	"github.com/zyvorai/nodra/internal/twinschema"
)

// twinSchemaSet registers (or, with a null/empty schema, clears) the JSON
// Schema-subset that constrains a twin's desired state. The existing desired
// state is not rewritten; it is checked and reported in the response so an
// operator sees immediately whether the current state already violates it.
func (s *Server) twinSchemaSet(w http.ResponseWriter, r *http.Request) {
	dev, ok := s.store.Device(r.PathValue("id"))
	if !ok || !s.callerCanMutateSite(r, dev.SiteID) {
		errorJSON(w, 404, "device not found")
		return
	}
	var in struct {
		Schema map[string]any `json:"schema"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	if len(in.Schema) == 0 {
		in.Schema = nil
	}
	if in.Schema != nil {
		if err := twinschema.CheckSchema(in.Schema); err != nil {
			errorJSON(w, 400, "invalid schema: "+err.Error())
			return
		}
	}
	var tw model.Twin
	if err := s.store.UpdateTwin(dev.ID, func(cur *model.Twin) {
		cur.DeviceID = dev.ID
		cur.SiteID = dev.SiteID
		cur.Schema = in.Schema
		cur.UpdatedAt = time.Now().UTC()
		tw = *cur
	}); err != nil {
		errorJSON(w, 507, err.Error())
		return
	}
	var viol []twinschema.Violation
	if tw.Schema != nil {
		viol = twinschema.Validate(tw.Schema, tw.Desired)
	}
	writeJSON(w, 200, map[string]any{"twin": tw, "current_desired_violations": viol})
}

// twinDriftReport is the drift view of one twin.
type twinDriftReport struct {
	DeviceID           string                 `json:"device_id"`
	SiteID             string                 `json:"site_id"`
	InSync             bool                   `json:"in_sync"`
	Drift              []twinschema.DriftItem `json:"drift"`
	DesiredViolations  []twinschema.Violation `json:"desired_violations,omitempty"`
	ReportedViolations []twinschema.Violation `json:"reported_violations,omitempty"`
	DesiredVersion     uint64                 `json:"desired_version"`
	ReportedVersion    uint64                 `json:"reported_version"`
}

func driftReport(tw model.Twin) twinDriftReport {
	rep := twinDriftReport{DeviceID: tw.DeviceID, SiteID: tw.SiteID, DesiredVersion: tw.DesiredVersion, ReportedVersion: tw.ReportedVersion,
		Drift: twinschema.Drift(tw.Desired, tw.Reported)}
	if rep.Drift == nil {
		rep.Drift = []twinschema.DriftItem{}
	}
	if tw.Schema != nil {
		rep.DesiredViolations = twinschema.Validate(tw.Schema, tw.Desired)
		rep.ReportedViolations = twinschema.Validate(tw.Schema, tw.Reported)
	}
	rep.InSync = len(rep.Drift) == 0 && len(rep.ReportedViolations) == 0
	return rep
}

func (s *Server) twinDrift(w http.ResponseWriter, r *http.Request) {
	tw, ok := s.store.Twin(r.PathValue("id"))
	if !ok || !s.callerCanSeeSite(r, tw.SiteID) {
		errorJSON(w, 404, "twin not found")
		return
	}
	writeJSON(w, 200, driftReport(tw))
}

// twinsDrift lists every visible twin that is out of sync (or reports state
// that violates its schema). Add ?all=true to include in-sync twins.
func (s *Server) twinsDrift(w http.ResponseWriter, r *http.Request) {
	all := r.URL.Query().Get("all") == "true"
	out := []twinDriftReport{}
	for _, tw := range s.store.Snapshot().Twins {
		if !s.callerCanSeeSite(r, tw.SiteID) {
			continue
		}
		if rep := driftReport(tw); all || !rep.InSync {
			out = append(out, rep)
		}
	}
	writeJSON(w, 200, out)
}

// twinDriftMetrics exports how many twins are out of sync fleet-wide.
func (s *Server) twinDriftMetrics() string {
	drifted, total := 0, 0
	for _, tw := range s.store.Snapshot().Twins {
		total++
		if !driftReport(tw).InSync {
			drifted++
		}
	}
	return fmt.Sprintf("# HELP nodra_twins_drifted Twins whose reported state differs from desired or violates the schema.\n# TYPE nodra_twins_drifted gauge\nnodra_twins_drifted %d\n# TYPE nodra_twins_total gauge\nnodra_twins_total %d\n", drifted, total)
}
