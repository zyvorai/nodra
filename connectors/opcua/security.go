// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package opcua

import (
	"fmt"
	"os"
	"strings"
)

// securityPolicyBasic256Sha256 is the Part 7 URI for Basic256Sha256.
// securityPolicyNone lives in client.go (shared with the None wire path).
const securityPolicyBasic256Sha256 = "http://opcfoundation.org/UA/SecurityPolicy#Basic256Sha256"

// MessageSecurityMode values (Part 4).
const (
	messageSecurityModeNone           int32 = 1
	messageSecurityModeSign           int32 = 2
	messageSecurityModeSignAndEncrypt int32 = 3
)

// SecurityConfig selects the OPC-UA channel security policy and optional
// certificate paths. Empty / "None" keeps the existing anonymous
// SecurityPolicy#None path. "Basic256Sha256" is accepted as configuration
// scaffolding: selection is wired through dial, but establishing a real
// Sign/SignAndEncrypt secure channel still needs RSA-OAEP, HMAC-SHA256 key
// derivation, and message crypto that this build does not ship — see
// resolveSecurity.
type SecurityConfig struct {
	SecurityPolicy string `json:"security_policy,omitempty"` // ""|"None"|"Basic256Sha256"
	SecurityMode   string `json:"security_mode,omitempty"`   // ""|"None"|"Sign"|"SignAndEncrypt"
	ClientCertPath string `json:"client_cert_path,omitempty"`
	ClientKeyPath  string `json:"client_key_path,omitempty"`
	ServerCertPath string `json:"server_cert_path,omitempty"`
}

// securityMaterial is the resolved channel security for one dial. For
// SecurityPolicy None, PolicyURI is securityPolicyNone and Mode is None;
// CertPEM fields stay empty. Basic256Sha256 currently never returns a usable
// material — resolveSecurity fails first with an actionable error.
type securityMaterial struct {
	PolicyURI     string
	Mode          int32
	ClientCertPEM []byte
	ClientKeyPEM  []byte
	ServerCertPEM []byte
}

// basic256CryptoAvailable reports whether this build can run the
// Basic256Sha256 secure-channel crypto (asymmetric OpenSecureChannel,
// symmetric MSG signing/encryption). Scaffolding ships without it so CI
// never needs real application-instance certificates.
func basic256CryptoAvailable() bool { return false }

// resolveSecurity validates cfg and returns material for SecurityPolicy None,
// or a clear error when Basic256Sha256 is selected without certs / without
// channel crypto support.
func resolveSecurity(cfg SecurityConfig) (*securityMaterial, error) {
	policy := strings.TrimSpace(cfg.SecurityPolicy)
	if policy == "" {
		policy = "None"
	}
	modeName := strings.TrimSpace(cfg.SecurityMode)

	if strings.EqualFold(policy, "None") {
		if modeName != "" && !strings.EqualFold(modeName, "None") {
			return nil, fmt.Errorf("opcua: security_mode %q is incompatible with security_policy None (use None or omit)", cfg.SecurityMode)
		}
		return &securityMaterial{
			PolicyURI: securityPolicyNone,
			Mode:      messageSecurityModeNone,
		}, nil
	}

	if !strings.EqualFold(policy, "Basic256Sha256") {
		return nil, fmt.Errorf("opcua: unsupported security_policy %q (supported: None, Basic256Sha256)", cfg.SecurityPolicy)
	}

	if modeName == "" {
		modeName = "SignAndEncrypt"
	}
	var mode int32
	switch {
	case strings.EqualFold(modeName, "Sign"):
		mode = messageSecurityModeSign
	case strings.EqualFold(modeName, "SignAndEncrypt"):
		mode = messageSecurityModeSignAndEncrypt
	case strings.EqualFold(modeName, "None"):
		return nil, fmt.Errorf("opcua: security_mode None is incompatible with security_policy Basic256Sha256 (use Sign or SignAndEncrypt)")
	default:
		return nil, fmt.Errorf("opcua: unsupported security_mode %q (supported: None, Sign, SignAndEncrypt)", cfg.SecurityMode)
	}

	missing := missingCertPaths(cfg)
	if len(missing) > 0 {
		return nil, fmt.Errorf("opcua: Basic256Sha256 requires %s (set them in connector config or Client.Security); without application-instance certificates the secure channel cannot be opened", strings.Join(missing, ", "))
	}

	clientCert, err := os.ReadFile(cfg.ClientCertPath)
	if err != nil {
		return nil, fmt.Errorf("opcua: Basic256Sha256 client_cert_path %q: %w", cfg.ClientCertPath, err)
	}
	clientKey, err := os.ReadFile(cfg.ClientKeyPath)
	if err != nil {
		return nil, fmt.Errorf("opcua: Basic256Sha256 client_key_path %q: %w", cfg.ClientKeyPath, err)
	}
	serverCert, err := os.ReadFile(cfg.ServerCertPath)
	if err != nil {
		return nil, fmt.Errorf("opcua: Basic256Sha256 server_cert_path %q: %w", cfg.ServerCertPath, err)
	}

	if !basic256CryptoAvailable() {
		return nil, fmt.Errorf("opcua: Basic256Sha256 channel crypto is not available in this build (need RSA-OAEP encrypt/decrypt, PKCS#1/PSS signing, and HMAC-SHA256 key derivation); certs were readable at client_cert_path/client_key_path/server_cert_path but the secure channel cannot be established yet — see ROADMAP.md")
	}

	return &securityMaterial{
		PolicyURI:     securityPolicyBasic256Sha256,
		Mode:          mode,
		ClientCertPEM: clientCert,
		ClientKeyPEM:  clientKey,
		ServerCertPEM: serverCert,
	}, nil
}

func missingCertPaths(cfg SecurityConfig) []string {
	var missing []string
	if strings.TrimSpace(cfg.ClientCertPath) == "" {
		missing = append(missing, "client_cert_path")
	}
	if strings.TrimSpace(cfg.ClientKeyPath) == "" {
		missing = append(missing, "client_key_path")
	}
	if strings.TrimSpace(cfg.ServerCertPath) == "" {
		missing = append(missing, "server_cert_path")
	}
	return missing
}
