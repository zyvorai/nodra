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
// certificate paths. Empty / "None" keeps the anonymous
// SecurityPolicy#None path. "Basic256Sha256" runs a real Sign or
// SignAndEncrypt secure channel (RSA-OAEP asymmetric OpenSecureChannel,
// P_SHA256 key derivation, AES-256-CBC + HMAC-SHA256 symmetric messages)
// and requires all three certificate paths. The user identity token stays
// anonymous under every policy.
type SecurityConfig struct {
	SecurityPolicy string `json:"security_policy,omitempty"` // ""|"None"|"Basic256Sha256"
	SecurityMode   string `json:"security_mode,omitempty"`   // ""|"None"|"Sign"|"SignAndEncrypt"
	ClientCertPath string `json:"client_cert_path,omitempty"`
	ClientKeyPath  string `json:"client_key_path,omitempty"`
	ServerCertPath string `json:"server_cert_path,omitempty"`
}

// securityMaterial is the resolved channel security for one dial. For
// SecurityPolicy None, PolicyURI is securityPolicyNone, Mode is None and
// the cert fields stay empty. For Basic256Sha256, crypto holds the parsed
// certificates and key the channel runs on.
type securityMaterial struct {
	PolicyURI     string
	Mode          int32
	ClientCertPEM []byte
	ClientKeyPEM  []byte
	ServerCertPEM []byte
	crypto        *channelCrypto
}

// basic256CryptoAvailable reports whether this build can run the
// Basic256Sha256 secure-channel crypto (asymmetric OpenSecureChannel,
// symmetric MSG signing/encryption). It does — see crypto_basic256.go and
// securechannel_basic256.go.
func basic256CryptoAvailable() bool { return true }

// resolveSecurity validates cfg and returns material for SecurityPolicy
// None, or the parsed Basic256Sha256 key material. It fails with a clear
// error when Basic256Sha256 is selected without readable, usable
// certificates.
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
		return nil, fmt.Errorf("opcua: Basic256Sha256 channel crypto is not available in this build")
	}

	crypto, err := newChannelCrypto(clientCert, clientKey, serverCert)
	if err != nil {
		return nil, fmt.Errorf("opcua: Basic256Sha256 key material: %w", err)
	}

	return &securityMaterial{
		PolicyURI:     securityPolicyBasic256Sha256,
		Mode:          mode,
		ClientCertPEM: clientCert,
		ClientKeyPEM:  clientKey,
		ServerCertPEM: serverCert,
		crypto:        crypto,
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
