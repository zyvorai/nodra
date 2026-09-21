// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/zyvorai/nodra/internal/model"
	"github.com/zyvorai/nodra/pkg/ota"
)

// otaCampaigns lists campaigns visible to the caller (every site involved
// must pass callerCanSeeSite — an org token never sees another org's
// multi-site campaign).
func (s *Server) otaCampaigns(w http.ResponseWriter, r *http.Request) {
	v := s.store.OTACampaigns()
	out := v[:0]
	for _, c := range v {
		if s.callerCanSeeCampaign(r, c) {
			out = append(out, c)
		}
	}
	writeJSON(w, 200, asJSONList(out))
}

func (s *Server) otaCampaignGet(w http.ResponseWriter, r *http.Request) {
	c, ok := s.store.OTACampaign(r.PathValue("id"))
	if !ok || !s.callerCanSeeCampaign(r, c) {
		errorJSON(w, 404, "campaign not found")
		return
	}
	c = s.refreshOTACampaign(c)
	writeJSON(w, 200, c)
}

func (s *Server) otaCampaignCreate(w http.ResponseWriter, r *http.Request) {
	var raw struct {
		Name                    string                   `json:"name"`
		SiteIDs                 []string                 `json:"site_ids"`
		DeviceIDs               []string                 `json:"device_ids"`
		Stages                  []model.OTACampaignStage `json:"stages"`
		Manifest                ota.Manifest             `json:"manifest"`
		Policy                  ota.Policy               `json:"policy"`
		FailureThresholdPercent *int                     `json:"failure_threshold_percent"`
	}
	if !s.decode(w, r, &raw) {
		return
	}
	in := model.OTACampaign{
		Name:      raw.Name,
		SiteIDs:   raw.SiteIDs,
		DeviceIDs: raw.DeviceIDs,
		Stages:    raw.Stages,
		Manifest:  raw.Manifest,
		Policy:    raw.Policy,
	}
	if raw.FailureThresholdPercent != nil {
		in.FailureThresholdPercent = *raw.FailureThresholdPercent
	} else {
		in.FailureThresholdPercent = 10
	}
	if err := validateOTACampaignInput(&in); err != nil {
		errorJSON(w, 400, err.Error())
		return
	}
	sites, err := s.resolveCampaignSiteIDs(in)
	if err != nil {
		errorJSON(w, 400, err.Error())
		return
	}
	for _, siteID := range sites {
		if !s.callerCanMutateSite(r, siteID) {
			errorJSON(w, 403, "an org-scoped token may only create campaigns for its own org's sites")
			return
		}
	}
	now := time.Now().UTC()
	in.ID = id("otacamp")
	in.Status = model.OTACampaignDraft
	in.CurrentStage = -1
	in.TargetDeviceIDs = nil
	in.Devices = nil
	in.CreatedAt = now
	in.UpdatedAt = now
	if err := s.store.AddOTACampaign(in); err != nil {
		errorJSON(w, 507, err.Error())
		return
	}
	siteNote := ""
	if len(sites) > 0 {
		siteNote = sites[0]
	}
	s.note("ok", "control-plane", s.actorForBearer(bearer(r)), "", siteNote, in.Name, "ota.campaign.create", "OTA campaign created: "+in.Name, map[string]any{"campaign_id": in.ID, "stages": len(in.Stages)})
	writeJSON(w, 201, in)
}

func (s *Server) otaCampaignStart(w http.ResponseWriter, r *http.Request) {
	c, ok := s.loadMutableCampaign(w, r)
	if !ok {
		return
	}
	if c.Status == model.OTACampaignPaused {
		errorJSON(w, 409, "campaign is paused; use POST .../resume")
		return
	}
	if c.Status != model.OTACampaignDraft {
		errorJSON(w, 409, "campaign must be draft to start")
		return
	}
	targets, err := s.resolveCampaignTargets(c)
	if err != nil {
		errorJSON(w, 400, err.Error())
		return
	}
	if len(targets) == 0 {
		errorJSON(w, 400, "campaign has no target devices")
		return
	}
	c.TargetDeviceIDs = targets
	c.CurrentStage = 0
	c.Status = model.OTACampaignRunning
	c.UpdatedAt = time.Now().UTC()
	if err := s.applyCampaignWave(&c); err != nil {
		errorJSON(w, 507, err.Error())
		return
	}
	if err := s.store.UpdateOTACampaign(c.ID, func(out *model.OTACampaign) { *out = c }); err != nil {
		errorJSON(w, 507, err.Error())
		return
	}
	c = s.refreshOTACampaign(c)
	s.note("ok", "control-plane", s.actorForBearer(bearer(r)), "", firstSite(c), c.Name, "ota.campaign.start", "OTA campaign started: "+c.Name, map[string]any{"campaign_id": c.ID, "stage": c.CurrentStage, "selected": len(c.Devices)})
	writeJSON(w, 200, c)
}

