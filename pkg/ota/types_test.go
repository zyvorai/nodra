package ota

import (
	"encoding/json"
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
			HealthTimeout:     "5m",
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

	r = validRequest()
	r.Policy.HealthTimeout = "not-a-duration"
	if err := r.Validate(); err == nil {
		t.Fatal("expected invalid health_timeout to fail")
	}

	r = validRequest()
	r.Policy.HealthTimeout = "-1s"
	if err := r.Validate(); err == nil {
		t.Fatal("expected negative health_timeout to fail")
	}
}

func TestPolicyHealthTimeoutDuration(t *testing.T) {
	d, err := (Policy{HealthTimeout: "90s"}).HealthTimeoutDuration()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if d != 90*time.Second {
		t.Fatalf("got %v, want 90s", d)
	}
	d, err = (Policy{}).HealthTimeoutDuration()
	if err != nil || d != 0 {
		t.Fatalf("empty timeout: got %v %v", d, err)
	}
}

func TestRequestHealthTimeoutJSON(t *testing.T) {
	raw, err := json.Marshal(validRequest())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), `"health_timeout":300000000000`) {
		t.Fatalf("health_timeout must not encode as nanoseconds: %s", raw)
	}
	if !strings.Contains(string(raw), `"health_timeout":"5m"`) {
		t.Fatalf("health_timeout missing Go duration string: %s", raw)
	}

	var decoded Request
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if err := decoded.Validate(); err != nil {
		t.Fatalf("round-trip validate: %v", err)
	}
	d, err := decoded.Policy.HealthTimeoutDuration()
	if err != nil || d != 5*time.Minute {
		t.Fatalf("round-trip duration: got %v %v", d, err)
	}
}

func TestStatusValidate(t *testing.T) {
	s := Status{
		UpdateID:        "upd-001",
		DeviceID:        "device-001",
		State:           StateDownloading,
		ProgressPercent: 42,
		ActiveSlot:      SlotA,
		TargetSlot:      SlotB,
		UpdatedAt:       time.Now().UTC(),
	}
	if err := s.Validate(); err != nil {
		t.Fatalf("valid status rejected: %v", err)
	}

	bad := s
	bad.ProgressPercent = 101
	if err := bad.Validate(); err == nil {
		t.Fatal("expected progress_percent > 100 to fail")
	}

	bad = s
	bad.ActiveSlot = "C"
	if err := bad.Validate(); err == nil {
		t.Fatal("expected invalid active_slot to fail")
	}

	bad = s
	bad.State = "unknown"
	if err := bad.Validate(); err == nil {
		t.Fatal("expected unsupported state to fail")
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

	cancelable := []State{StatePending, StateDownloading, StateDownloaded, StateVerifying, StateStaged}
	for _, state := range cancelable {
		if err := ValidateTransition(state, StateCancelled); err != nil {
			t.Fatalf("%s -> cancelled rejected: %v", state, err)
		}
		if err := ValidateTransition(state, StateFailed); err != nil {
			t.Fatalf("%s -> failed rejected: %v", state, err)
		}
	}

	if err := ValidateTransition(StateCommitted, StateFailed); err == nil {
		t.Fatal("expected committed -> failed to fail")
	}
	if err := ValidateTransition(StateDownloading, StateDownloading); err != nil {
		t.Fatalf("idempotent replay rejected: %v", err)
	}
	if err := ValidateTransition(StateActivating, StateHealthCheck); err != nil {
		t.Fatalf("non-reboot activate path rejected: %v", err)
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
