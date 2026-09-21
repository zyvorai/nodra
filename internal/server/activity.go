// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/zyvorai/nodra/internal/audit"
)

const activityCap = 2000

// ActivityEntry is a detailed console/simulation log line for demos.
type ActivityEntry struct {
	ID      string         `json:"id"`
	Time    time.Time      `json:"time"`
	Level   string         `json:"level"` // info|warn|error|chapter|ok
	Source  string         `json:"source"`
	Chapter string         `json:"chapter,omitempty"` // A-Z
	SiteID  string         `json:"site_id,omitempty"`
	Site    string         `json:"site,omitempty"`
	Action  string         `json:"action,omitempty"`
	Message string         `json:"message"`
	Detail  map[string]any `json:"detail,omitempty"`
}

type activityLog struct {
	mu   sync.RWMutex
	ring []ActivityEntry
	seq  uint64
}

func (a *activityLog) add(e ActivityEntry) ActivityEntry {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.seq++
	if e.ID == "" {
		e.ID = "act_" + strconv.FormatUint(a.seq, 10)
	}
	if e.Time.IsZero() {
		e.Time = time.Now().UTC()
	}
	if e.Level == "" {
		e.Level = "info"
	}
	if e.Source == "" {
		e.Source = "control-plane"
	}
	a.ring = append(a.ring, e)
	if len(a.ring) > activityCap {
		a.ring = a.ring[len(a.ring)-activityCap:]
	}
	return e
}

func (a *activityLog) list(limit int) []ActivityEntry {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if limit <= 0 || limit > len(a.ring) {
		limit = len(a.ring)
	}
	if limit == 0 {
		return []ActivityEntry{}
	}
	out := make([]ActivityEntry, limit)
	copy(out, a.ring[len(a.ring)-limit:])
	return out
}

// note appends to the in-memory Activity/Logs ring (for the live console tail)
// and, when actor is non-empty, durably audits the same event via s.audit.
// actor is the authenticated principal responsible for the action — an
// admin/viewer username, a site ID for agent-initiated actions, or "" for
// internal/system bookkeeping that isn't a distinct actor's action.
func (s *Server) note(level, source, actor, chapter, siteID, site, action, message string, detail map[string]any) {
	if s.activity != nil {
		s.activity.add(ActivityEntry{
			Level: level, Source: source, Chapter: chapter,
			SiteID: siteID, Site: site, Action: action, Message: message, Detail: detail,
		})
	}
	if s.audit == nil || actor == "" {
		return
	}
	result := "ok"
	switch level {
	case "error":
		result = "error"
	case "warn":
		result = "denied"
	}
	_ = s.audit.Append(audit.Entry{
		Actor: actor, ActorType: source, Action: action, Target: site,
		SiteID: siteID, Result: result, Message: message, Detail: detail,
	})
}

func (s *Server) activityList(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 250
	}
	if limit > 1000 {
		limit = 1000
	}
	v := s.activity.list(limit)
	out := v[:0]
	for _, e := range v {
		if s.callerCanSeeSite(r, e.SiteID) {
			out = append(out, e)
		}
	}
	writeJSON(w, 200, asJSONList(out))
}

func (s *Server) activityPost(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Entries []ActivityEntry `json:"entries"`
		ActivityEntry
	}
	if !s.decode(w, r, &in) {
		return
	}
	var out []ActivityEntry
	if len(in.Entries) > 0 {
		for _, e := range in.Entries {
			if e.Source == "" {
				e.Source = "sim"
			}
			out = append(out, s.activity.add(e))
		}
	} else {
		e := in.ActivityEntry
		if e.Message == "" {
			errorJSON(w, 400, "message is required")
			return
		}
		if e.Source == "" {
			e.Source = "sim"
		}
		out = append(out, s.activity.add(e))
	}
	writeJSON(w, 201, map[string]any{"accepted": len(out), "entries": out})
}

