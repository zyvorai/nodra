// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package oidc

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"
)

func testKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// signJWT builds a compact RS256 JWT (or, for negative tests, a token that
// merely claims a different alg without actually being validly signed for
// it) from the given header/payload maps.
func signJWT(t *testing.T, key *rsa.PrivateKey, header, payload map[string]any) string {
	t.Helper()
	hb, err := json.Marshal(header)
	if err != nil {
		t.Fatal(err)
	}
	pb, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	signingInput := b64(hb) + "." + b64(pb)
	digest := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return signingInput + "." + b64(sig)
}

func testJWKS(kid string, key *rsa.PrivateKey) *JWKSet {
	return &JWKSet{Keys: []jwk{{
		Kty: "RSA",
		Kid: kid,
		N:   b64(key.PublicKey.N.Bytes()),
		E:   b64(bigIntBytes(key.PublicKey.E)),
	}}}
}

func bigIntBytes(e int) []byte {
	// Minimal big-endian encoding of e, matching how a JWKS "e" field is
	// encoded (e.g. 65537 -> 0x01 0x00 0x01, base64url "AQAB").
	if e == 0 {
		return []byte{0}
	}
	var b []byte
	for e > 0 {
		b = append([]byte{byte(e & 0xff)}, b...)
		e >>= 8
	}
	return b
}

func TestVerifyIDTokenHappyPath(t *testing.T) {
	key := testKey(t)
	now := time.Now()
	tok := signJWT(t, key,
		map[string]any{"alg": "RS256", "kid": "k1"},
		map[string]any{
			"iss": "https://idp.example.com", "aud": "nodra-console",
			"exp": now.Add(time.Hour).Unix(), "sub": "u1", "nonce": "n1",
			"groups": []string{"nodra-admins"},
		})
	claims, err := VerifyIDToken(tok, testJWKS("k1", key), "https://idp.example.com", "nodra-console", "n1", now)
	if err != nil {
		t.Fatal(err)
	}
	if claims.String("sub") != "u1" {
		t.Fatalf("sub=%q", claims.String("sub"))
	}
	role := RoleForClaims(claims, Config{AdminGroup: "nodra-admins", ViewerGroup: "nodra-viewers"})
	if role != "admin" {
		t.Fatalf("role=%q, want admin", role)
	}
}

func TestVerifyIDTokenRejectsExpired(t *testing.T) {
	key := testKey(t)
	now := time.Now()
	tok := signJWT(t, key,
		map[string]any{"alg": "RS256", "kid": "k1"},
		map[string]any{"iss": "iss1", "aud": "aud1", "exp": now.Add(-time.Hour).Unix()})
	if _, err := VerifyIDToken(tok, testJWKS("k1", key), "iss1", "aud1", "", now); err == nil {
		t.Fatal("expected expired-token error")
	}
}

func TestVerifyIDTokenRejectsWrongIssuer(t *testing.T) {
	key := testKey(t)
	now := time.Now()
	tok := signJWT(t, key,
		map[string]any{"alg": "RS256", "kid": "k1"},
		map[string]any{"iss": "attacker-issuer", "aud": "aud1", "exp": now.Add(time.Hour).Unix()})
	if _, err := VerifyIDToken(tok, testJWKS("k1", key), "iss1", "aud1", "", now); err == nil {
		t.Fatal("expected issuer-mismatch error")
	}
}

func TestVerifyIDTokenRejectsWrongAudience(t *testing.T) {
	key := testKey(t)
	now := time.Now()
	tok := signJWT(t, key,
		map[string]any{"alg": "RS256", "kid": "k1"},
		map[string]any{"iss": "iss1", "aud": "someone-else", "exp": now.Add(time.Hour).Unix()})
	if _, err := VerifyIDToken(tok, testJWKS("k1", key), "iss1", "aud1", "", now); err == nil {
		t.Fatal("expected audience-mismatch error")
	}
}

func TestVerifyIDTokenRejectsTamperedSignature(t *testing.T) {
	key := testKey(t)
	now := time.Now()
	tok := signJWT(t, key,
		map[string]any{"alg": "RS256", "kid": "k1"},
		map[string]any{"iss": "iss1", "aud": "aud1", "exp": now.Add(time.Hour).Unix()})
	// Flip a character in the middle of the signature (not the trailing
	// edge, where a raw-base64url character can encode unused padding
	// bits that don't affect the decoded bytes at all).
	mid := len(tok) - 20
	flip := byte('A')
	if tok[mid] == 'A' {
		flip = 'B'
	}
	tampered := tok[:mid] + string(flip) + tok[mid+1:]
	if _, err := VerifyIDToken(tampered, testJWKS("k1", key), "iss1", "aud1", "", now); err == nil {
		t.Fatal("expected signature verification to fail")
	}
}

func TestVerifyIDTokenRejectsAlgNone(t *testing.T) {
	key := testKey(t)
	now := time.Now()
	tok := signJWT(t, key,
		map[string]any{"alg": "none", "kid": "k1"},
		map[string]any{"iss": "iss1", "aud": "aud1", "exp": now.Add(time.Hour).Unix()})
	_, err := VerifyIDToken(tok, testJWKS("k1", key), "iss1", "aud1", "", now)
	if err == nil {
		t.Fatal("expected alg:none to be rejected")
	}
}

func TestVerifyIDTokenRejectsHS256(t *testing.T) {
	key := testKey(t)
	now := time.Now()
	tok := signJWT(t, key,
		map[string]any{"alg": "HS256", "kid": "k1"},
		map[string]any{"iss": "iss1", "aud": "aud1", "exp": now.Add(time.Hour).Unix()})
	_, err := VerifyIDToken(tok, testJWKS("k1", key), "iss1", "aud1", "", now)
	if err == nil {
		t.Fatal("expected alg:HS256 to be rejected")
	}
}

func TestVerifyIDTokenRejectsUnknownKid(t *testing.T) {
	key := testKey(t)
	now := time.Now()
	tok := signJWT(t, key,
		map[string]any{"alg": "RS256", "kid": "no-such-key"},
		map[string]any{"iss": "iss1", "aud": "aud1", "exp": now.Add(time.Hour).Unix()})
	if _, err := VerifyIDToken(tok, testJWKS("k1", key), "iss1", "aud1", "", now); err == nil {
		t.Fatal("expected unknown-kid error")
	}
}

func TestRoleForClaimsViewer(t *testing.T) {
	claims := Claims{"groups": []any{"nodra-viewers"}}
	role := RoleForClaims(claims, Config{AdminGroup: "nodra-admins", ViewerGroup: "nodra-viewers"})
	if role != "viewer" {
		t.Fatalf("role=%q, want viewer", role)
	}
}

func TestRoleForClaimsNoMatch(t *testing.T) {
	claims := Claims{"groups": []any{"someone-else"}}
	role := RoleForClaims(claims, Config{AdminGroup: "nodra-admins", ViewerGroup: "nodra-viewers"})
	if role != "" {
		t.Fatalf("role=%q, want empty", role)
	}
}
