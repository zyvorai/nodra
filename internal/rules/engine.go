// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-2.0

// Package rules is Nodra's offline rule engine: hysteresis alarms with
// debounce, rate limits and site-local time windows, evaluated at the edge.
// Rules are data (see Parse), never code, and the only actions are emitting
// events through the normal ingest path. The engine is a pure state machine:
// the caller supplies the time, persists Snapshot() and executes the returned
// Outcomes, which makes it deterministic to test and to replay.
package rules

import (
	"encoding/json"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/zyvorai/nodra/internal/router"
)

// SnapshotVersion is the on-disk state format version.
const SnapshotVersion = 1

// KeyState is the persisted state of one rule for one concrete topic.
type KeyState struct {
	Firing       bool        `json:"firing"`
	Since        time.Time   `json:"since,omitempty"`
	Pending      string      `json:"pending,omitempty"` // "raise" or "clear"
	PendingSince time.Time   `json:"pending_since,omitempty"`
	LastValue    float64     `json:"last_value"`
	LastSeen     time.Time   `json:"last_seen"`
	Fires        []time.Time `json:"fires,omitempty"`
	Suppressed   uint64      `json:"suppressed,omitempty"`
}

// Snapshot is the durable form of all rule state.
type Snapshot struct {
	Version int                            `json:"version"`
	SavedAt time.Time                      `json:"saved_at"`
	Rules   map[string]map[string]KeyState `json:"rules"`
}

// Outcome is one state transition. Actions is empty when the firing was
// rate-limited (Suppressed) - the state still changed.
type Outcome struct {
	Rule       string    `json:"rule"`
	Key        string    `json:"key"`
	Kind       string    `json:"kind"` // "fire" or "clear"
	Value      float64   `json:"value"`
	At         time.Time `json:"at"`
	Suppressed bool      `json:"suppressed,omitempty"`
	Actions    []Action  `json:"-"`
}

// Counters are cumulative engine statistics.
type Counters struct {
	Evaluated   uint64 `json:"evaluated"`
	Matched     uint64 `json:"matched"`
	Skipped     uint64 `json:"skipped"` // matched topic but field missing, non-numeric or payload invalid/oversized
	Fired       uint64 `json:"fired"`
	Cleared     uint64 `json:"cleared"`
	RateLimited uint64 `json:"rate_limited"`
	KeyOverflow uint64 `json:"key_overflow"`
	OutOfWindow uint64 `json:"out_of_window"`
}

// Engine evaluates a RuleSet. Safe for concurrent use.
type Engine struct {
	mu       sync.Mutex
	rs       *RuleSet
	state    map[string]map[string]*KeyState
	total    int
	dirty    bool
	counters Counters
}

// NewEngine returns an engine with empty state.
func NewEngine(rs *RuleSet) *Engine {
	e := &Engine{rs: rs, state: map[string]map[string]*KeyState{}}
	for _, r := range rs.rules {
		e.state[r.Name] = map[string]*KeyState{}
	}
	return e
}

// Evaluate feeds one message at time now and returns transitions. Messages
// whose payload is not a JSON object with a finite number at the rule field
// are counted as skipped and never change state.
func (e *Engine) Evaluate(now time.Time, topic string, payload []byte) []Outcome {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.counters.Evaluated++
	var obj map[string]any
	parsed := false
	var out []Outcome
	for _, r := range e.rs.rules {
		if !router.Match(r.Topic, topic) {
			continue
		}
		e.counters.Matched++
		if !parsed {
			parsed = true
			if len(payload) <= MaxPayloadBytes {
				if json.Unmarshal(payload, &obj) != nil {
					obj = nil
				}
			}
		}
		v, ok := lookup(obj, r.fieldPath)
		if !ok || len(topic) > MaxTopicLen {
			e.counters.Skipped++
			continue
		}
		if o, ok := e.step(r, now, topic, v); ok {
			out = append(out, o)
		}
	}
	return out
}

func lookup(obj map[string]any, path []string) (float64, bool) {
	var cur any = obj
	for _, p := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return 0, false
		}
		cur, ok = m[p]
		if !ok {
			return 0, false
		}
	}
	f, ok := cur.(float64)
	if !ok || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false
	}
	return f, true
}

