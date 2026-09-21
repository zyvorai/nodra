// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package telemetry

import (
	"strings"
	"testing"
)

func TestPrometheusUsesRealNewlines(t *testing.T) {
	var m Metrics
	m.Events.Store(3)
	out := m.Prometheus()
	if strings.Contains(out, `\n`) {
		t.Fatalf("Prometheus output still contains literal \\n escapes")
	}
	if !strings.Contains(out, "nodra_events_total 3\n") {
		t.Fatalf("missing events line: %q", out)
	}
	for _, line := range []string{"# HELP nodra_events_total", "# TYPE nodra_events_total counter", "nodra_events_total 3"} {
		if !strings.Contains(out, line) {
			t.Fatalf("missing %q in %q", line, out)
		}
	}
}
