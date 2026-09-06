// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package telemetry

import (
	"fmt"
	"sync/atomic"
)

type Metrics struct {
	Events           atomic.Uint64
	Deliveries       atomic.Uint64
	DeliveryFailures atomic.Uint64
	Heartbeats       atomic.Uint64
	Enrollments      atomic.Uint64
}

func (m *Metrics) Prometheus() string {
	return fmt.Sprintf(`# HELP nodra_events_total Events accepted by Nodra.\n# TYPE nodra_events_total counter\nnodra_events_total %d\n# HELP nodra_deliveries_total Successful route deliveries.\n# TYPE nodra_deliveries_total counter\nnodra_deliveries_total %d\n# HELP nodra_delivery_failures_total Failed route delivery attempts.\n# TYPE nodra_delivery_failures_total counter\nnodra_delivery_failures_total %d\n# HELP nodra_heartbeats_total Agent heartbeats.\n# TYPE nodra_heartbeats_total counter\nnodra_heartbeats_total %d\n# HELP nodra_enrollments_total Edge site enrollments.\n# TYPE nodra_enrollments_total counter\nnodra_enrollments_total %d\n`, m.Events.Load(), m.Deliveries.Load(), m.DeliveryFailures.Load(), m.Heartbeats.Load(), m.Enrollments.Load())
}