func (c *compiled) inWindow(now time.Time) bool {
	if !c.hasWindow {
		return true
	}
	t := now.In(c.loc)
	mins := t.Hour()*60 + t.Minute()
	if c.startMin < c.endMin {
		return c.days[t.Weekday()] && mins >= c.startMin && mins < c.endMin
	}
	// Overnight window: belongs to the day it started on.
	if mins >= c.startMin {
		return c.days[t.Weekday()]
	}
	if mins < c.endMin {
		return c.days[(t.Weekday()+6)%7]
	}
	return false
}

func (e *Engine) step(r *compiled, now time.Time, key string, v float64) (Outcome, bool) {
	ks := e.state[r.Name]
	st, ok := ks[key]
	if !ok {
		if len(ks) >= r.maxKeys || e.total >= MaxTotalKeys {
			if !e.evict(ks) {
				e.counters.KeyOverflow++
				return Outcome{}, false
			}
		}
		st = &KeyState{}
		ks[key] = st
		e.total++
	}
	// A clock that went backwards must not wedge debounce or rate limits.
	if st.PendingSince.After(now) {
		st.PendingSince = now
	}
	if st.Since.After(now) {
		st.Since = now
	}
	for i := range st.Fires {
		if st.Fires[i].After(now) {
			st.Fires[i] = now
		}
	}
	st.LastSeen = now
	st.LastValue = v

	var raiseCond, clearCond bool
	if r.above {
		raiseCond, clearCond = v > r.raise, v < r.clear
	} else {
		raiseCond, clearCond = v < r.raise, v > r.clear
	}
	if !st.Firing {
		inWin := r.inWindow(now)
		if !inWin && raiseCond {
			e.counters.OutOfWindow++
		}
		if !raiseCond || !inWin {
			if st.Pending != "" {
				st.Pending, st.PendingSince = "", time.Time{}
				e.dirty = true
			}
			return Outcome{}, false
		}
		if st.Pending != "raise" {
			st.Pending, st.PendingSince = "raise", now
			e.dirty = true
		}
		if now.Sub(st.PendingSince) < r.debounce {
			return Outcome{}, false
		}
		st.Firing, st.Since = true, now
		st.Pending, st.PendingSince = "", time.Time{}
		e.dirty = true
		e.counters.Fired++
		o := Outcome{Rule: r.Name, Key: key, Kind: "fire", Value: v, At: now}
		st.Fires = pruneFires(st.Fires, now, r.ratePer)
		if r.rateMax > 0 && len(st.Fires) >= r.rateMax {
			st.Suppressed++
			e.counters.RateLimited++
			o.Suppressed = true
			return o, true
		}
		if r.rateMax > 0 {
			st.Fires = append(st.Fires, now)
		}
		o.Actions = r.OnFire
		return o, true
	}
	// Firing: only a clear can happen. The dead band between clear and raise
	// keeps the alarm latched.
	if !clearCond {
		if st.Pending != "" {
			st.Pending, st.PendingSince = "", time.Time{}
			e.dirty = true
		}
		return Outcome{}, false
	}
	if st.Pending != "clear" {
		st.Pending, st.PendingSince = "clear", now
		e.dirty = true
	}
	if now.Sub(st.PendingSince) < r.clearDebounce {
		return Outcome{}, false
	}
	st.Firing, st.Since = false, time.Time{}
	st.Pending, st.PendingSince = "", time.Time{}
	e.dirty = true
	e.counters.Cleared++
	return Outcome{Rule: r.Name, Key: key, Kind: "clear", Value: v, At: now, Actions: r.OnClear}, true
}

func pruneFires(f []time.Time, now time.Time, per time.Duration) []time.Time {
	if per <= 0 {
		return nil
	}
	n := 0
	for _, t := range f {
		if now.Sub(t) < per {
			f[n] = t
			n++
		}
	}
	return f[:n]
}

