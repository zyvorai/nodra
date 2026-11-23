// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-2.0

package ota

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"sort"
	"time"
)

// Rollout limits.
const (
	MaxWaves          = 10
	MaxRolloutDevices = 100000
)

// Wave is one cumulative stage: after it is released, Percent of the target
// devices have been offered the update. Soak is how long the whole wave must
// stay committed and healthy before the next wave is released.
type Wave struct {
	Percent int           `json:"percent"`
	Soak    time.Duration `json:"soak"`
}

// HealthPolicy turns what the agent already reports (OTA status plus the
// heartbeat metrics goroutines, heap_bytes and spool_risk) into a verdict.
type HealthPolicy struct {
	// HeartbeatTimeout: a device that has begun activating must keep sending
	// heartbeats at least this often (0 disables the check).
	HeartbeatTimeout time.Duration `json:"heartbeat_timeout"`
	// MaxFailurePercent: halt when more than this percentage of the released
	// devices are unhealthy. 0 means a single unhealthy device halts.
	MaxFailurePercent int     `json:"max_failure_percent"`
	MaxGoroutines     float64 `json:"max_goroutines,omitempty"`
	MaxHeapBytes      float64 `json:"max_heap_bytes,omitempty"`
	HaltOnSpoolRisk   bool    `json:"halt_on_spool_critical,omitempty"`
}

// RolloutPlan is the immutable description of a staged rollout.
type RolloutPlan struct {
	ID           string       `json:"id"`
	Waves        []Wave       `json:"waves"`
	Health       HealthPolicy `json:"health"`
	AutoRollback bool         `json:"auto_rollback"`
}

// Validate checks the plan shape.
func (p RolloutPlan) Validate() error {
	if p.ID == "" {
		return errors.New("ota: rollout id is required")
	}
	if len(p.Waves) == 0 || len(p.Waves) > MaxWaves {
		return fmt.Errorf("ota: rollout needs 1..%d waves", MaxWaves)
	}
	prev := 0
	for i, w := range p.Waves {
		if w.Percent <= prev || w.Percent > 100 {
			return fmt.Errorf("ota: wave %d percent must be increasing and within 1..100", i)
		}
		if w.Soak < 0 || w.Soak > 7*24*time.Hour {
			return fmt.Errorf("ota: wave %d soak out of range", i)
		}
		prev = w.Percent
	}
	if prev != 100 {
		return errors.New("ota: last wave must be 100 percent")
	}
	h := p.Health
	if h.MaxFailurePercent < 0 || h.MaxFailurePercent > 100 || h.HeartbeatTimeout < 0 || h.MaxGoroutines < 0 || h.MaxHeapBytes < 0 {
		return errors.New("ota: invalid health policy")
	}
	return nil
}

// RolloutStatus is the rollout lifecycle.
type RolloutStatus string

const (
	RolloutPlanned    RolloutStatus = "planned"
	RolloutRunning    RolloutStatus = "running"
	RolloutCompleted  RolloutStatus = "completed"
	RolloutHalted     RolloutStatus = "halted"
	RolloutRolledBack RolloutStatus = "rolled_back"
)

// DeviceReport is one device's latest signal: OTA state from its status
// report and the heartbeat time and metrics map the agent sends.
type DeviceReport struct {
	DeviceID      string         `json:"device_id"`
	State         State          `json:"state"`
	LastHeartbeat time.Time      `json:"last_heartbeat"`
	Metrics       map[string]any `json:"metrics,omitempty"`
}

// Decision is what the controller wants the caller to do now. The controller
// never touches devices itself.
type Decision struct {
	Action   string   `json:"action"` // none, release, halt, rollback, complete
	Release  []string `json:"release,omitempty"`
	Rollback []string `json:"rollback,omitempty"`
	Reason   string   `json:"reason,omitempty"`
}

