// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package opcua

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// --- shared test certificates -----------------------------------------
//
// RSA-2048 keygen is slow enough that generating a fresh pair per test
// dominates the package's runtime, so the client and server identities are
// generated once and reused.

var (
	testCertOnce                     sync.Once
	testClientKey, testServerKey     *rsa.PrivateKey
	testClientCertDER, testServerDER []byte
)

func testIdentities(t *testing.T) (clientKey *rsa.PrivateKey, clientDER []byte, serverKey *rsa.PrivateKey, serverDER []byte) {
	t.Helper()
	testCertOnce.Do(func() {
		testClientKey, testClientCertDER = mustSelfSigned("urn:nodra:opcua-client")
		testServerKey, testServerDER = mustSelfSigned("urn:nodra:opcua-test-server")
	})
	return testClientKey, testClientCertDER, testServerKey, testServerDER
}

func mustSelfSigned(commonName string) (*rsa.PrivateKey, []byte) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		panic(err)
	}
	return key, der
}

// writeTestCertFiles writes a client cert/key pair and a server cert into
// dir in PEM form and returns their paths, matching what SecurityConfig
// expects on disk.
func writeTestCertFiles(t *testing.T, dir string) (certPath, keyPath, serverCertPath string) {
	t.Helper()
	clientKey, clientDER, _, serverDER := testIdentities(t)
	certPath = filepath.Join(dir, "client.pem")
	keyPath = filepath.Join(dir, "client.key")
	serverCertPath = filepath.Join(dir, "server.pem")
	writePEMFile(t, certPath, "CERTIFICATE", clientDER)
	writePEMFile(t, keyPath, "RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(clientKey))
	writePEMFile(t, serverCertPath, "CERTIFICATE", serverDER)
	return certPath, keyPath, serverCertPath
}

func writePEMFile(t *testing.T, path, blockType string, der []byte) {
	t.Helper()
	buf := pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: der})
	if err := os.WriteFile(path, buf, 0o600); err != nil {
		t.Fatal(err)
	}
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// --- known-answer tests ------------------------------------------------

// TestHMACSHA256KnownVectors uses RFC 4231 test cases 1 and 2.
func TestHMACSHA256KnownVectors(t *testing.T) {
	cases := []struct {
		name, key, data, want string
	}{
		{
			name: "rfc4231-case1",
			key:  "0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b",
			data: hex.EncodeToString([]byte("Hi There")),
			want: "b0344c61d8db38535ca8afceaf0bf12b881dc200c9833da726e9376c2e32cff7",
		},
		{
			name: "rfc4231-case2",
			key:  hex.EncodeToString([]byte("Jefe")),
			data: hex.EncodeToString([]byte("what do ya want for nothing?")),
			want: "5bdcc146bf60754e6a042426089575c75a003f089d2739839dec58b964ec3843",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := hmacSHA256(mustHex(t, tc.key), mustHex(t, tc.data))
			if want := mustHex(t, tc.want); !bytes.Equal(got, want) {
				t.Fatalf("got %x, want %x", got, want)
			}
			if !verifyHMACSHA256(mustHex(t, tc.key), mustHex(t, tc.data), got) {
				t.Fatal("verifyHMACSHA256 rejected its own output")
			}
		})
	}
}

// TestAESCBCKnownVector uses the AES-256-CBC vectors from NIST SP 800-38A
// §F.2.5/F.2.6.
func TestAESCBCKnownVector(t *testing.T) {
	key := mustHex(t, "603deb1015ca71be2b73aef0857d77811f352c073b6108d72d9810a30914dff4")
	iv := mustHex(t, "000102030405060708090a0b0c0d0e0f")
	plain := mustHex(t,
		"6bc1bee22e409f96e93d7e117393172a"+
			"ae2d8a571e03ac9c9eb76fac45af8e51"+
			"30c81c46a35ce411e5fbc1191a0a52ef"+
			"f69f2445df4f9b17ad2b417be66c3710")
	want := mustHex(t,
		"f58c4c04d6e5f1ba779eabfb5f7bfbd6"+
			"9cfc4e967edb808d679f777bc6702c7d"+
			"39f23369a9d9bacfa530e26304231461"+
			"b2eb05e2c39be9fcda6c19078c6a9d1b")

	got, err := aesCBCEncrypt(key, iv, plain)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("ciphertext\n got %x\nwant %x", got, want)
	}
	back, err := aesCBCDecrypt(key, iv, got)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(back, plain) {
		t.Fatalf("round trip got %x, want %x", back, plain)
	}
}

