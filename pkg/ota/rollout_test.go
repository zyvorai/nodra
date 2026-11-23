// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-2.0

package ota

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// fakeFleet simulates devices reaching committed one tick after release.
type fakeFleet struct {
	now      time.Time
	state    map[string]State
	hb       map[string]time.Time
	metrics  map[string]map[string]any
	released map[string]bool
}

func newFleet(n int) (*fakeFleet, []string) {
	f := &fakeFleet{now: clock0, state: map[string]State{}, hb: map[string]time.Time{}, metrics: map[string]map[string]any{}, released: map[string]bool{}}
	var ids []string
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("dev-%03d", i)
		ids = append(ids, id)
		f.state[id] = StatePending
		f.hb[id] = clock0
		f.metrics[id] = map[string]any{"goroutines": 20.0, "heap_bytes": 1e7, "spool_risk": "ok"}
	}
	return f, ids
}

func (f *fakeFleet) apply(d Decision) {
	for _, id := range d.Release {
		f.released[id] = true
	}
	for _, id := range d.Rollback {
		f.state[id] = StateRollbackPending
	}
}

// step moves released devices forward one lifecycle step and refreshes heartbeats.
func (f *fakeFleet) step() {
	for id := range f.released {
		switch f.state[id] {
		case StatePending:
			f.state[id] = StateActivating
		case StateActivating:
			f.state[id] = StateHealthCheck
		case StateHealthCheck:
			f.state[id] = StateCommitted
		case StateRollbackPending:
			f.state[id] = StateRolledBack
		}
	}
	for id := range f.hb {
		f.hb[id] = f.now
	}
}

func (f *fakeFleet) reports() []DeviceReport {
	var out []DeviceReport
	for id, s := range f.state {
		out = append(out, DeviceReport{DeviceID: id, State: s, LastHeartbeat: f.hb[id], Metrics: f.metrics[id]})
	}
	return out
}

func testPlan() RolloutPlan {
	return RolloutPlan{ID: "r1", AutoRollback: true,
		Waves:  []Wave{{Percent: 5, Soak: 10 * time.Minute}, {Percent: 25, Soak: 10 * time.Minute}, {Percent: 100}},
		Health: HealthPolicy{HeartbeatTimeout: 2 * time.Minute, MaxFailurePercent: 0, MaxGoroutines: 200, MaxHeapBytes: 1e8, HaltOnSpoolRisk: true}}
}

func TestPlanValidation(t *testing.T) {
	bad := map[string]func(*RolloutPlan){
		"no id":           func(p *RolloutPlan) { p.ID = "" },
		"no waves":        func(p *RolloutPlan) { p.Waves = nil },
		"too many":        func(p *RolloutPlan) { p.Waves = make([]Wave, MaxWaves+1) },
		"not increasing":  func(p *RolloutPlan) { p.Waves = []Wave{{Percent: 50}, {Percent: 50}, {Percent: 100}} },
		"zero percent":    func(p *RolloutPlan) { p.Waves = []Wave{{Percent: 0}, {Percent: 100}} },
		"not 100":         func(p *RolloutPlan) { p.Waves = []Wave{{Percent: 10}, {Percent: 90}} },
		"over 100":        func(p *RolloutPlan) { p.Waves = []Wave{{Percent: 101}} },
		"negative soak":   func(p *RolloutPlan) { p.Waves[0].Soak = -1 },
		"huge soak":       func(p *RolloutPlan) { p.Waves[0].Soak = 365 * 24 * time.Hour },
		"bad fail pct":    func(p *RolloutPlan) { p.Health.MaxFailurePercent = 101 },
		"negative limits": func(p *RolloutPlan) { p.Health.MaxHeapBytes = -1 },
	}
	for n, fn := range bad {
		p := testPlan()
		fn(&p)
		if p.Validate() == nil {
			t.Errorf("%s accepted", n)
		}
	}
	if err := testPlan().Validate(); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRollout(testPlan(), nil); err == nil {
		t.Error("no devices accepted")
	}
	if _, err := NewRollout(testPlan(), []string{"a", ""}); err == nil {
		t.Error("empty id accepted")
	}
	if _, err := NewRollout(testPlan(), make([]string, MaxRolloutDevices+1)); err == nil {
		t.Error("oversized fleet accepted")
	}
}

