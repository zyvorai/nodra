package pki

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"testing"
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
