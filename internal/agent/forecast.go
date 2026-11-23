// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-2.0

package agent

import (
	"context"
	"net/http"
	"time"

	"github.com/zyvorai/nodra/internal/queue"
)

const forecastInterval = 10 * time.Second

// forecastLoop samples the spool so the capacity forecast has a growth trend.
func (a *Agent) forecastLoop(ctx context.Context) {
	defer a.wg.Done()
	t := time.NewTicker(forecastInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			a.forecast.Observe(now, a.spool.Stats())
		}
	}
}

// SpoolForecast projects when the store-and-forward spool fills at the
// currently observed net growth rate.
func (a *Agent) SpoolForecast() queue.Forecast {
	now := time.Now()
	st := a.spool.Stats()
	outage := time.Duration(0)
	if last := time.Unix(0, a.lastUplink.Load()); now.Sub(last) > 2*a.cfg.Heartbeat+5*time.Second {
		outage = now.Sub(last)
	}
	return a.forecast.Assess(st, outage)
}

func (a *Agent) localSpoolForecast(w http.ResponseWriter, r *http.Request) {
	if !a.localAuth(r) {
		a.unauthorizedLocal(w, r, "local.spool_forecast")
		return
	}
	writeJSON(w, 200, a.SpoolForecast())
}
