// Copyright 2026 Zyvor
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"net/http"
	"strconv"
	"sync"
	"time"
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

func (s *Server) note(level, source, chapter, siteID, site, action, message string, detail map[string]any) {
	if s.activity == nil {
		return
	}
	s.activity.add(ActivityEntry{
		Level: level, Source: source, Chapter: chapter,
		SiteID: siteID, Site: site, Action: action, Message: message, Detail: detail,
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
	writeJSON(w, 200, asJSONList(s.activity.list(limit)))
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