// Rollout is a persistable staged-rollout state machine. All time comes from
// the now argument, so it is deterministic under a fake clock, and the struct
// round-trips through JSON so a restart resumes where it stopped.
type Rollout struct {
	Plan         RolloutPlan   `json:"plan"`
	Status       RolloutStatus `json:"status"`
	Order        []string      `json:"order"` // devices in release order (stable hash order)
	Wave         int           `json:"wave"`  // index of the last released wave, -1 before start
	Released     int           `json:"released"`
	WaveStarted  time.Time     `json:"wave_started"`
	CommittedAt  time.Time     `json:"committed_at,omitzero"` // when the whole released set was first seen committed
	HaltReason   string        `json:"halt_reason,omitempty"`
	RollbackSent []string      `json:"rollback_sent,omitempty"`
}

// NewRollout fixes the release order: devices are sorted by
// sha256(planID|deviceID), so the canary set is stable for a plan, unrelated
// to registration order, and differs between plans.
func NewRollout(plan RolloutPlan, deviceIDs []string) (*Rollout, error) {
	if err := plan.Validate(); err != nil {
		return nil, err
	}
	if len(deviceIDs) == 0 || len(deviceIDs) > MaxRolloutDevices {
		return nil, fmt.Errorf("ota: rollout needs 1..%d devices", MaxRolloutDevices)
	}
	seen := map[string]bool{}
	ids := make([]string, 0, len(deviceIDs))
	for _, id := range deviceIDs {
		if id == "" {
			return nil, errors.New("ota: empty device id")
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	key := func(id string) uint64 {
		h := sha256.Sum256([]byte(plan.ID + "\x00" + id))
		return binary.BigEndian.Uint64(h[:8])
	}
	sort.Slice(ids, func(i, j int) bool {
		ki, kj := key(ids[i]), key(ids[j])
		if ki != kj {
			return ki < kj
		}
		return ids[i] < ids[j]
	})
	return &Rollout{Plan: plan, Status: RolloutPlanned, Order: ids, Wave: -1}, nil
}

func (r *Rollout) waveSize(i int) int {
	n := (r.Plan.Waves[i].Percent*len(r.Order) + 99) / 100
	return max(1, min(n, len(r.Order)))
}

// Start releases the first (canary) wave.
func (r *Rollout) Start(now time.Time) (Decision, error) {
	if r.Status != RolloutPlanned {
		return Decision{}, fmt.Errorf("ota: rollout already %s", r.Status)
	}
	r.Status = RolloutRunning
	return r.release(0, now), nil
}

func (r *Rollout) release(wave int, now time.Time) Decision {
	n := r.waveSize(wave)
	d := Decision{Action: "release", Release: append([]string(nil), r.Order[r.Released:n]...)}
	r.Wave, r.Released, r.WaveStarted, r.CommittedAt = wave, n, now, time.Time{}
	return d
}

// Unhealthy reports whether one device's signals show a regression, with the
// reason. Heartbeat and metric checks apply only once a device has started
// activating, so an idle or still-downloading device is never blamed.
func (h HealthPolicy) Unhealthy(rep DeviceReport, now time.Time) (bool, string) {
	switch rep.State {
	case StateFailed, StateRollbackPending, StateRolledBack, StateCancelled:
		return true, "ota state " + string(rep.State)
	case StateActivating, StateRebooting, StateHealthCheck, StateCommitted:
	default:
		return false, ""
	}
	if h.HeartbeatTimeout > 0 && now.Sub(rep.LastHeartbeat) > h.HeartbeatTimeout {
		return true, "heartbeat stale"
	}
	if rep.State != StateHealthCheck && rep.State != StateCommitted {
		return false, ""
	}
	if v, ok := number(rep.Metrics["goroutines"]); ok && h.MaxGoroutines > 0 && v > h.MaxGoroutines {
		return true, "goroutines above limit"
	}
	if v, ok := number(rep.Metrics["heap_bytes"]); ok && h.MaxHeapBytes > 0 && v > h.MaxHeapBytes {
		return true, "heap above limit"
	}
	if s, ok := rep.Metrics["spool_risk"].(string); ok && h.HaltOnSpoolRisk && s == "critical" {
		return true, "spool risk critical"
	}
	return false, ""
}

func number(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case uint64:
		return float64(n), true
	}
	return 0, false
}

// Tick advances the rollout given the latest reports (reports for devices
// outside the rollout are ignored; a released device without a report counts
// as unhealthy once HeartbeatTimeout has passed since its wave started).
func (r *Rollout) Tick(now time.Time, reports []DeviceReport) Decision {
	switch r.Status {
	case RolloutHalted:
		return r.rollbackStep(reports)
	case RolloutRunning:
	default:
		return Decision{Action: "none"}
	}
	byID := make(map[string]DeviceReport, len(reports))
	for _, rep := range reports {
		byID[rep.DeviceID] = rep
	}
	if r.WaveStarted.After(now) { // clock stepped back
		r.WaveStarted = now
	}
	if r.CommittedAt.After(now) {
		r.CommittedAt = now
	}
	released := r.Order[:r.Released]
	bad, committed := 0, 0
	var firstReason string
	for _, id := range released {
		rep, ok := byID[id]
		if !ok {
			if r.Plan.Health.HeartbeatTimeout > 0 && now.Sub(r.WaveStarted) > r.Plan.Health.HeartbeatTimeout {
				bad++
				if firstReason == "" {
					firstReason = id + ": no report"
				}
			}
			continue
		}
		if u, why := r.Plan.Health.Unhealthy(rep, now); u {
			bad++
			if firstReason == "" {
				firstReason = id + ": " + why
			}
		} else if rep.State == StateCommitted {
			committed++
		}
	}
	if bad > 0 && bad*100 > r.Plan.Health.MaxFailurePercent*len(released) {
		return r.halt(fmt.Sprintf("%d of %d released devices unhealthy (limit %d%%); first: %s", bad, len(released), r.Plan.Health.MaxFailurePercent, firstReason), reports)
	}
	if committed < len(released) {
		r.CommittedAt = time.Time{}
		return Decision{Action: "none"}
	}
	if r.CommittedAt.IsZero() {
		r.CommittedAt = now
	}
	if now.Sub(r.CommittedAt) < r.Plan.Waves[r.Wave].Soak {
		return Decision{Action: "none"}
	}
	if r.Wave == len(r.Plan.Waves)-1 {
		r.Status = RolloutCompleted
		return Decision{Action: "complete"}
	}
	return r.release(r.Wave+1, now)
}

func (r *Rollout) halt(reason string, reports []DeviceReport) Decision {
	r.Status, r.HaltReason = RolloutHalted, reason
	if !r.Plan.AutoRollback {
		return Decision{Action: "halt", Reason: reason}
	}
	d := r.rollbackStep(reports)
	d.Action, d.Reason = "halt", reason
	return d
}

// rollbackStep lists released devices that still need a rollback request:
// those holding the new version or mid-install, not ones that never started
// (they are simply withdrawn) or that already rolled back or failed on their
// own. Once nothing is left the rollout becomes rolled_back.
func (r *Rollout) rollbackStep(reports []DeviceReport) Decision {
	if !r.Plan.AutoRollback {
		return Decision{Action: "none"}
	}
	byID := map[string]DeviceReport{}
	for _, rep := range reports {
		byID[rep.DeviceID] = rep
	}
	sent := map[string]bool{}
	for _, id := range r.RollbackSent {
		sent[id] = true
	}
	var need []string
	open := false
	for _, id := range r.Order[:r.Released] {
		switch byID[id].State {
		case StateActivating, StateRebooting, StateHealthCheck, StateCommitted:
			open = true
			if !sent[id] {
				need = append(need, id)
			}
		case StateRollbackPending:
			open = true
		}
	}
	r.RollbackSent = append(r.RollbackSent, need...)
	if len(need) == 0 && !open {
		r.Status = RolloutRolledBack
		return Decision{Action: "none"}
	}
	if len(need) == 0 {
		return Decision{Action: "none"}
	}
	return Decision{Action: "rollback", Rollback: need}
}