func (s *Server) otaCampaignPromote(w http.ResponseWriter, r *http.Request) {
	c, ok := s.loadMutableCampaign(w, r)
	if !ok {
		return
	}
	c = s.refreshOTACampaign(c)
	if c.Status != model.OTACampaignRunning {
		errorJSON(w, 409, "campaign must be running to promote")
		return
	}
	if c.CurrentStage < 0 || c.CurrentStage >= len(c.Stages)-1 {
		errorJSON(w, 409, "campaign is already on the final stage")
		return
	}
	c.CurrentStage++
	c.UpdatedAt = time.Now().UTC()
	if err := s.applyCampaignWave(&c); err != nil {
		errorJSON(w, 507, err.Error())
		return
	}
	if err := s.store.UpdateOTACampaign(c.ID, func(out *model.OTACampaign) { *out = c }); err != nil {
		errorJSON(w, 507, err.Error())
		return
	}
	c = s.refreshOTACampaign(c)
	s.note("ok", "control-plane", s.actorForBearer(bearer(r)), "", firstSite(c), c.Name, "ota.campaign.promote", "OTA campaign promoted: "+c.Name, map[string]any{"campaign_id": c.ID, "stage": c.CurrentStage, "selected": len(c.Devices)})
	writeJSON(w, 200, c)
}

func (s *Server) otaCampaignAbort(w http.ResponseWriter, r *http.Request) {
	c, ok := s.loadMutableCampaign(w, r)
	if !ok {
		return
	}
	if c.Status == model.OTACampaignCompleted || c.Status == model.OTACampaignAborted {
		errorJSON(w, 409, "campaign is already "+c.Status)
		return
	}
	c.Status = model.OTACampaignAborted
	c.UpdatedAt = time.Now().UTC()
	cleared := s.clearInFlightCampaignOTA(&c)
	if err := s.store.UpdateOTACampaign(c.ID, func(out *model.OTACampaign) {
		out.Status = model.OTACampaignAborted
		out.UpdatedAt = c.UpdatedAt
	}); err != nil {
		errorJSON(w, 507, err.Error())
		return
	}
	s.note("warn", "control-plane", s.actorForBearer(bearer(r)), "", firstSite(c), c.Name, "ota.campaign.abort", "OTA campaign aborted: "+c.Name, map[string]any{"campaign_id": c.ID, "cleared_desired": cleared})
	writeJSON(w, 200, c)
}

func (s *Server) otaCampaignPause(w http.ResponseWriter, r *http.Request) {
	c, ok := s.loadMutableCampaign(w, r)
	if !ok {
		return
	}
	if c.Status != model.OTACampaignRunning {
		errorJSON(w, 409, "campaign must be running to pause")
		return
	}
	c.Status = model.OTACampaignPaused
	c.UpdatedAt = time.Now().UTC()
	if err := s.store.UpdateOTACampaign(c.ID, func(out *model.OTACampaign) {
		out.Status = model.OTACampaignPaused
		out.UpdatedAt = c.UpdatedAt
	}); err != nil {
		errorJSON(w, 507, err.Error())
		return
	}
	s.note("ok", "control-plane", s.actorForBearer(bearer(r)), "", firstSite(c), c.Name, "ota.campaign.pause", "OTA campaign paused: "+c.Name, map[string]any{"campaign_id": c.ID, "stage": c.CurrentStage})
	writeJSON(w, 200, c)
}

