// Package ota defines Nodra's contract with an external Zyvor OTA agent.
//
// Nodra owns transport, durable delivery, desired/reported state and status
// propagation. The OTA agent owns device-specific staging, activation, reboot,
// health verification, commit and rollback.
package ota

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// ArtifactType describes a payload managed by the OTA agent.
type ArtifactType string

const (
	ArtifactFirmware      ArtifactType = "firmware"
	ArtifactBSP           ArtifactType = "bsp"
	ArtifactKernel        ArtifactType = "kernel"
	ArtifactDeviceTree    ArtifactType = "device-tree"
	ArtifactBootloader    ArtifactType = "bootloader"
	ArtifactRootFS        ArtifactType = "rootfs"
	ArtifactContainer     ArtifactType = "container"
	ArtifactConfiguration ArtifactType = "configuration"
	ArtifactMCUFirmware   ArtifactType = "mcu-firmware"
	ArtifactAIModel       ArtifactType = "ai-model"
)

// Slot identifies an A/B system slot.
type Slot string

const (
	SlotA Slot = "A"
	SlotB Slot = "B"
)

// State is the durable OTA lifecycle state reported by the device.
type State string

const (
	StatePending         State = "pending"
	StateDownloading     State = "downloading"
	StateDownloaded      State = "downloaded"
	StateVerifying       State = "verifying"
	StateStaged          State = "staged"
	StateActivating      State = "activating"
	StateRebooting       State = "rebooting"
	StateHealthCheck     State = "health-check"
	StateCommitted       State = "committed"
	StateFailed          State = "failed"
	StateRollbackPending State = "rollback-pending"
	StateRolledBack      State = "rolled-back"
	StateCancelled       State = "cancelled"
)

// Manifest is the immutable description of an OTA artifact.
type Manifest struct {
	ArtifactID        string       `json:"artifact_id"`
	Version           string       `json:"version"`
	Type              ArtifactType `json:"type"`
	Architecture      string       `json:"architecture"`
	Board             string       `json:"board,omitempty"`
	SizeBytes         int64        `json:"size_bytes"`
	SHA256            string       `json:"sha256"`
	Signature         string       `json:"signature"`
	SBOM              string       `json:"sbom,omitempty"`
	BundleFormat      string       `json:"bundle_format,omitempty"`
	MinimumBootloader string       `json:"minimum_bootloader,omitempty"`
	RollbackSupported bool         `json:"rollback_supported"`
}

// Policy contains per-device execution policy. Fleet owns fleet-wide batching,
// canaries and rollout orchestration.
type Policy struct {
	RebootRequired    bool          `json:"reboot_required"`
	HealthTimeout     time.Duration `json:"health_timeout"`
	RollbackOnFailure bool          `json:"rollback_on_failure"`
	RequireABSlots    bool          `json:"require_ab_slots"`
}

// Request is the desired OTA state delivered to a device.
type Request struct {
	UpdateID string   `json:"update_id"`
	DeviceID string   `json:"device_id"`
	SiteID   string   `json:"site_id,omitempty"`
	Manifest Manifest `json:"manifest"`
	Policy   Policy   `json:"policy"`
}

// Status is reported by the OTA agent and forwarded by Nodra.
type Status struct {
	UpdateID        string    `json:"update_id"`
	DeviceID        string    `json:"device_id"`
	State           State     `json:"state"`
	ProgressPercent int       `json:"progress_percent"`
	ActiveSlot      Slot      `json:"active_slot,omitempty"`
	TargetSlot      Slot      `json:"target_slot,omitempty"`
	CurrentVersion  string    `json:"current_version,omitempty"`
	TargetVersion   string    `json:"target_version,omitempty"`
	LastError       string    `json:"last_error,omitempty"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// Capability is inventory advertised by a device/OTA agent.
type Capability struct {
	Supported      bool           `json:"supported"`
	Backend        string         `json:"backend,omitempty"`
	Architecture   string         `json:"architecture"`
	Board          string         `json:"board,omitempty"`
	ActiveSlot     Slot           `json:"active_slot,omitempty"`
	AvailableSlots []Slot         `json:"available_slots,omitempty"`
	ArtifactTypes  []ArtifactType `json:"artifact_types,omitempty"`
	DiskFreeBytes  int64          `json:"disk_free_bytes,omitempty"`
}

// Validate performs transport-level validation. Device-specific compatibility
// checks remain the responsibility of the OTA agent.
func (r Request) Validate() error {
	if strings.TrimSpace(r.UpdateID) == "" {
		return errors.New("ota: update_id is required")
	}
	if strings.TrimSpace(r.DeviceID) == "" {
		return errors.New("ota: device_id is required")
	}
	if err := r.Manifest.Validate(); err != nil {
		return err
	}
	if r.Policy.HealthTimeout < 0 {
		return errors.New("ota: health_timeout cannot be negative")
	}
	return nil
}

// Validate checks fields required before an artifact can be accepted for OTA.
func (m Manifest) Validate() error {
	if strings.TrimSpace(m.ArtifactID) == "" {
		return errors.New("ota: artifact_id is required")
	}
	if strings.TrimSpace(m.Version) == "" {
		return errors.New("ota: version is required")
	}
	if !validArtifactType(m.Type) {
		return fmt.Errorf("ota: unsupported artifact type %q", m.Type)
	}
	if strings.TrimSpace(m.Architecture) == "" {
		return errors.New("ota: architecture is required")
	}
	if m.SizeBytes <= 0 {
		return errors.New("ota: size_bytes must be positive")
	}
	if !validSHA256(m.SHA256) {
		return errors.New("ota: sha256 must be 64 hexadecimal characters")
	}
	if strings.TrimSpace(m.Signature) == "" {
		return errors.New("ota: signature is required")
	}
	return nil
}

func validArtifactType(t ArtifactType) bool {
	switch t {
	case ArtifactFirmware, ArtifactBSP, ArtifactKernel, ArtifactDeviceTree,
		ArtifactBootloader, ArtifactRootFS, ArtifactContainer,
		ArtifactConfiguration, ArtifactMCUFirmware, ArtifactAIModel:
		return true
	default:
		return false
	}
}

func validSHA256(v string) bool {
	if len(v) != 64 {
		return false
	}
	for _, c := range v {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}