func TestHappyPathStagedRollout(t *testing.T) {
	f, ids := newFleet(40)
	r, err := NewRollout(testPlan(), ids)
	if err != nil {
		t.Fatal(err)
	}
	d, err := r.Start(f.now)
	if err != nil || len(d.Release) != 2 { // ceil(5% of 40)
		t.Fatalf("canary: %v %+v", err, d)
	}
	if _, err = r.Start(f.now); err == nil {
		t.Fatal("double start")
	}
	f.apply(d)
	var waves []int
	for i := 0; i < 400 && r.Status == RolloutRunning; i++ {
		f.now = f.now.Add(time.Minute)
		f.step()
		d = r.Tick(f.now, f.reports())
		f.apply(d)
		if d.Action == "release" {
			waves = append(waves, len(f.released))
		}
	}
	if r.Status != RolloutCompleted {
		t.Fatalf("status %s (%s)", r.Status, r.HaltReason)
	}
	if len(waves) != 2 || waves[0] != 10 || waves[1] != 40 {
		t.Fatalf("cumulative waves %v", waves)
	}
	if d = r.Tick(f.now, f.reports()); d.Action != "none" {
		t.Fatal("completed rollout kept acting")
	}
}

func TestSoakIsRespected(t *testing.T) {
	f, ids := newFleet(20)
	r, _ := NewRollout(testPlan(), ids)
	f.apply(mustStart(t, r, f.now))
	for i := 0; i < 4; i++ {
		f.now = f.now.Add(time.Minute)
		f.step()
		if d := r.Tick(f.now, f.reports()); d.Action == "release" {
			t.Fatalf("released at +%dmin before the 10min soak", i+1)
		}
	}
	// committed at about +3min; soak ends about +13min
	f.now = f.now.Add(8 * time.Minute)
	f.step()
	if d := r.Tick(f.now, f.reports()); d.Action == "release" {
		t.Fatal("released before soak ended")
	}
	f.now = f.now.Add(3 * time.Minute)
	f.step()
	if d := r.Tick(f.now, f.reports()); d.Action != "release" {
		t.Fatalf("did not release after soak: %+v", d)
	}
}