func (s *Server) otaCampaignResume(w http.ResponseWriter, r *http.Request) {
	c, ok := s.loadMutableCampaign(w, r)
	if !ok {
		return
	}
	if c.Status != model.OTACampaignPaused {
		errorJSON(w, 409, "campaign must be paused to resume")
		return
	}
	c.Status = model.OTACampaignRunning
	c.UpdatedAt = time.Now().UTC()
	if err := s.store.UpdateOTACampaign(c.ID, func(out *model.OTACampaign) {
		out.Status = model.OTACampaignRunning
		out.UpdatedAt = c.UpdatedAt
	}); err != nil {
		errorJSON(w, 507, err.Error())
		return
	}
	c = s.refreshOTACampaign(c)
	s.note("ok", "control-plane", s.actorForBearer(bearer(r)), "", firstSite(c), c.Name, "ota.campaign.resume", "OTA campaign resumed: "+c.Name, map[string]any{"campaign_id": c.ID, "stage": c.CurrentStage})
	writeJSON(w, 200, c)
}

// clearInFlightCampaignOTA removes Twin.Desired["ota"] for devices that have
// not finished (committed/failed/rolled-back) so abort stops further install work.
func (s *Server) clearInFlightCampaignOTA(c *model.OTACampaign) int {
	cleared := 0
	for _, d := range c.Devices {
		switch d.State {
		case ota.StateCommitted, ota.StateFailed, ota.StateRolledBack:
			continue
		}
		dev, ok := s.store.Device(d.DeviceID)
		if !ok {
			continue
		}
		removed := false
		_ = s.store.UpdateTwin(dev.ID, func(tw *model.Twin) {
			if tw.Desired == nil {
				return
			}
			req, has, err := otaRequestFromDesired(tw.Desired)
			if err != nil || !has {
				return
			}
			if req.UpdateID != "" && req.UpdateID != d.UpdateID {
				return
			}
			delete(tw.Desired, otaTwinKey)
			tw.DesiredVersion++
			tw.UpdatedAt = time.Now().UTC()
			removed = true
		})
		if removed {
			cleared++
		}
	}
	return cleared
}

func (s *Server) loadMutableCampaign(w http.ResponseWriter, r *http.Request) (model.OTACampaign, bool) {
	c, ok := s.store.OTACampaign(r.PathValue("id"))
	if !ok || !s.callerCanMutateCampaign(r, c) {
		errorJSON(w, 404, "campaign not found")
		return model.OTACampaign{}, false
	}
	return c, true
}

func (s *Server) callerCanSeeCampaign(r *http.Request, c model.OTACampaign) bool {
	for _, siteID := range campaignSiteIDs(c) {
		if !s.callerCanSeeSite(r, siteID) {
			return false
		}
	}
	return true
}

func (s *Server) callerCanMutateCampaign(r *http.Request, c model.OTACampaign) bool {
	sites := campaignSiteIDs(c)
	if len(sites) == 0 {
		// No sites resolved yet (draft with only device_ids that disappeared) —
		// still require a global token via empty-site mutate rules for each
		// explicit site_id, else deny org tokens on empty.
		if len(c.SiteIDs) == 0 {
			return s.callerCanMutateSite(r, "")
		}
		sites = append([]string(nil), c.SiteIDs...)
	}
	for _, siteID := range sites {
		if !s.callerCanMutateSite(r, siteID) {
			return false
		}
	}
	return true
}

