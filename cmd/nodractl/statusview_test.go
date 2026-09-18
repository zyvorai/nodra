package main

import (
	"strings"
	"testing"
)

func TestStatusBanner(t *testing.T) {
	view := StatusView{
		Labels: [5]string{"A", "B", "C", "D", "E"},
		Features: []Feature{
			okFeature("Sites", true),
			okFeature("Kryton", false),
		},
	}
	text := stripANSI(view.Format())
	if !strings.Contains(text, "/¯¯\\") || !strings.Contains(text, "✅ OK") || !strings.Contains(text, "ℹ️  disabled") {
		t.Fatalf("banner missing expected text:\n%s", text)
	}
}
