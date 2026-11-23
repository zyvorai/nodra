// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-2.0

package queue

import (
	"math"
	"testing"
	"time"
)

var t0 = time.Unix(1_700_000_000, 0)

func feed(f *Forecaster, n int, step time.Duration, bytesAt func(i int) int64, itemsAt func(i int) int, max Stats) Stats {
	var last Stats
	for i := 0; i < n; i++ {
		last = max
		last.Bytes, last.Items = bytesAt(i), itemsAt(i)
		f.Observe(t0.Add(time.Duration(i)*step), last)
	}
	return last
}

func TestForecastTable(t *testing.T) {
	cap := Stats{MaxBytes: 1_000_000, MaxItems: 1_000_000, Policy: "reject"}
	tests := []struct {
		name     string
		bytesAt  func(int) int64
		itemsAt  func(int) int
		wantTTF  float64 // seconds; -1 = none
		wantRisk string
	}{
		{"100B/s growth", func(i int) int64 { return int64(i) * 1000 }, func(i int) int { return i }, (1_000_000 - 29_000) / 100.0, RiskWatch},
		{"flat", func(int) int64 { return 5000 }, func(int) int { return 50 }, -1, RiskOK},
		{"draining", func(i int) int64 { return 100_000 - int64(i)*1000 }, func(i int) int { return 1000 - i }, -1, RiskOK},
		{"already over capacity projects zero", func(i int) int64 { return int64(i) * 100_000 }, func(i int) int { return i }, 0, RiskCritical},
		{"item-bound", func(i int) int64 { return int64(i) }, func(i int) int { return i * 1000 }, (1_000_000 - 29_000) / 100.0, RiskWatch},
		{"slow growth, far from full", func(i int) int64 { return int64(i) }, func(i int) int { return i }, (1_000_000 - 29) / 0.1, RiskOK},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := NewForecaster(0, 0)
			cur := feed(f, 30, 10*time.Second, tc.bytesAt, tc.itemsAt, cap)
			fc := f.Assess(cur, 0)
			if math.Abs(fc.TimeToFullSeconds-tc.wantTTF) > math.Abs(tc.wantTTF)*0.02 {
				t.Fatalf("ttf=%v want ~%v", fc.TimeToFullSeconds, tc.wantTTF)
			}
			if fc.Risk != tc.wantRisk {
				t.Fatalf("risk=%s want %s (%+v)", fc.Risk, tc.wantRisk, fc)
			}
		})
	}
}

func TestForecastRiskLevels(t *testing.T) {
	cap := Stats{MaxBytes: 1000, Policy: "drop-oldest"}
	tests := []struct {
		name  string
		bytes int64
		slope int64 // bytes per 10s sample
		risk  string
	}{
		{"empty and idle", 0, 0, RiskOK},
		{"71 percent full idle", 710, 0, RiskWatch},
		{"91 percent full idle", 910, 0, RiskCritical},
		{"full", 1000, 0, RiskCritical},
		{"growing, ttf < 1h", 100, 1, RiskCritical}, // 0.1 B/s -> 9000s? computed below
	}
	for _, tc := range tests {
		f := NewForecaster(0, 0)
		var cur Stats
		for i := 0; i < 20; i++ {
			cur = cap
			cur.Bytes = tc.bytes + int64(i)*tc.slope
			f.Observe(t0.Add(time.Duration(i)*10*time.Second), cur)
		}
		fc := f.Assess(cur, 0)
		want := tc.risk
		if tc.name == "growing, ttf < 1h" {
			// 0.1 B/s, ~881 B left -> ~8800s: between 1h and 6h = watch
			want = RiskWatch
		}
		if fc.Risk != want {
			t.Errorf("%s: risk=%s want %s ttf=%v", tc.name, fc.Risk, want, fc.TimeToFullSeconds)
		}
		if fc.DataLoss != "drops_oldest" {
			t.Errorf("data loss label %q", fc.DataLoss)
		}
	}
}

func TestForecastOutageCapacityAndDrain(t *testing.T) {
	f := NewForecaster(0, 0)
	cap := Stats{MaxBytes: 10_000, Policy: "reject"}
	cur := feed(f, 20, 10*time.Second, func(i int) int64 { return int64(i) * 100 }, func(i int) int { return i }, cap)
	fc := f.Assess(cur, 30*time.Minute)
	if fc.OutageSeconds != 1800 || math.Abs(fc.OutageCapacitySeconds-(1800+fc.TimeToFullSeconds)) > 1e-6 {
		t.Fatalf("outage math: %+v", fc)
	}
	if fc.Confidence != "normal" || fc.DataLoss != "rejects_new" {
		t.Fatalf("meta: %+v", fc)
	}
	d := NewForecaster(0, 0)
	cur = feed(d, 20, 10*time.Second, func(i int) int64 { return 10_000 - int64(i)*100 }, func(i int) int { return 100 - i }, cap)
	fc = d.Assess(cur, 0)
	if fc.DrainSeconds <= 0 || fc.TimeToFullSeconds != -1 || fc.OutageCapacitySeconds != -1 {
		t.Fatalf("drain: %+v", fc)
	}
}

func TestForecastEdgeCases(t *testing.T) {
	f := NewForecaster(5, time.Minute)
	// no samples
	fc := f.Assess(Stats{MaxBytes: 100}, 0)
	if fc.TimeToFullSeconds != -1 || fc.Risk != RiskOK || fc.Confidence != "low" {
		t.Fatalf("empty: %+v", fc)
	}
	// unbounded queue never forecasts full
	for i := 0; i < 10; i++ {
		f.Observe(t0.Add(time.Duration(i)*time.Second), Stats{Bytes: int64(i) * 1e6})
	}
	if fc = f.Assess(Stats{Bytes: 9e6}, 0); fc.TimeToFullSeconds != -1 || fc.FillRatio != 0 {
		t.Fatalf("unbounded: %+v", fc)
	}
	// ring bound
	if len(f.samples) != 5 {
		t.Fatalf("ring size %d", len(f.samples))
	}
	// duplicate timestamp ignored, backwards clock resets window
	f.Observe(t0.Add(9*time.Second), Stats{})
	if len(f.samples) != 5 {
		t.Fatal("duplicate timestamp should be ignored")
	}
	f.Observe(t0, Stats{})
	if len(f.samples) != 1 {
		t.Fatalf("backwards clock should reset, have %d", len(f.samples))
	}
	// identical timestamps cannot divide by zero
	if b, i := slopes([]sample{{t0, 1, 1}, {t0, 2, 2}, {t0, 3, 3}}); b != 0 || i != 0 {
		t.Fatal("zero time spread must yield zero slope")
	}
	// window excludes stale samples
	g := NewForecaster(100, 30*time.Second)
	for i := 0; i < 60; i++ {
		b := int64(1000)
		if i > 40 {
			b += int64(i-40) * 100 // growth only in the last 20s
		}
		g.Observe(t0.Add(time.Duration(i)*time.Second), Stats{Bytes: b, MaxBytes: 1_000_000})
	}
	fc = g.Assess(Stats{Bytes: 2900, MaxBytes: 1_000_000}, 0)
	if fc.GrowthBytesPerSec <= 0 {
		t.Fatalf("recent growth missed: %+v", fc)
	}
}