// evict drops the least-recently-seen idle key (not firing, not pending) so
// new topics can be tracked; it reports false when every key is active.
func (e *Engine) evict(ks map[string]*KeyState) bool {
	var victim string
	var oldest time.Time
	found := false
	for k, st := range ks {
		if st.Firing || st.Pending != "" {
			continue
		}
		if !found || st.LastSeen.Before(oldest) || (st.LastSeen.Equal(oldest) && k < victim) {
			victim, oldest, found = k, st.LastSeen, true
		}
	}
	if !found {
		return false
	}
	delete(ks, victim)
	e.total--
	return true
}

// TakeDirty reports (and clears) whether state changed since the last call.
func (e *Engine) TakeDirty() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	d := e.dirty
	e.dirty = false
	return d
}

// Counters returns a copy of the statistics.
func (e *Engine) Counters() Counters {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.counters
}

// Firing returns "rule key" pairs currently firing, sorted.
func (e *Engine) Firing() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []string
	for rule, ks := range e.state {
		for k, st := range ks {
			if st.Firing {
				out = append(out, rule+" "+k)
			}
		}
	}
	sort.Strings(out)
	return out
}

// Snapshot returns a compact copy of the durable state: idle keys with no
// firing, pending or recent firings are omitted so the file stays small.
func (e *Engine) Snapshot(now time.Time) Snapshot {
	e.mu.Lock()
	defer e.mu.Unlock()
	s := Snapshot{Version: SnapshotVersion, SavedAt: now, Rules: map[string]map[string]KeyState{}}
	per := map[string]time.Duration{}
	for _, r := range e.rs.rules {
		per[r.Name] = r.ratePer
	}
	for rule, ks := range e.state {
		for k, st := range ks {
			cp := *st
			cp.Fires = pruneFires(append([]time.Time(nil), st.Fires...), now, per[rule])
			if !cp.Firing && cp.Pending == "" && len(cp.Fires) == 0 {
				continue
			}
			if s.Rules[rule] == nil {
				s.Rules[rule] = map[string]KeyState{}
			}
			s.Rules[rule][k] = cp
		}
	}
	return s
}

// Restore loads persisted state. State for rules that no longer exist is
// dropped, per-rule and total key caps are enforced and implausible values
// (unknown pending kind, oversized fire history) are sanitised, so a
// corrupt or hostile file cannot grow memory or wedge a rule. It returns the
// number of keys restored.
func (e *Engine) Restore(s Snapshot) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	n := 0
	for _, r := range e.rs.rules {
		in := s.Rules[r.Name]
		keys := make([]string, 0, len(in))
		for k := range in {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if len(k) > MaxTopicLen || len(e.state[r.Name]) >= r.maxKeys || e.total >= MaxTotalKeys {
				continue
			}
			st := in[k]
			switch {
			case st.Pending != "" && st.Pending != "raise" && st.Pending != "clear",
				st.Firing && st.Pending == "raise",
				!st.Firing && st.Pending == "clear":
				st.Pending, st.PendingSince = "", time.Time{}
			}
			if r.rateMax == 0 {
				st.Fires = nil
			} else if len(st.Fires) > r.rateMax {
				st.Fires = st.Fires[len(st.Fires)-r.rateMax:]
			}
			if math.IsNaN(st.LastValue) || math.IsInf(st.LastValue, 0) {
				st.LastValue = 0
			}
			cp := st
			e.state[r.Name][k] = &cp
			e.total++
			n++
		}
	}
	return n
}

// Status describes one rule for the status endpoint.
type Status struct {
	Rule   string              `json:"rule"`
	Topic  string              `json:"topic"`
	Keys   map[string]KeyState `json:"keys"`
	Firing int                 `json:"firing"`
}

// Status returns per-rule state in rule file order.
func (e *Engine) Status() []Status {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]Status, 0, len(e.rs.rules))
	for _, r := range e.rs.rules {
		s := Status{Rule: r.Name, Topic: r.Topic, Keys: map[string]KeyState{}}
		for k, st := range e.state[r.Name] {
			s.Keys[k] = *st
			if st.Firing {
				s.Firing++
			}
		}
		out = append(out, s)
	}
	return out
}
