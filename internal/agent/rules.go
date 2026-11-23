// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-2.0

package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/zyvorai/nodra/internal/durable"
	"github.com/zyvorai/nodra/internal/rules"
)

const (
	rulesStateFile     = "rules-state.json"
	rulesStateMaxBytes = 8 << 20
	rulesFlushInterval = 5 * time.Second
	rulesEvalBodyMax   = 8 << 20
)

// loadRules reads and strictly validates cfg.RulesFile, then restores the
// persisted firing/pending state so a restart neither re-fires nor loses it.
func (a *Agent) loadRules() error {
	if a.cfg.RulesFile == "" {
		return nil
	}
	src, err := os.ReadFile(a.cfg.RulesFile)
	if err != nil {
		return fmt.Errorf("rules_file: %w", err)
	}
	rs, errs := rules.Parse(src)
	if errs != nil {
		return fmt.Errorf("rules_file %s: %w", a.cfg.RulesFile, errs)
	}
	a.rulesSet = rs
	a.rules = rules.NewEngine(rs)
	if a.rulesNow == nil {
		a.rulesNow = time.Now
	}
	path := filepath.Join(a.cfg.DataDir, rulesStateFile)
	b, err := os.ReadFile(path)
	if err != nil {
		return nil // first start
	}
	var snap rules.Snapshot
	if len(b) > rulesStateMaxBytes || json.Unmarshal(b, &snap) != nil || snap.Version != rules.SnapshotVersion {
		slog.Warn("rules state unreadable; starting with clean state", "file", path)
		_ = os.Rename(path, path+".corrupt")
		return nil
	}
	n := a.rules.Restore(snap)
	slog.Info("rules loaded", "rules", rs.Len(), "restored_keys", n)
	return nil
}

func (a *Agent) saveRules() {
	if a.rules == nil {
		return
	}
	a.rulesSaveMu.Lock()
	defer a.rulesSaveMu.Unlock()
	a.rules.TakeDirty()
	b, err := json.Marshal(a.rules.Snapshot(a.rulesNow()))
	if err != nil {
		return
	}
	if err = durable.AtomicWrite(filepath.Join(a.cfg.DataDir, rulesStateFile), b, 0o600); err != nil {
		slog.Warn("persist rules state failed", "error", err)
	}
}

// runRules evaluates one accepted event. State is persisted before actions
// run (at-most-once actions: a crash in between loses the action but never
// re-fires it after restart).
func (a *Agent) runRules(ctx context.Context, topic string, payload json.RawMessage) {
	outs := a.rules.Evaluate(a.rulesNow(), topic, payload)
	if len(outs) == 0 {
		return
	}
	a.saveRules()
	for _, o := range outs {
		for _, act := range o.Actions {
			ev, err := rules.Render(o, act)
			if err != nil {
				slog.Warn("rule action render failed", "rule", o.Rule, "error", err)
				continue
			}
			if _, err = a.ingestCore(ctx, ev.Topic, ev.Payload, ev.Headers, time.Time{}, false); err != nil {
				slog.Warn("rule action ingest failed", "rule", o.Rule, "error", err)
			}
		}
	}
}

func (a *Agent) rulesLoop(ctx context.Context) {
	defer a.wg.Done()
	t := time.NewTicker(rulesFlushInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			a.saveRules()
			return
		case <-t.C:
			if a.rules.TakeDirty() {
				a.saveRules()
			}
		}
	}
}

func (a *Agent) rulesFiring() int {
	if a.rules == nil {
		return 0
	}
	return len(a.rules.Firing())
}

// localRules serves GET /v1/rules.
func (a *Agent) localRules(w http.ResponseWriter, r *http.Request) {
	if !a.localAuth(r) {
		a.unauthorizedLocal(w, r, "local.rules")
		return
	}
	if a.rules == nil {
		writeJSON(w, 200, map[string]any{"enabled": false})
		return
	}
	f := a.rules.Firing()
	writeJSON(w, 200, map[string]any{"enabled": true, "rules": a.rules.Status(), "counters": a.rules.Counters(), "firing": f})
}

// localRulesEvaluate serves POST /v1/rules/evaluate: a dry run that replays
// recorded messages against a rule set (inline, or the loaded one) in a fresh
// engine. Nothing is persisted, ingested or executed.
func (a *Agent) localRulesEvaluate(w http.ResponseWriter, r *http.Request) {
	if !a.localAuth(r) {
		a.unauthorizedLocal(w, r, "local.rules_evaluate")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, rulesEvalBodyMax)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	var in struct {
		Rules    json.RawMessage `json:"rules"`
		Start    time.Time       `json:"start"`
		Messages []rules.Message `json:"messages"`
	}
	if err := d.Decode(&in); err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	var rs *rules.RuleSet
	if t := bytes.TrimSpace(in.Rules); len(t) > 0 && string(t) != "null" {
		src := []byte(t)
		if src[0] == '"' {
			var s string
			if json.Unmarshal(src, &s) != nil {
				writeJSON(w, 400, map[string]string{"error": "rules must be an object or a string"})
				return
			}
			src = []byte(s)
		}
		var errs rules.ConfigErrors
		if rs, errs = rules.Parse(src); errs != nil {
			writeJSON(w, 400, map[string]any{"error": "invalid rules", "errors": errs})
			return
		}
	} else if a.rulesSet != nil {
		rs = a.rulesSet
	} else {
		writeJSON(w, 400, map[string]string{"error": "no rules supplied and none loaded (set rules_file)"})
		return
	}
	if in.Start.IsZero() {
		in.Start = time.Now().UTC()
	}
	rep, err := rules.Replay(rs, in.Start, in.Messages)
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"dry_run": true, "report": rep})
}
