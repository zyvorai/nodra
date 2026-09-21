// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package opcua

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestResolveSecurityNoneDefault(t *testing.T) {
	mat, err := resolveSecurity(SecurityConfig{})
	if err != nil {
		t.Fatalf("None default: %v", err)
	}
	if mat.PolicyURI != securityPolicyNone || mat.Mode != messageSecurityModeNone {
		t.Fatalf("got policy=%q mode=%d", mat.PolicyURI, mat.Mode)
	}
	mat, err = resolveSecurity(SecurityConfig{SecurityPolicy: "None"})
	if err != nil {
		t.Fatalf("explicit None: %v", err)
	}
	if mat.PolicyURI != securityPolicyNone {
		t.Fatalf("got policy %q", mat.PolicyURI)
	}
}

func TestResolveSecurityBasic256RequiresCerts(t *testing.T) {
	_, err := resolveSecurity(SecurityConfig{SecurityPolicy: "Basic256Sha256"})
	if err == nil {
		t.Fatal("expected error when Basic256Sha256 has no cert paths")
	}
	msg := err.Error()
	for _, want := range []string{"client_cert_path", "client_key_path", "server_cert_path", "Basic256Sha256"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error %q missing %q", msg, want)
		}
	}
}

func TestResolveSecurityBasic256RejectsUnparseableCerts(t *testing.T) {
	dir := t.TempDir()
	cert := filepath.Join(dir, "client.pem")
	key := filepath.Join(dir, "client.key")
	server := filepath.Join(dir, "server.pem")
	for _, p := range []string{cert, key, server} {
		if err := os.WriteFile(p, []byte("not-a-real-pem\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	_, err := resolveSecurity(SecurityConfig{
		SecurityPolicy: "Basic256Sha256",
		SecurityMode:   "SignAndEncrypt",
		ClientCertPath: cert,
		ClientKeyPath:  key,
		ServerCertPath: server,
	})
	if err == nil {
		t.Fatal("expected a key-material error")
	}
	if !strings.Contains(err.Error(), "Basic256Sha256 key material") {
		t.Fatalf("got %v", err)
	}
}

func TestResolveSecurityBasic256WithRealCerts(t *testing.T) {
	if !basic256CryptoAvailable() {
		t.Skip("Basic256Sha256 channel crypto is not built in")
	}
	certPath, keyPath, serverCertPath := writeTestCertFiles(t, t.TempDir())

	for _, tc := range []struct {
		mode     string
		wantMode int32
	}{
		{"", messageSecurityModeSignAndEncrypt}, // default
		{"Sign", messageSecurityModeSign},
		{"SignAndEncrypt", messageSecurityModeSignAndEncrypt},
	} {
		mat, err := resolveSecurity(SecurityConfig{
			SecurityPolicy: "Basic256Sha256",
			SecurityMode:   tc.mode,
			ClientCertPath: certPath,
			ClientKeyPath:  keyPath,
			ServerCertPath: serverCertPath,
		})
		if err != nil {
			t.Fatalf("security_mode %q: %v", tc.mode, err)
		}
		if mat.PolicyURI != securityPolicyBasic256Sha256 {
			t.Fatalf("security_mode %q: policy %q", tc.mode, mat.PolicyURI)
		}
		if mat.Mode != tc.wantMode {
			t.Fatalf("security_mode %q: mode %d, want %d", tc.mode, mat.Mode, tc.wantMode)
		}
		if mat.crypto == nil || mat.crypto.clientKey == nil || mat.crypto.serverKey == nil {
			t.Fatalf("security_mode %q: key material was not parsed", tc.mode)
		}
	}
}

func TestClientBasic256WithoutCertsFails(t *testing.T) {
	cli := &Client{
		Endpoint: "opc.tcp://127.0.0.1:1/nodra",
		Timeout:  time.Second,
		Security: SecurityConfig{SecurityPolicy: "Basic256Sha256"},
	}
	_, err := cli.Read(context.Background(), []NodeID{{Namespace: 0, Numeric: 2258}})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "client_cert_path") {
		t.Fatalf("want actionable cert error, got %v", err)
	}
}

func TestNewPollerRejectsBasic256WithoutCerts(t *testing.T) {
	raw, _ := json.Marshal(PollerConfig{
		Endpoint: "opc.tcp://plc.local:4840", NodeIDs: []string{"ns=2;i=1"}, Topic: "t",
		SecurityPolicy: "Basic256Sha256",
	})
	_, err := NewPoller("plc", raw)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "client_cert_path") {
		t.Fatalf("got %v", err)
	}
}

func TestNewPollerAcceptsBasic256WithCerts(t *testing.T) {
	certPath, keyPath, serverCertPath := writeTestCertFiles(t, t.TempDir())
	raw, _ := json.Marshal(PollerConfig{
		Endpoint: "opc.tcp://plc.local:4840", NodeIDs: []string{"ns=2;i=1"}, Topic: "t",
		SecurityPolicy: "Basic256Sha256", SecurityMode: "Sign",
		ClientCertPath: certPath, ClientKeyPath: keyPath, ServerCertPath: serverCertPath,
	})
	c, err := NewPoller("plc", raw)
	if err != nil {
		t.Fatal(err)
	}
	cli := c.(*Poller).cli.(*Client)
	if cli.Security.SecurityPolicy != "Basic256Sha256" || cli.Security.SecurityMode != "Sign" {
		t.Fatalf("Security=%+v", cli.Security)
	}
}

func TestNewPollerAcceptsNoneSecurity(t *testing.T) {
	raw, _ := json.Marshal(PollerConfig{
		Endpoint: "opc.tcp://plc.local:4840", NodeIDs: []string{"ns=2;i=1"}, Topic: "t",
		SecurityPolicy: "None",
	})
	c, err := NewPoller("plc", raw)
	if err != nil {
		t.Fatal(err)
	}
	p := c.(*Poller)
	cli := p.cli.(*Client)
	if cli.Security.SecurityPolicy != "None" {
		t.Fatalf("SecurityPolicy=%q", cli.Security.SecurityPolicy)
	}
}
