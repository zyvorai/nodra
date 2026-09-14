// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package pki

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"time"

	"github.com/zyvorai/nodra/internal/durable"
)

type CA struct {
	Cert    *x509.Certificate
	Key     *ecdsa.PrivateKey
	CertPEM []byte
}

type Signed struct {
	CertPEM   []byte
	Serial    string
	ExpiresAt time.Time
}

func EnsureCA(dir string) (*CA, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	certPath := filepath.Join(dir, "ca.crt")
	keyPath := filepath.Join(dir, "ca.key")
	cb, ce := os.ReadFile(certPath)
	kb, ke := os.ReadFile(keyPath)
	if ce == nil && ke == nil {
		return parseCA(cb, kb)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	tmpl := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "Nodra Edge CA", Organization: []string{"Zyvor"}}, NotBefore: now.Add(-5 * time.Minute), NotAfter: now.AddDate(10, 0, 0), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	cp := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	kb2, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, err
	}
	kp := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb2})
	if err = durable.AtomicWrite(certPath, cp, 0o644); err != nil {
		return nil, err
	}
	if err = durable.AtomicWrite(keyPath, kp, 0o600); err != nil {
		return nil, err
	}
	return parseCA(cp, kp)
}
func parseCA(cp, kp []byte) (*CA, error) {
	cb, _ := pem.Decode(cp)
	kb, _ := pem.Decode(kp)
	if cb == nil || kb == nil {
		return nil, errors.New("invalid CA PEM")
	}
	cert, err := x509.ParseCertificate(cb.Bytes)
	if err != nil {
		return nil, err
	}
	key, err := x509.ParseECPrivateKey(kb.Bytes)
	if err != nil {
		return nil, err
	}
	return &CA{Cert: cert, Key: key, CertPEM: cp}, nil
}

// SignCSR signs csrPEM against ca, valid for ttl (default 90 days).
// crlDistributionPoints is optional (variadic so existing call sites compile
// unchanged) — when given, it's embedded in the issued certificate's
// CRLDistributionPoints extension so clients that check CRLs directly (not
// just via the control plane's own revoked-site check) know where to fetch
// the CRL from.
func SignCSR(ca *CA, csrPEM []byte, commonName string, ttl time.Duration, crlDistributionPoints ...string) (Signed, error) {
	b, _ := pem.Decode(csrPEM)
	if b == nil {
		return Signed{}, errors.New("invalid CSR PEM")
	}
	csr, err := x509.ParseCertificateRequest(b.Bytes)
	if err != nil {
		return Signed{}, err
	}
	if err = csr.CheckSignature(); err != nil {
		return Signed{}, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return Signed{}, err
	}
	if ttl <= 0 {
		ttl = 90 * 24 * time.Hour
	}
	now := time.Now().UTC()
	exp := now.Add(ttl)
	tmpl := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: commonName, Organization: []string{"Zyvor Nodra Edge"}}, NotBefore: now.Add(-5 * time.Minute), NotAfter: exp, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, CRLDistributionPoints: crlDistributionPoints}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.Cert, csr.PublicKey, ca.Key)
	if err != nil {
		return Signed{}, err
	}
	return Signed{CertPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), Serial: serial.Text(16), ExpiresAt: exp}, nil
}

// ShouldRotate reports whether a certificate expiring at expiresAt should be
// rotated as of now, i.e. now has entered the "before" window ahead of
// expiry. A zero expiresAt (no certificate yet) never triggers rotation.
func ShouldRotate(expiresAt, now time.Time, before time.Duration) bool {
	if expiresAt.IsZero() {
		return false
	}
	return !now.Before(expiresAt.Add(-before))
}

// RevokedCert is one entry to include in a generated CRL.
type RevokedCert struct {
	Serial    *big.Int
	RevokedAt time.Time
}

// GenerateCRL issues a new CRL signed by ca listing revoked, valid from
// thisUpdate until nextUpdate. ca.Cert must carry KeyUsageCRLSign (EnsureCA's
// CA does).
func GenerateCRL(ca *CA, revoked []RevokedCert, thisUpdate, nextUpdate time.Time) ([]byte, error) {
	entries := make([]x509.RevocationListEntry, 0, len(revoked))
	for _, rc := range revoked {
		entries = append(entries, x509.RevocationListEntry{SerialNumber: rc.Serial, RevocationTime: rc.RevokedAt})
	}
	number, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 64))
	if err != nil {
		return nil, err
	}
	tmpl := &x509.RevocationList{
		Number:                    number,
		ThisUpdate:                thisUpdate,
		NextUpdate:                nextUpdate,
		RevokedCertificateEntries: entries,
	}
	der, err := x509.CreateRevocationList(rand.Reader, tmpl, ca.Cert, ca.Key)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "X509 CRL", Bytes: der}), nil
}

func NewClientCSR(commonName string) (keyPEM, csrPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	kb, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb})
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: commonName}}, key)
	if err != nil {
		return nil, nil, err
	}
	csrPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER})
	return
}