func mustStart(t *testing.T, r *Rollout, now time.Time) Decision {
	t.Helper()
	d, err := r.Start(now)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestRegressionHaltsAndRollsBack(t *testing.T) {
	regress := map[string]func(*fakeFleet, string){
		"ota failed":      func(f *fakeFleet, id string) { f.state[id] = StateFailed },
		"goroutine leak":  func(f *fakeFleet, id string) { f.metrics[id]["goroutines"] = 5000.0 },
		"heap growth":     func(f *fakeFleet, id string) { f.metrics[id]["heap_bytes"] = 9e9 },
		"spool critical":  func(f *fakeFleet, id string) { f.metrics[id]["spool_risk"] = "critical" },
		"heartbeat stale": func(f *fakeFleet, id string) { f.hb[id] = f.now.Add(-time.Hour) },
	}
	for name, fn := range regress {
		t.Run(name, func(t *testing.T) {
			f, ids := newFleet(40)
			r, _ := NewRollout(testPlan(), ids)
			f.apply(mustStart(t, r, f.now))
			var victim string
			halted := false
			for i := 0; i < 200 && !halted; i++ {
				f.now = f.now.Add(time.Minute)
				f.step()
				if len(f.released) > 2 && victim == "" { // regression appears in wave 2
					for id := range f.released {
						if f.state[id] == StateHealthCheck || f.state[id] == StateActivating {
							victim = id
							break
						}
					}
					if victim != "" {
						fn(f, victim)
					}
				}
				d := r.Tick(f.now, f.reports())
				f.apply(d)
				if d.Action == "halt" {
					halted = true
					if !strings.Contains(d.Reason, "unhealthy") {
						t.Errorf("reason %q", d.Reason)
					}
					if len(f.released) != 10 {
						t.Errorf("released %d devices; halt must stop further waves", len(f.released))
					}
				}
			}
			if !halted || r.Status != RolloutHalted && r.Status != RolloutRolledBack {
				t.Fatalf("not halted: %s", r.Status)
			}
			// drive rollback to completion; no more releases ever
			for i := 0; i < 20; i++ {
				f.now = f.now.Add(time.Minute)
				f.step()
				d := r.Tick(f.now, f.reports())
				if d.Action == "release" {
					t.Fatal("released after halt")
				}
				f.apply(d)
			}
			if r.Status != RolloutRolledBack {
				t.Fatalf("status %s", r.Status)
			}
			for id := range f.released {
				if s := f.state[id]; s != StateRolledBack && s != StateFailed {
					t.Errorf("%s left in %s", id, s)
				}
			}
			for id := range f.state {
				if !f.released[id] && f.state[id] != StatePending {
					t.Errorf("unreleased %s changed", id)
				}
			}
		})
	}
}

func TestFailureToleranceAndNoAutoRollback(t *testing.T) {
	p := testPlan()
	p.Health.MaxFailurePercent = 20
	p.AutoRollback = false
	f, ids := newFleet(100)
	r, _ := NewRollout(p, ids)
	f.apply(mustStart(t, r, f.now)) // canary 5 devices
	f.now = f.now.Add(time.Minute)
	f.step()
	one := ""
	for id := range f.released {
		one = id
		break
	}
	f.state[one] = StateFailed // 1/5 = 20%, not above the limit
	if d := r.Tick(f.now, f.reports()); d.Action == "halt" {
		t.Fatalf("halted at exactly the limit: %+v", d)
	}
	n := 0
	for id := range f.released {
		if id != one && n < 1 {
			f.state[id] = StateFailed
			n++
		}
	}
	d := r.Tick(f.now, f.reports()) // 2/5 = 40% > 20%
	if d.Action != "halt" || len(d.Rollback) != 0 || r.Status != RolloutHalted {
		t.Fatalf("%+v %s", d, r.Status)
	}
	if d = r.Tick(f.now, f.reports()); d.Action != "none" {
		t.Fatal("kept acting while halted without auto rollback")
	}
}

func TestMissingReportsAndHostileReports(t *testing.T) {
	f, ids := newFleet(10)
	r, _ := NewRollout(testPlan(), ids)
	f.apply(mustStart(t, r, f.now))
	// no reports at all inside the heartbeat timeout: not (yet) a failure
	if d := r.Tick(f.now.Add(time.Minute), nil); d.Action == "halt" {
		t.Fatal("halted too early")
	}
	// hostile reports: unknown device, nil/mistyped metrics, future heartbeat, unknown state
	rep := []DeviceReport{
		{DeviceID: "ghost", State: StateFailed},
		{DeviceID: ids[0], State: State("weird"), Metrics: map[string]any{"goroutines": "many"}},
		{DeviceID: ids[1], State: StateCommitted, LastHeartbeat: f.now.Add(24 * time.Hour), Metrics: map[string]any{"heap_bytes": nil}},
	}
	for _, id := range ids {
		rep = append(rep, DeviceReport{DeviceID: id, State: StatePending, LastHeartbeat: f.now})
	}
	if d := r.Tick(f.now.Add(time.Minute), rep); d.Action == "halt" {
		t.Fatalf("hostile-but-harmless reports halted: %+v", d)
	}
	// released device silent for longer than the timeout -> unhealthy
	if d := r.Tick(f.now.Add(10*time.Minute), nil); d.Action != "halt" || !strings.Contains(d.Reason, "no report") {
		t.Fatalf("silent device: %+v", d)
	}
}

func TestClockSkewInRollout(t *testing.T) {
	f, ids := newFleet(20)
	r, _ := NewRollout(testPlan(), ids)
	f.apply(mustStart(t, r, f.now))
	for i := 0; i < 3; i++ {
		f.now = f.now.Add(time.Minute)
		f.step()
		r.Tick(f.now, f.reports())
	}
	if r.CommittedAt.IsZero() {
		t.Fatal("setup: wave not committed")
	}
	// clock jumps back a day: no panic, no early release, soak restarts from the new time
	back := f.now.Add(-24 * time.Hour)
	f.now = back
	f.step()
	if d := r.Tick(back, f.reports()); d.Action == "release" {
		t.Fatal("released on a backwards clock")
	}
	if r.CommittedAt.After(back) {
		t.Fatal("soak start left in the future")
	}
	f.now = back.Add(11 * time.Minute)
	f.step()
	if d := r.Tick(f.now, f.reports()); d.Action != "release" {
		t.Fatalf("soak did not complete after the step back: %+v", d)
	}
	// a forward jump of a year cannot skip the committed requirement
	f2, ids2 := newFleet(20)
	r2, _ := NewRollout(testPlan(), ids2)
	f2.apply(mustStart(t, r2, f2.now))
	if d := r2.Tick(f2.now.Add(365*24*time.Hour), f2.reports()); d.Action == "release" {
		t.Fatal("wave advanced without devices committing")
	}
}

func TestRolloutPersistsAcrossRestart(t *testing.T) {
	f, ids := newFleet(40)
	r, _ := NewRollout(testPlan(), ids)
	f.apply(mustStart(t, r, f.now))
	for i := 0; i < 5; i++ {
		f.now = f.now.Add(time.Minute)
		f.step()
		f.apply(r.Tick(f.now, f.reports()))
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var r2 Rollout
	if err = json.Unmarshal(b, &r2); err != nil {
		t.Fatal(err)
	}
	// the restored controller makes the same decisions as the original
	for i := 0; i < 300 && r.Status == RolloutRunning; i++ {
		f.now = f.now.Add(time.Minute)
		f.step()
		rep := f.reports()
		da, db := r.Tick(f.now, rep), r2.Tick(f.now, rep)
		ja, _ := json.Marshal(da)
		jb, _ := json.Marshal(db)
		if string(ja) != string(jb) {
			t.Fatalf("diverged: %s vs %s", ja, jb)
		}
		f.apply(da)
	}
	if r.Status != RolloutCompleted || r2.Status != RolloutCompleted {
		t.Fatalf("%s %s", r.Status, r2.Status)
	}
}

func TestCanarySelectionStableAndPlanDependent(t *testing.T) {
	_, ids := newFleet(200)
	rev := append([]string(nil), ids...)
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	a, _ := NewRollout(testPlan(), ids)
	b, _ := NewRollout(testPlan(), append(rev, ids[0])) // reversed + duplicate
	if strings.Join(a.Order, ",") != strings.Join(b.Order, ",") {
		t.Fatal("order depends on input order")
	}
	p2 := testPlan()
	p2.ID = "r2"
	c, _ := NewRollout(p2, ids)
	if strings.Join(a.Order[:10], ",") == strings.Join(c.Order[:10], ",") {
		t.Fatal("canary set identical for different plans")
	}
	if len(a.Order) != 200 {
		t.Fatal("duplicates not removed")
	}
	// a single device: canary is still one device
	one, _ := NewRollout(testPlan(), []string{"only"})
	if d, _ := one.Start(clock0); len(d.Release) != 1 {
		t.Fatalf("%+v", d)
	}
}