func campaignSiteIDs(c model.OTACampaign) []string {
	seen := map[string]struct{}{}
	var out []string
	add := func(id string) {
		if id == "" {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	for _, id := range c.SiteIDs {
		add(id)
	}
	for _, d := range c.Devices {
		add(d.SiteID)
	}
	return out
}

func firstSite(c model.OTACampaign) string {
	sites := campaignSiteIDs(c)
	if len(sites) == 0 && len(c.SiteIDs) > 0 {
		return c.SiteIDs[0]
	}
	if len(sites) == 0 {
		return ""
	}
	return sites[0]
}

func validateOTACampaignInput(in *model.OTACampaign) error {
	if strings.TrimSpace(in.Name) == "" {
		return fmt.Errorf("name is required")
	}
	if len(in.SiteIDs) == 0 && len(in.DeviceIDs) == 0 {
		return fmt.Errorf("site_ids or device_ids is required")
	}
	if len(in.Stages) == 0 {
		return fmt.Errorf("stages is required")
	}
	prev := 0
	for i, st := range in.Stages {
		if st.CanaryPercent < 1 || st.CanaryPercent > 100 {
			return fmt.Errorf("stages[%d].canary_percent must be between 1 and 100", i)
		}
		if st.CanaryPercent <= prev {
			return fmt.Errorf("stages[%d].canary_percent must be strictly increasing", i)
		}
		prev = st.CanaryPercent
	}
	if in.Stages[len(in.Stages)-1].CanaryPercent != 100 {
		return fmt.Errorf("final stage canary_percent must be 100")
	}
	if err := in.Manifest.Validate(); err != nil {
		return err
	}
	if _, err := in.Policy.HealthTimeoutDuration(); err != nil {
		return err
	}
	if in.FailureThresholdPercent < 0 || in.FailureThresholdPercent > 100 {
		return fmt.Errorf("failure_threshold_percent must be between 0 and 100")
	}
	return nil
}

func (s *Server) resolveCampaignSiteIDs(in model.OTACampaign) ([]string, error) {
	seen := map[string]struct{}{}
	var out []string
	add := func(siteID string) error {
		if siteID == "" {
			return fmt.Errorf("empty site_id")
		}
		if _, ok := s.store.Site(siteID); !ok {
			return fmt.Errorf("site %q not found", siteID)
		}
		if _, ok := seen[siteID]; ok {
			return nil
		}
		seen[siteID] = struct{}{}
		out = append(out, siteID)
		return nil
	}
	for _, siteID := range in.SiteIDs {
		if err := add(siteID); err != nil {
			return nil, err
		}
	}
	for _, deviceID := range in.DeviceIDs {
		dev, ok := s.store.Device(deviceID)
		if !ok {
			return nil, fmt.Errorf("device %q not found", deviceID)
		}
		if len(in.SiteIDs) > 0 {
			allowed := false
			for _, sid := range in.SiteIDs {
				if sid == dev.SiteID {
					allowed = true
					break
				}
			}
			if !allowed {
				return nil, fmt.Errorf("device %q is not in the campaign site_ids", deviceID)
			}
		}
		if err := add(dev.SiteID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Server) resolveCampaignTargets(c model.OTACampaign) ([]string, error) {
	seen := map[string]struct{}{}
	var out []string
	add := func(dev model.Device) {
		if _, ok := seen[dev.ID]; ok {
			return
		}
		seen[dev.ID] = struct{}{}
		out = append(out, dev.ID)
	}
	if len(c.DeviceIDs) > 0 {
		for _, deviceID := range c.DeviceIDs {
			dev, ok := s.store.Device(deviceID)
			if !ok {
				return nil, fmt.Errorf("device %q not found", deviceID)
			}
			if len(c.SiteIDs) > 0 {
				allowed := false
				for _, sid := range c.SiteIDs {
					if sid == dev.SiteID {
						allowed = true
						break
					}
				}
				if !allowed {
					return nil, fmt.Errorf("device %q is not in the campaign site_ids", deviceID)
				}
			}
			add(dev)
		}
	} else {
		siteSet := map[string]struct{}{}
		for _, sid := range c.SiteIDs {
			siteSet[sid] = struct{}{}
		}
		for _, dev := range s.store.Devices() {
			if _, ok := siteSet[dev.SiteID]; ok {
				add(dev)
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

// applyCampaignWave selects devices for c.CurrentStage's cumulative canary
// percent and writes Twin.Desired["ota"] for each newly selected device.
func (s *Server) applyCampaignWave(c *model.OTACampaign) error {
	if c.CurrentStage < 0 || c.CurrentStage >= len(c.Stages) {
		return fmt.Errorf("invalid current stage")
	}
	percent := c.Stages[c.CurrentStage].CanaryPercent
	want := (len(c.TargetDeviceIDs)*percent + 99) / 100 // ceil
	if want < 1 && len(c.TargetDeviceIDs) > 0 {
		want = 1
	}
	if want > len(c.TargetDeviceIDs) {
		want = len(c.TargetDeviceIDs)
	}
	selected := map[string]struct{}{}
	for _, d := range c.Devices {
		selected[d.DeviceID] = struct{}{}
	}
	need := want - len(selected)
	if need <= 0 {
		return nil
	}
	for _, deviceID := range c.TargetDeviceIDs {
		if need <= 0 {
			break
		}
		if _, ok := selected[deviceID]; ok {
			continue
		}
		dev, ok := s.store.Device(deviceID)
		if !ok {
			return fmt.Errorf("device %q not found", deviceID)
		}
		updateID := c.ID + "-" + deviceID
		req := ota.Request{
			UpdateID: updateID,
			DeviceID: deviceID,
			SiteID:   dev.SiteID,
			Manifest: c.Manifest,
			Policy:   c.Policy,
		}
		if _, err := s.setDeviceOTADesired(dev, req); err != nil {
			return err
		}
		c.Devices = append(c.Devices, model.OTACampaignDevice{
			DeviceID: deviceID,
			SiteID:   dev.SiteID,
			UpdateID: updateID,
			Stage:    c.CurrentStage,
		})
		selected[deviceID] = struct{}{}
		need--
	}
	return nil
}

// refreshOTACampaign pulls per-device outcomes from twins and may transition
// the campaign to completed or aborted (failure threshold). Persists when
// status or device states change.
func (s *Server) refreshOTACampaign(c model.OTACampaign) model.OTACampaign {
	if c.Status != model.OTACampaignRunning && c.Status != model.OTACampaignPaused {
		return c
	}
	changed := false
	failed := 0
	committed := 0
	for i := range c.Devices {
		tw, ok := s.store.Twin(c.Devices[i].DeviceID)
		if !ok {
			continue
		}
		st, has, err := otaStatusFromReported(tw.Reported)
		if err != nil || !has {
			continue
		}
		if st.UpdateID != "" && st.UpdateID != c.Devices[i].UpdateID {
			continue
		}
		if c.Devices[i].State != st.State {
			c.Devices[i].State = st.State
			changed = true
		}
		switch st.State {
		case ota.StateFailed, ota.StateRolledBack:
			failed++
		case ota.StateCommitted:
			committed++
		}
	}
	n := len(c.Devices)
	if n > 0 && failed > 0 && failed*100/n >= c.FailureThresholdPercent {
		if c.Status != model.OTACampaignAborted {
			c.Status = model.OTACampaignAborted
			c.UpdatedAt = time.Now().UTC()
			changed = true
			_ = s.clearInFlightCampaignOTA(&c)
			_ = s.store.AddAlert(model.Alert{
				ID: id("alert"), SiteID: firstSite(c), Severity: "high", Type: "ota_campaign_aborted",
				Message:   fmt.Sprintf("OTA campaign %s aborted: failure threshold %d%% exceeded (%d/%d)", c.Name, c.FailureThresholdPercent, failed, n),
				CreatedAt: time.Now().UTC(),
			})
		}
	} else if c.Status == model.OTACampaignRunning &&
		c.CurrentStage == len(c.Stages)-1 &&
		n > 0 && committed == n && n == len(c.TargetDeviceIDs) {
		c.Status = model.OTACampaignCompleted
		c.UpdatedAt = time.Now().UTC()
		changed = true
	}
	if changed {
		_ = s.store.UpdateOTACampaign(c.ID, func(out *model.OTACampaign) { *out = c })
		if latest, ok := s.store.OTACampaign(c.ID); ok {
			c = latest
		}
	}
	return c
}

// syncOTACampaignsForDevice updates any running campaign that selected this
// device when an agent reports OTA status.
func (s *Server) syncOTACampaignsForDevice(deviceID string, st ota.Status) {
	for _, c := range s.store.OTACampaigns() {
		if c.Status != model.OTACampaignRunning && c.Status != model.OTACampaignPaused {
			continue
		}
		for _, d := range c.Devices {
			if d.DeviceID == deviceID && (st.UpdateID == "" || st.UpdateID == d.UpdateID) {
				_ = s.refreshOTACampaign(c)
				break
			}
		}
	}
}
