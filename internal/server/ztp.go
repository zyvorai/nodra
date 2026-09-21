// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"net/http"
	"strings"
	"time"

	"github.com/zyvorai/nodra/internal/auth"
	"github.com/zyvorai/nodra/internal/model"
	"github.com/zyvorai/nodra/internal/pki"
)

// ztpBootstrap is zero-touch provisioning for an edge host that has only a
// control-plane URL and an enrollment token. It enrolls a site and returns
// the agent token plus a ready-to-write nodrad.json skeleton.
func (s *Server) ztpBootstrap(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name            string            `json:"name"`
		EnrollmentToken string            `json:"enrollment_token"`
		Metadata        map[string]string `json:"metadata"`
		Listen          string            `json:"listen"`
		MQTTListen      string            `json:"mqtt_listen"`
		CSRPem          string            `json:"csr_pem,omitempty"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	if s.attemptLimited("enroll", r) {
		errorJSON(w, 429, "too many enrollment attempts")
		return
	}
	if strings.TrimSpace(in.Name) == "" {
		errorJSON(w, 400, "site name is required")
		return
	}
	orgID := ""
	switch {
	case s.cfg.EnrollmentToken != "" && auth.EqualToken(in.EnrollmentToken, s.cfg.EnrollmentToken):
		s.enrollMu.Lock()
		expired := !s.enrollExpires.IsZero() && time.Now().UTC().After(s.enrollExpires)
		s.enrollMu.Unlock()
		if expired {
			s.recordAttempt("enroll", r)
			errorJSON(w, 401, "enrollment token expired")
			return
		}
	default:
		matched := false
		for _, org := range s.store.Orgs() {
			if org.EnrollmentTokenHash != "" && auth.EqualHash(org.EnrollmentTokenHash, in.EnrollmentToken) {
				orgID, matched = org.ID, true
				break
			}
		}
		if !matched {
			s.recordAttempt("enroll", r)
			errorJSON(w, 401, "invalid enrollment token")
			return
		}
	}
	tok, err := auth.NewToken(24)
	if err != nil {
		errorJSON(w, 500, "token generation failed")
		return
	}
	now := time.Now().UTC()
	site := model.Site{ID: id("site"), Name: in.Name, TokenHash: auth.Hash(tok), Status: "online", CreatedAt: now, LastSeen: now, Metadata: in.Metadata, OrgID: orgID}
	listen := in.Listen
	if listen == "" {
		listen = "127.0.0.1:9091"
	}
	mqttListen := in.MQTTListen
	if mqttListen == "" {
		mqttListen = "127.0.0.1:1883"
	}
	base := s.cfg.PublicBaseURL
	if base == "" {
		base = "http://" + r.Host
	}
	localTok, err := auth.NewToken(16)
	if err != nil {
		errorJSON(w, 500, "token generation failed")
		return
	}
	agentCfg := map[string]any{
		"server_url":       strings.TrimRight(base, "/"),
		"site_name":        site.Name,
		"site_id":          site.ID,
		"agent_token":      tok,
		"data_dir":         "./nodra-agent-data",
		"listen":           listen,
		"mqtt_listen":      mqttListen,
		"local_token":      localTok,
		"heartbeat":        "30s",
		"flush_interval":   "5s",
		"runner":           "none",
		"max_spool_bytes":  2 << 30,
		"max_spool_events": 1000000,
		"spool_policy":     "reject",
	}
	out := map[string]any{
		"site_id":      site.ID,
		"agent_token":  tok,
		"agent_config": agentCfg,
	}
	if s.ca != nil && in.CSRPem != "" {
		signed, er := pki.SignCSR(s.ca, []byte(in.CSRPem), site.ID, 90*24*time.Hour, s.crlDistributionPoints()...)
		if er != nil {
			errorJSON(w, 400, "csr sign failed: "+er.Error())
			return
		}
		out["client_certificate"] = string(signed.CertPEM)
		out["ca_certificate"] = string(s.ca.CertPEM)
		agentCfg["request_certificate"] = false
	}
	if err := s.store.AddSite(site); err != nil {
		errorJSON(w, 507, err.Error())
		return
	}
	s.clearAttempts("enroll", r)
	s.note("ok", "control-plane", site.Name, "", site.ID, site.Name, "ztp.bootstrap", "ZTP bootstrap enrolled site: "+site.Name, map[string]any{"site_id": site.ID})
	writeJSON(w, 201, out)
}
