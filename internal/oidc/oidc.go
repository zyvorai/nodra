// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

// Package oidc implements just enough of the OIDC authorization-code flow
// to let the Nodra console offer "Sign in with SSO" as a third way to
// obtain the existing admin/viewer bearer tokens — not a user directory,
// not multi-tenant orgs. ID token verification is RS256-only (the
// overwhelming majority of real-world IdPs), hand-rolled against stdlib
// crypto rather than pulling in a JWT/JOSE dependency: RS256 verification
// is a small, well-bounded amount of code, unlike this project's other
// "shell out instead of hand-roll" calls for genuinely complex trust
// machinery (e.g. cosign/sigstore).
package oidc

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Discovery is the subset of an OIDC provider's
// /.well-known/openid-configuration document this package needs.
type Discovery struct {
	Issuer                string `json:"issuer"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	JWKSURI               string `json:"jwks_uri"`
}

// Discover fetches and decodes issuerURL's discovery document.
func Discover(ctx context.Context, client *http.Client, issuerURL string) (*Discovery, error) {
	u := strings.TrimRight(issuerURL, "/") + "/.well-known/openid-configuration"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("oidc: fetch discovery document: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("oidc: discovery document returned HTTP %d", resp.StatusCode)
	}
	var d Discovery
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&d); err != nil {
		return nil, fmt.Errorf("oidc: decode discovery document: %w", err)
	}
	if d.AuthorizationEndpoint == "" || d.TokenEndpoint == "" || d.JWKSURI == "" {
		return nil, errors.New("oidc: discovery document is missing required endpoints")
	}
	return &d, nil
}

// jwk is one entry in a JWK Set. Only the RSA fields this package supports
// (RS256 verification) are decoded.
type jwk struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	N   string `json:"n"`
	E   string `json:"e"`
	Alg string `json:"alg"`
	Use string `json:"use"`
}

// JWKSet is a fetched JSON Web Key Set.
type JWKSet struct {
	Keys []jwk `json:"keys"`
}

// FetchJWKS fetches and decodes the JWK Set at jwksURI.
func FetchJWKS(ctx context.Context, client *http.Client, jwksURI string) (*JWKSet, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, jwksURI, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("oidc: fetch JWKS: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("oidc: JWKS endpoint returned HTTP %d", resp.StatusCode)
	}
	var set JWKSet
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&set); err != nil {
		return nil, fmt.Errorf("oidc: decode JWKS: %w", err)
	}
	return &set, nil
}

func (s *JWKSet) rsaKey(kid string) (*rsa.PublicKey, error) {
	if s == nil {
		return nil, errors.New("oidc: no JWKS loaded")
	}
	for _, k := range s.Keys {
		if k.Kid != kid {
			continue
		}
		if k.Kty != "RSA" {
			return nil, fmt.Errorf("oidc: key %q is not an RSA key (kty=%q)", kid, k.Kty)
		}
		nb, err := base64.RawURLEncoding.DecodeString(k.N)
		if err != nil {
			return nil, fmt.Errorf("oidc: key %q has invalid modulus: %w", kid, err)
		}
		eb, err := base64.RawURLEncoding.DecodeString(k.E)
		if err != nil {
			return nil, fmt.Errorf("oidc: key %q has invalid exponent: %w", kid, err)
		}
		e := new(big.Int).SetBytes(eb)
		return &rsa.PublicKey{N: new(big.Int).SetBytes(nb), E: int(e.Int64())}, nil
	}
	return nil, fmt.Errorf("oidc: no JWKS key found for kid %q", kid)
}

// Claims is a decoded JWT payload.
type Claims map[string]any

// String returns claims[key] as a string, or "" if absent/not a string.
func (c Claims) String(key string) string {
	s, _ := c[key].(string)
	return s
}

// StringSlice returns claims[key] as a slice of strings. It accepts either
// a JSON array of strings or a single space-delimited string (some IdPs
// send scope-like claims that way).
func (c Claims) StringSlice(key string) []string {
	switch v := c[key].(type) {
	case []any:
		out := make([]string, 0, len(v))
		for _, e := range v {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case string:
		return strings.Fields(v)
	default:
		return nil
	}
}

func base64URLDecode(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(s)
}

// VerifyIDToken parses and verifies a compact JWT ID token: signature
// (RS256 only — "none" and any HS* algorithm are explicitly rejected, the
// classic JWT algorithm-confusion mitigation), issuer, audience, expiry,
// not-before, and (when expectedNonce is non-empty) nonce. Each failure
// mode returns a distinct, specific error.
func VerifyIDToken(rawJWT string, jwks *JWKSet, issuer, audience, expectedNonce string, now time.Time) (Claims, error) {
	parts := strings.Split(rawJWT, ".")
	if len(parts) != 3 {
		return nil, errors.New("oidc: malformed JWT (expected 3 dot-separated parts)")
	}
	headerB, payloadB, sigB := parts[0], parts[1], parts[2]

	headerJSON, err := base64URLDecode(headerB)
	if err != nil {
		return nil, fmt.Errorf("oidc: invalid JWT header encoding: %w", err)
	}
	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := json.Unmarshal(headerJSON, &header); err != nil {
		return nil, fmt.Errorf("oidc: invalid JWT header JSON: %w", err)
	}
	if header.Alg != "RS256" {
		return nil, fmt.Errorf("oidc: unsupported JWT algorithm %q (only RS256 is accepted; \"none\" and HS* are explicitly rejected)", header.Alg)
	}

	pubKey, err := jwks.rsaKey(header.Kid)
	if err != nil {
		return nil, err
	}

	sig, err := base64URLDecode(sigB)
	if err != nil {
		return nil, fmt.Errorf("oidc: invalid JWT signature encoding: %w", err)
	}
	signingInput := headerB + "." + payloadB
	digest := sha256.Sum256([]byte(signingInput))
	if err := rsa.VerifyPKCS1v15(pubKey, crypto.SHA256, digest[:], sig); err != nil {
		return nil, fmt.Errorf("oidc: JWT signature verification failed: %w", err)
	}

	payloadJSON, err := base64URLDecode(payloadB)
	if err != nil {
		return nil, fmt.Errorf("oidc: invalid JWT payload encoding: %w", err)
	}
	var claims Claims
	if err := json.Unmarshal(payloadJSON, &claims); err != nil {
		return nil, fmt.Errorf("oidc: invalid JWT payload JSON: %w", err)
	}

	if iss := claims.String("iss"); iss != issuer {
		return nil, fmt.Errorf("oidc: unexpected issuer %q (want %q)", iss, issuer)
	}
	if !audienceMatches(claims["aud"], audience) {
		return nil, fmt.Errorf("oidc: token audience does not include %q", audience)
	}
	exp, ok := claims["exp"].(float64)
	if !ok {
		return nil, errors.New("oidc: token is missing exp claim")
	}
	if now.After(time.Unix(int64(exp), 0)) {
		return nil, errors.New("oidc: token has expired")
	}
	if nbfRaw, ok := claims["nbf"]; ok {
		nbf, ok := nbfRaw.(float64)
		if !ok {
			return nil, errors.New("oidc: token has an invalid nbf claim")
		}
		if now.Before(time.Unix(int64(nbf), 0)) {
			return nil, errors.New("oidc: token is not yet valid (nbf)")
		}
	}
	if expectedNonce != "" && claims.String("nonce") != expectedNonce {
		return nil, errors.New("oidc: token nonce does not match the expected value")
	}
	return claims, nil
}

func audienceMatches(aud any, want string) bool {
	switch v := aud.(type) {
	case string:
		return v == want
	case []any:
		for _, e := range v {
			if s, ok := e.(string); ok && s == want {
				return true
			}
		}
	}
	return false
}

// Config holds the OIDC provider settings the console login flow needs.
type Config struct {
	IssuerURL    string
	ClientID     string
	ClientSecret string
	RedirectURL  string
	// GroupsClaim names the ID-token claim carrying the caller's
	// groups/roles; defaults to "groups" when empty.
	GroupsClaim string
	AdminGroup  string
	ViewerGroup string
	Scopes      []string
}

func (c Config) groupsClaim() string {
	if c.GroupsClaim == "" {
		return "groups"
	}
	return c.GroupsClaim
}

// BuildAuthURL builds the authorization-code-flow redirect URL for the
// configured provider.
func BuildAuthURL(disc *Discovery, cfg Config, state, nonce string) string {
	scope := "openid profile email"
	if len(cfg.Scopes) > 0 {
		scope = scope + " " + strings.Join(cfg.Scopes, " ")
	}
	q := url.Values{
		"response_type": {"code"},
		"client_id":     {cfg.ClientID},
		"redirect_uri":  {cfg.RedirectURL},
		"scope":         {scope},
		"state":         {state},
		"nonce":         {nonce},
	}
	return disc.AuthorizationEndpoint + "?" + q.Encode()
}

// ExchangeCode exchanges an authorization code for an ID token via the
// standard OAuth2 authorization_code grant.
func ExchangeCode(ctx context.Context, client *http.Client, disc *Discovery, cfg Config, code string) (string, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {cfg.RedirectURL},
		"client_id":     {cfg.ClientID},
		"client_secret": {cfg.ClientSecret},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, disc.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("oidc: token exchange request failed: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("oidc: token exchange returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var out struct {
		IDToken string `json:"id_token"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("oidc: decode token response: %w", err)
	}
	if out.IDToken == "" {
		return "", errors.New("oidc: token response did not contain an id_token")
	}
	return out.IDToken, nil
}

// RoleForClaims resolves "admin"/"viewer"/"" from claims's groups claim.
func RoleForClaims(claims Claims, cfg Config) string {
	groups := claims.StringSlice(cfg.groupsClaim())
	for _, g := range groups {
		if cfg.AdminGroup != "" && g == cfg.AdminGroup {
			return "admin"
		}
	}
	for _, g := range groups {
		if cfg.ViewerGroup != "" && g == cfg.ViewerGroup {
			return "viewer"
		}
	}
	return ""
}
