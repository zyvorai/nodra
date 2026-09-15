package ota

import (
	"strings"
	"testing"
	"time"
)

func validRequest() Request {
	return Request{
		UpdateID: "upd-001",
		DeviceID: "device-001",
		Manifest: Manifest{
			ArtifactID:        "edge-os-arm64-1.2.0",
			Version:           "1.2.0",
			Type:              ArtifactRootFS,
			Architecture:      "arm64",
			SizeBytes:         1024,
			SHA256:            strings.Repeat("a", 64),
			Signature:         "oci://registry.example/edge-os:1.2.0.sig",
			SBOM:              "sbom.spdx.json",
			BundleFormat:      "rauc",
			RollbackSupported: true,
		},
		Policy: Policy{
			RebootRequired:    true,
			HealthTimeout:     5 * time.Minute,
			RollbackOnFailure: true,
			RequireABSlots:    true,
		},
	}
}

func TestRequestValidate(t *testing.T) {
	if err := validRequest().Validate(); err != nil {
		t.Fatalf("valid request rejected: %v", err)
	}

	r := validRequest()
	r.Manifest.SHA256 = "bad"
	if err := r.Validate(); err == nil {
		t.Fatal("expected invalid sha256 to fail")
	}

	r = validRequest()
	r.Manifest.Signature = ""
	if err := r.Validate(); err == nil {
		t.Fatal("expected missing signature to fail")
	}
}

func TestStateTransitions(t *testing.T) {
	happyPath := []State{
		StatePending,
		StateDownloading,
		StateDownloaded,
		StateVerifying,
		StateStaged,
		StateActivating,
		StateRebooting,
		StateHealthCheck,
		StateCommitted,
	}

	for i := 0; i < len(happyPath)-1; i++ {
		if err := ValidateTransition(happyPath[i], happyPath[i+1]); err != nil {
			t.Fatalf("transition %s -> %s rejected: %v", happyPath[i], happyPath[i+1], err)
		}
	}

	if err := ValidateTransition(StatePending, StateCommitted); err == nil {
		t.Fatal("expected pending -> committed to fail")
	}

	if err := ValidateTransition(StateHealthCheck, StateRollbackPending); err != nil {
		t.Fatalf("health-check -> rollback-pending rejected: %v", err)
	}
	if err := ValidateTransition(StateRollbackPending, StateRolledBack); err != nil {
		t.Fatalf("rollback-pending -> rolled-back rejected: %v", err)
	}
}

func TestTerminal(t *testing.T) {
	for _, state := range []State{StateCommitted, StateFailed, StateRolledBack, StateCancelled} {
		if !state.Terminal() {
			t.Fatalf("expected %s to be terminal", state)
		}
	}
	if StateHealthCheck.Terminal() {
		t.Fatal("health-check must not be terminal")
	}
}