func auditFilterFromQuery(r *http.Request) audit.Filter {
	q := r.URL.Query()
	f := audit.Filter{SiteID: q.Get("site_id"), Action: q.Get("action"), Actor: q.Get("actor"), Cursor: q.Get("cursor")}
	if v := q.Get("since"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			f.Since = t
		}
	}
	if v := q.Get("until"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			f.Until = t
		}
	}
	f.Limit, _ = strconv.Atoi(q.Get("limit"))
	return f
}

// auditList is a small/interactive query over the durable audit trail —
// unlike activityList, it returns a next_cursor since audit history is
// unbounded and can't be handed back as one bare array.
//
// Org filtering here is a post-filter over each fetched page (audit.Filter
// has no notion of "any site in this org"), so an org-scoped caller's page
// can come back with fewer than f.Limit entries even though more exist
// further in — next_cursor still lets them page forward, this only affects
// how full a single page looks, not what they can eventually see.
func (s *Server) auditList(w http.ResponseWriter, r *http.Request) {
	if s.audit == nil {
		writeJSON(w, 200, map[string]any{"entries": []audit.Entry{}, "next_cursor": ""})
		return
	}
	f := auditFilterFromQuery(r)
	if f.Limit <= 0 {
		f.Limit = 250
	}
	if f.SiteID != "" && !s.callerCanSeeSite(r, f.SiteID) {
		writeJSON(w, 200, map[string]any{"entries": []audit.Entry{}, "next_cursor": ""})
		return
	}
	if f.SiteID == "" {
		if orgID, scoped := s.orgForBearer(bearer(r)); scoped {
			f.SiteIDs = s.siteIDsForOrg(orgID)
			if len(f.SiteIDs) == 0 {
				writeJSON(w, 200, map[string]any{"entries": []audit.Entry{}, "next_cursor": ""})
				return
			}
		}
	}
	entries, next, err := s.audit.Query(f)
	if err != nil {
		errorJSON(w, 500, "audit query failed: "+err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"entries": asJSONList(entries), "next_cursor": next})
}

// auditExport streams the full matching audit history as newline-delimited
// JSON, paging through the store internally — deliberately not buffered
// into one JSON array, since the export is meant to cover unbounded history.
func (s *Server) auditExport(w http.ResponseWriter, r *http.Request) {
	if s.audit == nil {
		w.Header().Set("Content-Type", "application/x-ndjson")
		return
	}
	f := auditFilterFromQuery(r)
	f.Limit = 1000
	w.Header().Set("Content-Type", "application/x-ndjson")
	if f.SiteID != "" && !s.callerCanSeeSite(r, f.SiteID) {
		return
	}
	if f.SiteID == "" {
		if orgID, scoped := s.orgForBearer(bearer(r)); scoped {
			f.SiteIDs = s.siteIDsForOrg(orgID)
			if len(f.SiteIDs) == 0 {
				return
			}
		}
	}
	flusher, _ := w.(http.Flusher)
	enc := json.NewEncoder(w)
	for {
		entries, next, err := s.audit.Query(f)
		if err != nil {
			slog.Error("audit export query failed", "error", err)
			return
		}
		for _, e := range entries {
			if err := enc.Encode(e); err != nil {
				return
			}
		}
		if flusher != nil {
			flusher.Flush()
		}
		if next == "" {
			return
		}
		f.Cursor = next
	}
}

// auditRetentionLoop periodically prunes audit history older than the
// configured retention window. Deliberately separate from the 1s delivery
// worker ticker — retention sweeps are rare and comparatively expensive.
func (s *Server) auditRetentionLoop(ctx context.Context) {
	defer s.wg.Done()
	prune := func() {
		if s.audit == nil {
			return
		}
		days := s.cfg.AuditRetentionDays
		if days <= 0 {
			days = 90
		}
		if err := s.audit.Prune(time.Now().UTC().AddDate(0, 0, -days)); err != nil {
			slog.Error("audit retention prune failed", "error", err)
		}
	}
	prune()
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			prune()
		}
	}
}