// TestCertThumbprintIsSHA1 checks the thumbprint against the SHA-1 digest
// of "abc" (FIPS 180-1 appendix A) rather than recomputing it the same way
// the implementation does.
func TestCertThumbprintIsSHA1(t *testing.T) {
	got := certThumbprint([]byte("abc"))
	want := mustHex(t, "a9993e364706816aba3e25717850c26c9cd0d89d")
	if !bytes.Equal(got, want) {
		t.Fatalf("got %x, want %x", got, want)
	}
}

// TestPSHA256MatchesDefinition expands the RFC 5246 §5 P_hash construction
// by hand and compares, which checks the A(i) chaining and the
// HMAC(secret, A(i)||seed) block order rather than just that the function
// is deterministic.
func TestPSHA256MatchesDefinition(t *testing.T) {
	secret := []byte("opc-ua-secret-nonce")
	seed := []byte("opc-ua-seed-nonce")

	a1 := hmacSHA256(secret, seed)
	a2 := hmacSHA256(secret, a1)
	a3 := hmacSHA256(secret, a2)
	want := bytes.Join([][]byte{
		hmacSHA256(secret, append(append([]byte{}, a1...), seed...)),
		hmacSHA256(secret, append(append([]byte{}, a2...), seed...)),
		hmacSHA256(secret, append(append([]byte{}, a3...), seed...)),
	}, nil)

	if got := pSHA256(secret, seed, len(want)); !bytes.Equal(got, want) {
		t.Fatalf("full expansion\n got %x\nwant %x", got, want)
	}
	// A shorter request must be a prefix of the longer one.
	if got := pSHA256(secret, seed, 80); !bytes.Equal(got, want[:80]) {
		t.Fatalf("truncated expansion\n got %x\nwant %x", got, want[:80])
	}
	if got := pSHA256(secret, seed, 0); got != nil {
		t.Fatalf("zero length returned %x", got)
	}
}

func TestDeriveChannelKeys(t *testing.T) {
	clientNonce := bytes.Repeat([]byte{0x11}, basic256NonceLength)
	serverNonce := bytes.Repeat([]byte{0x22}, basic256NonceLength)
	k := deriveChannelKeys(clientNonce, serverNonce)

	for name, b := range map[string][]byte{
		"clientSigning":    k.clientSigning,
		"clientEncrypting": k.clientEncrypting,
		"serverSigning":    k.serverSigning,
		"serverEncrypting": k.serverEncrypting,
	} {
		if len(b) != basic256SymKeyLength {
			t.Fatalf("%s is %d bytes, want %d", name, len(b), basic256SymKeyLength)
		}
	}
	if len(k.clientIV) != basic256SymIVLength || len(k.serverIV) != basic256SymIVLength {
		t.Fatalf("IVs are %d/%d bytes, want %d", len(k.clientIV), len(k.serverIV), basic256SymIVLength)
	}

	// Part 6 §6.7.5: the client set is P_SHA256(serverNonce, clientNonce),
	// the server set the mirror image. Getting these backwards is the
	// classic way a secure channel fails only against real servers.
	wantClient := pSHA256(serverNonce, clientNonce, 2*basic256SymKeyLength+basic256SymIVLength)
	wantServer := pSHA256(clientNonce, serverNonce, 2*basic256SymKeyLength+basic256SymIVLength)
	if !bytes.Equal(k.clientSigning, wantClient[:32]) ||
		!bytes.Equal(k.clientEncrypting, wantClient[32:64]) ||
		!bytes.Equal(k.clientIV, wantClient[64:]) {
		t.Fatal("client key set does not match P_SHA256(serverNonce, clientNonce)")
	}
	if !bytes.Equal(k.serverSigning, wantServer[:32]) ||
		!bytes.Equal(k.serverEncrypting, wantServer[32:64]) ||
		!bytes.Equal(k.serverIV, wantServer[64:]) {
		t.Fatal("server key set does not match P_SHA256(clientNonce, serverNonce)")
	}
	if bytes.Equal(k.clientSigning, k.serverSigning) {
		t.Fatal("client and server signing keys are identical")
	}
}

