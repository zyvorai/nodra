// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package pki

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"math/big"
	"testing"
	"time"
)

func TestIssueClientCertificate(t *testing.T) {
	ca, e := EnsureCA(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	key, csr, e := NewClientCSR("site-1")
	if e != nil {
		t.Fatal(e)
	}
	signed, e := SignCSR(ca, csr, "site-1", 0)
	if e != nil {
		t.Fatal(e)
	}
	if signed.Serial == "" {
		t.Fatal("serial")
	}
	if _, e = tls.X509KeyPair(signed.CertPEM, key); e != nil {
		t.Fatal(e)
	}
	b, _ := pem.Decode(signed.CertPEM)
	cert, e := x509.ParseCertificate(b.Bytes)
	if e != nil || cert.Subject.CommonName != "site-1" {
		t.Fatalf("%v %+v", e, cert)
	}
}

func TestSignCSRWithCRLDistributionPoint(t *testing.T) {
	ca, e := EnsureCA(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	_, csr, e := NewClientCSR("site-crl")
	if e != nil {
		t.Fatal(e)
	}
	signed, e := SignCSR(ca, csr, "site-crl", 0, "https://cp.example/api/v1/ca/crl")
	if e != nil {
		t.Fatal(e)
	}
	b, _ := pem.Decode(signed.CertPEM)
	cert, e := x509.ParseCertificate(b.Bytes)
	if e != nil {
		t.Fatal(e)
	}
	if len(cert.CRLDistributionPoints) != 1 || cert.CRLDistributionPoints[0] != "https://cp.example/api/v1/ca/crl" {
		t.Fatalf("crl distribution points = %v", cert.CRLDistributionPoints)
	}
}

func TestShouldRotate(t *testing.T) {
	now := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name      string
		expiresAt time.Time
		before    time.Duration
		want      bool
	}{
		{"zero expiry never rotates", time.Time{}, 30 * 24 * time.Hour, false},
		{"far from expiry", now.AddDate(0, 0, 60), 30 * 24 * time.Hour, false},
		{"just outside window", now.AddDate(0, 0, 31), 30 * 24 * time.Hour, false},
		{"exactly at window boundary", now.AddDate(0, 0, 30), 30 * 24 * time.Hour, true},
		{"inside window", now.AddDate(0, 0, 10), 30 * 24 * time.Hour, true},
		{"already expired", now.AddDate(0, 0, -1), 30 * 24 * time.Hour, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ShouldRotate(tc.expiresAt, now, tc.before); got != tc.want {
				t.Fatalf("ShouldRotate(%v, %v, %v) = %v, want %v", tc.expiresAt, now, tc.before, got, tc.want)
			}
		})
	}
}

func TestGenerateCRL(t *testing.T) {
	ca, e := EnsureCA(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	_, csr1, e := NewClientCSR("site-revoked")
	if e != nil {
		t.Fatal(e)
	}
	signed1, e := SignCSR(ca, csr1, "site-revoked", 0)
	if e != nil {
		t.Fatal(e)
	}
	_, csr2, e := NewClientCSR("site-kept")
	if e != nil {
		t.Fatal(e)
	}
	signed2, e := SignCSR(ca, csr2, "site-kept", 0)
	if e != nil {
		t.Fatal(e)
	}
	serial1, ok := new(big.Int).SetString(signed1.Serial, 16)
	if !ok {
		t.Fatalf("bad serial %q", signed1.Serial)
	}
	serial2, ok := new(big.Int).SetString(signed2.Serial, 16)
	if !ok {
		t.Fatalf("bad serial %q", signed2.Serial)
	}
	now := time.Now().UTC()
	crlPEM, e := GenerateCRL(ca, []RevokedCert{{Serial: serial1, RevokedAt: now}}, now, now.Add(15*time.Minute))
	if e != nil {
		t.Fatal(e)
	}
	block, _ := pem.Decode(crlPEM)
	if block == nil {
		t.Fatal("invalid CRL PEM")
	}
	crl, e := x509.ParseRevocationList(block.Bytes)
	if e != nil {
		t.Fatal(e)
	}
	if e = crl.CheckSignatureFrom(ca.Cert); e != nil {
		t.Fatalf("CRL not signed by CA: %v", e)
	}
	if len(crl.RevokedCertificateEntries) != 1 {
		t.Fatalf("want 1 revoked entry, got %d", len(crl.RevokedCertificateEntries))
	}
	if crl.RevokedCertificateEntries[0].SerialNumber.Cmp(serial1) != 0 {
		t.Fatalf("revoked serial = %s, want %s", crl.RevokedCertificateEntries[0].SerialNumber, serial1)
	}
	for _, e := range crl.RevokedCertificateEntries {
		if e.SerialNumber.Cmp(serial2) == 0 {
			t.Fatal("kept site's serial must not appear in the CRL")
		}
	}
}
