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

func TestResolveSecurityBasic256CryptoUnavailable(t *testing.T) {
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
		t.Fatal("expected crypto-unavailable error")
	}
	if !strings.Contains(err.Error(), "channel crypto is not available") {
		t.Fatalf("got %v", err)
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