func TestMessagePaddingRoundTrip(t *testing.T) {
	cases := []struct {
		blockSize, signatureSize int
		extra                    bool
	}{
		{basic256SymIVLength, basic256SignatureSize, false}, // AES-256-CBC
		{2048/8 - rsaOAEPSHA1Overhead, 2048 / 8, false},     // RSA-2048 OAEP
		{4096/8 - rsaOAEPSHA1Overhead, 4096 / 8, true},      // RSA-4096 OAEP
	}
	for _, tc := range cases {
		for dataLen := 0; dataLen < 600; dataLen++ {
			data := bytes.Repeat([]byte{0xAB}, dataLen)
			pad := messagePadding(dataLen, tc.signatureSize, tc.blockSize, tc.extra)
			if total := dataLen + len(pad) + tc.signatureSize; total%tc.blockSize != 0 {
				t.Fatalf("block=%d extra=%v dataLen=%d: padded total %d is not a multiple of the block", tc.blockSize, tc.extra, dataLen, total)
			}
			back, err := stripMessagePadding(append(append([]byte{}, data...), pad...), tc.extra)
			if err != nil {
				t.Fatalf("block=%d extra=%v dataLen=%d: %v", tc.blockSize, tc.extra, dataLen, err)
			}
			if !bytes.Equal(back, data) {
				t.Fatalf("block=%d extra=%v dataLen=%d: round trip returned %d bytes", tc.blockSize, tc.extra, dataLen, len(back))
			}
		}
	}
}

func TestStripMessagePaddingRejectsCorruption(t *testing.T) {
	data := bytes.Repeat([]byte{0x01}, 20)
	padded := append(append([]byte{}, data...), messagePadding(len(data), basic256SignatureSize, basic256SymIVLength, false)...)
	if len(padded) == len(data) {
		t.Skip("this length needs no padding")
	}
	corrupt := append([]byte{}, padded...)
	corrupt[len(corrupt)-2] ^= 0xFF
	if _, err := stripMessagePadding(corrupt, false); err == nil {
		t.Fatal("expected an error for a corrupted padding byte")
	}
	if _, err := stripMessagePadding([]byte{200}, false); err == nil {
		t.Fatal("expected an error when PaddingSize exceeds the message")
	}
}

func TestRSAOAEPRoundTrip(t *testing.T) {
	_, _, serverKey, _ := testIdentities(t)
	blockSize := serverKey.Size() - rsaOAEPSHA1Overhead

	plain := bytes.Repeat([]byte{0x5A}, blockSize*3)
	ct, err := rsaOAEPEncrypt(&serverKey.PublicKey, plain)
	if err != nil {
		t.Fatal(err)
	}
	if want := 3 * serverKey.Size(); len(ct) != want {
		t.Fatalf("ciphertext is %d bytes, want %d", len(ct), want)
	}
	back, err := rsaOAEPDecrypt(serverKey, ct)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(back, plain) {
		t.Fatal("OAEP round trip changed the plaintext")
	}
	if _, err := rsaOAEPEncrypt(&serverKey.PublicKey, plain[:blockSize-1]); err == nil {
		t.Fatal("expected an error for a partial plaintext block")
	}
}

func TestRSASignVerifySHA256(t *testing.T) {
	clientKey, _, _, _ := testIdentities(t)
	data := []byte("OpenSecureChannelRequest")
	sig, err := rsaSignSHA256(clientKey, data)
	if err != nil {
		t.Fatal(err)
	}
	if len(sig) != clientKey.Size() {
		t.Fatalf("signature is %d bytes, want %d", len(sig), clientKey.Size())
	}
	if err := rsaVerifySHA256(&clientKey.PublicKey, data, sig); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if err := rsaVerifySHA256(&clientKey.PublicKey, append(data, '!'), sig); err == nil {
		t.Fatal("expected verification to fail for modified data")
	}
}

func TestNewChannelCryptoValidation(t *testing.T) {
	clientKey, clientDER, serverKey, serverDER := testIdentities(t)
	clientPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: clientDER})
	clientKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(clientKey)})
	serverPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: serverDER})

	c, err := newChannelCrypto(clientPEM, clientKeyPEM, serverPEM)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(c.clientCertDER, clientDER) || !bytes.Equal(c.serverCertDER, serverDER) {
		t.Fatal("parsed DER does not match the input certificates")
	}
	if c.serverKey.N.Cmp(serverKey.N) != 0 {
		t.Fatal("server public key does not match the server certificate")
	}

	// PKCS#8 and bare DER are accepted too.
	pkcs8, err := x509.MarshalPKCS8PrivateKey(clientKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := newChannelCrypto(clientDER, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8}), serverDER); err != nil {
		t.Fatalf("PKCS#8 key / bare DER certs: %v", err)
	}

	// A key that belongs to a different certificate must be rejected.
	if _, err := newChannelCrypto(clientPEM, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(serverKey)}), serverPEM); err == nil {
		t.Fatal("expected a mismatched key/certificate pair to be rejected")
	}
	if _, err := newChannelCrypto([]byte("not-a-pem"), clientKeyPEM, serverPEM); err == nil {
		t.Fatal("expected a garbage certificate to be rejected")
	}
}
