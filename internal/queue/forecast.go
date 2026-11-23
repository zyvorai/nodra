// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-2.0

package queue

import (
	"math"
	"sync"
	"time"
)

// Risk levels reported by a Forecast.
const (
	RiskOK       = "ok"
	RiskWatch    = "watch"
	RiskCritical = "critical"
)

// Default sampling and assessment parameters.
const (
	DefaultForecastSamples = 360 // 1h at one sample per 10s
	DefaultForecastWindow  = 15 * time.Minute
	minForecastSamples     = 3
	WatchTimeToFull        = 6 * time.Hour
	CriticalTimeToFull     = time.Hour
	WatchFill              = 0.70
	CriticalFill           = 0.90
)

type sample struct {
	at    time.Time
	bytes int64
	items int
}

// Forecaster estimates how long a bounded queue can keep absorbing data at
// the currently observed net growth rate. It is fed Stats samples; the
// regression is over net change, so while the uplink is down the slope is the
// ingest rate and while it is draining the slope is negative.
type Forecaster struct {
	mu      sync.Mutex
	samples []sample
	max     int
	window  time.Duration
}

// NewForecaster keeps up to maxSamples samples and regresses over the most
// recent window. Non-positive arguments select the defaults.
func NewForecaster(maxSamples int, window time.Duration) *Forecaster {
	if maxSamples < minForecastSamples {
		maxSamples = DefaultForecastSamples
	}
	if window <= 0 {
		window = DefaultForecastWindow
	}
	return &Forecaster{max: maxSamples, window: window}
}

// Observe records a sample. Out-of-order (non-increasing) timestamps are
// dropped so a clock step cannot corrupt the regression.
func (f *Forecaster) Observe(at time.Time, s Stats) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if n := len(f.samples); n > 0 && !at.After(f.samples[n-1].at) {
		if at.Before(f.samples[n-1].at) {
			f.samples = f.samples[:0] // clock went backwards: restart the window
		} else {
			return
		}
	}
	f.samples = append(f.samples, sample{at: at, bytes: s.Bytes, items: s.Items})
	if len(f.samples) > f.max {
		f.samples = append(f.samples[:0], f.samples[len(f.samples)-f.max:]...)
	}
}

// Forecast is a point-in-time capacity projection.
type Forecast struct {
	Items             int     `json:"items"`
	Bytes             int64   `json:"bytes"`
	MaxItems          int     `json:"max_items"`
	MaxBytes          int64   `json:"max_bytes"`
	Policy            string  `json:"policy"`
	FillRatio         float64 `json:"fill_ratio"`
	GrowthBytesPerSec float64 `json:"growth_bytes_per_sec"`
	GrowthItemsPerSec float64 `json:"growth_items_per_sec"`
	// TimeToFullSeconds is -1 when the queue is not growing (or unbounded).
	TimeToFullSeconds float64 `json:"time_to_full_seconds"`
	// DrainSeconds is the time to empty at the current (negative) rate, or -1.
	DrainSeconds float64 `json:"drain_seconds"`
	// OutageSeconds is how long the uplink has been down, when known.
	OutageSeconds float64 `json:"outage_seconds"`
	// OutageCapacitySeconds is the total WAN-down duration the queue can
	// absorb at the current rate (outage so far plus time to full), or -1.
	OutageCapacitySeconds float64 `json:"outage_capacity_seconds"`
	Risk                  string  `json:"risk"`
	DataLoss              string  `json:"data_loss"` // none|rejects_new|drops_oldest|drops_newest
	Samples               int     `json:"samples"`
	Confidence            string  `json:"confidence"` // low (<10 samples) or normal
}

// Assess projects fill time from the sampled history and the current stats.
// outage is how long the uplink has been unreachable (0 when connected).
func (f *Forecaster) Assess(cur Stats, outage time.Duration) Forecast {
	f.mu.Lock()
	win := make([]sample, 0, len(f.samples))
	if n := len(f.samples); n > 0 {
		cutoff := f.samples[n-1].at.Add(-f.window)
		for _, s := range f.samples {
			if !s.at.Before(cutoff) {
				win = append(win, s)
			}
		}
	}
	f.mu.Unlock()

	out := Forecast{Items: cur.Items, Bytes: cur.Bytes, MaxItems: cur.MaxItems, MaxBytes: cur.MaxBytes, Policy: cur.Policy,
		TimeToFullSeconds: -1, DrainSeconds: -1, OutageSeconds: outage.Seconds(), Samples: len(win), Risk: RiskOK, Confidence: "low"}
	if len(win) >= 10 {
		out.Confidence = "normal"
	}
	if cur.MaxBytes > 0 {
		out.FillRatio = float64(cur.Bytes) / float64(cur.MaxBytes)
	}
	if cur.MaxItems > 0 {
		if r := float64(cur.Items) / float64(cur.MaxItems); r > out.FillRatio {
			out.FillRatio = r
		}
	}
	switch cur.Policy {
	case "drop-oldest":
		out.DataLoss = "drops_oldest"
	case "drop-newest":
		out.DataLoss = "drops_newest"
	default:
		out.DataLoss = "rejects_new"
	}

	bps, ips := slopes(win)
	out.GrowthBytesPerSec, out.GrowthItemsPerSec = bps, ips
	ttf := math.Inf(1)
	if bps > 0 && cur.MaxBytes > 0 {
		ttf = math.Min(ttf, math.Max(float64(cur.MaxBytes-cur.Bytes), 0)/bps)
	}
	if ips > 0 && cur.MaxItems > 0 {
		ttf = math.Min(ttf, math.Max(float64(cur.MaxItems-cur.Items), 0)/ips)
	}
	if !math.IsInf(ttf, 1) {
		out.TimeToFullSeconds = ttf
	}
	out.OutageCapacitySeconds = -1
	if out.TimeToFullSeconds >= 0 {
		out.OutageCapacitySeconds = out.OutageSeconds + out.TimeToFullSeconds
	}
	if bps < 0 && cur.Bytes > 0 {
		out.DrainSeconds = float64(cur.Bytes) / -bps
	}

	switch {
	case out.FillRatio >= 1 || (out.TimeToFullSeconds >= 0 && out.TimeToFullSeconds < CriticalTimeToFull.Seconds()) || out.FillRatio >= CriticalFill:
		out.Risk = RiskCritical
	case (out.TimeToFullSeconds >= 0 && out.TimeToFullSeconds < WatchTimeToFull.Seconds()) || out.FillRatio >= WatchFill:
		out.Risk = RiskWatch
	}
	return out
}

// slopes returns least-squares bytes/s and items/s over the samples, or zeros
// when there are too few samples or no time spread.
func slopes(w []sample) (bps, ips float64) {
	if len(w) < minForecastSamples {
		return 0, 0
	}
	t0 := w[0].at
	var n, sx, sxx, sy, sxy, sz, sxz float64
	for _, s := range w {
		x := s.at.Sub(t0).Seconds()
		y, z := float64(s.bytes), float64(s.items)
		n++
		sx += x
		sxx += x * x
		sy += y
		sxy += x * y
		sz += z
		sxz += x * z
	}
	den := n*sxx - sx*sx
	if den <= 0 {
		return 0, 0
	}
	return (n*sxy - sx*sy) / den, (n*sxz - sx*sz) / den
}
