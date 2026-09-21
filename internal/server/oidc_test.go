// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func oidcB64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func oidcBigIntBytes(e int) []byte {
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

func signTestJWT(t *testing.T, key *rsa.PrivateKey, header, payload map[string]any) string {
	t.Helper()
	hb, _ := json.Marshal(header)
	pb, _ := json.Marshal(payload)
	signingInput := oidcB64(hb) + "." + oidcB64(pb)
	digest := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return signingInput + "." + oidcB64(sig)
}

// fakeIdP stands up a minimal OIDC provider: discovery document, JWKS, and
// a token endpoint that always returns a valid ID token for whatever
// code/nonce the test drove oidcLogin/oidcCallback with.
type fakeIdP struct {
	*httptest.Server
	key      *rsa.PrivateKey
	kid      string
	sub, aud string
}

func newFakeIdP(t *testing.T, clientID string) *fakeIdP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeIdP{key: key, kid: "k1", sub: "u1", aud: clientID}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"issuer":                 f.Server.URL,
			"authorization_endpoint": f.Server.URL + "/authorize",
			"token_endpoint":         f.Server.URL + "/token",
			"jwks_uri":               f.Server.URL + "/jwks",
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"keys": []map[string]string{{
				"kty": "RSA", "kid": f.kid,
				"n": oidcB64(key.PublicKey.N.Bytes()),
				"e": oidcB64(oidcBigIntBytes(key.PublicKey.E)),
			}},
		})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		nonce := r.FormValue("code") // the test uses the "code" as a stand-in carrying the nonce
		tok := signTestJWT(t, key,
			map[string]any{"alg": "RS256", "kid": f.kid},
			map[string]any{
				"iss": f.Server.URL, "aud": f.aud, "sub": f.sub,
				"exp": time.Now().Add(time.Hour).Unix(), "nonce": nonce,
				"email": "u1@example.com", "groups": []string{"nodra-admins"},
			})
		_ = json.NewEncoder(w).Encode(map[string]string{"id_token": tok})
	})
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Server.Close)
	return f
}

// noRedirectClient never follows redirects, so the test can inspect each
// hop's Location header itself.
var noRedirectClient = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func TestOIDCLoginFlow(t *testing.T) {
	idp := newFakeIdP(t, "nodra-console")
	s, err := New(Config{
		DataDir: t.TempDir(), AdminToken: "adm", AdminUser: "admin", EnrollmentToken: "enroll",
		OIDCIssuerURL: idp.Server.URL, OIDCClientID: "nodra-console", OIDCRedirectURL: "http://cp.local/callback",
		OIDCAdminGroup: "nodra-admins", OIDCViewerGroup: "nodra-viewers",
	})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	// 1. GET /api/v1/auth/oidc/login redirects to the IdP's authorize
	// endpoint, carrying a state param.
	resp, err := noRedirectClient.Get(ts.URL + "/api/v1/auth/oidc/login")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("oidc login: status=%d", resp.StatusCode)
	}
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	state := loc.Query().Get("state")
	nonce := loc.Query().Get("nonce")
	if state == "" || nonce == "" {
		t.Fatalf("missing state/nonce in redirect: %s", loc)
	}
	if !strings.HasPrefix(loc.String(), idp.Server.URL) {
		t.Fatalf("expected redirect to the IdP, got %s", loc)
	}

	// 2. Simulate the IdP redirecting back with a "code" — the fake token
	// endpoint above treats the code as the nonce to embed in the ID token,
	// so passing the real nonce here proves oidcCallback's nonce check
	// actually round-trips correctly.
	callbackURL := fmt.Sprintf("%s/api/v1/auth/oidc/callback?code=%s&state=%s", ts.URL, url.QueryEscape(nonce), url.QueryEscape(state))
	resp, err = noRedirectClient.Get(callbackURL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("oidc callback: status=%d", resp.StatusCode)
	}
	dest := resp.Header.Get("Location")
	if !strings.Contains(dest, "oidc_token=") || !strings.Contains(dest, "role=admin") {
		t.Fatalf("unexpected callback redirect: %s", dest)
	}
	frag := strings.TrimPrefix(dest, "/#")
	values, err := url.ParseQuery(frag)
	if err != nil {
		t.Fatal(err)
	}
	tok := values.Get("oidc_token")
	if tok == "" || tok == "adm" {
		t.Fatalf("expected a minted session token, not the static admin secret, got %q", tok)
	}
	if values.Get("expires_at") == "" {
		t.Fatalf("expected expires_at in fragment: %s", dest)
	}

	// 3. The minted token authenticates against /api/v1/auth/me.
	c := testClient{ts.URL, "adm", t}
	code, b := c.req(http.MethodGet, "/api/v1/auth/me", nil, tok)
	if code != 200 {
		t.Fatalf("auth/me: %d %s", code, b)
	}
	var me struct {
		Authenticated bool `json:"authenticated"`
		Session       bool `json:"session"`
	}
	if err := json.Unmarshal(b, &me); err != nil || !me.Authenticated || !me.Session {
		t.Fatalf("expected authenticated session: %v (%s)", err, b)
	}

	// 4. A durable "oidc.login" audit entry was recorded.
	code, b = c.req(http.MethodGet, "/api/v1/audit?limit=100", nil, "adm")
	if code != 200 {
		t.Fatalf("audit list: %d %s", code, b)
	}
	var audit struct {
		Entries []map[string]any `json:"entries"`
	}
	if err := json.Unmarshal(b, &audit); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range audit.Entries {
		if e["action"] == "oidc.login" && e["result"] == "ok" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected an oidc.login audit entry, got %+v", audit.Entries)
	}

	// 5. A state replayed a second time is rejected (single-use).
	resp, err = noRedirectClient.Get(callbackURL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected state replay to be rejected with 400, got %d", resp.StatusCode)
	}
}

func TestOIDCLoginDisabledByDefault(t *testing.T) {
	_, ts, _ := newTestServer(t)
	resp, err := noRedirectClient.Get(ts.URL + "/api/v1/auth/oidc/login")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when OIDC is not configured, got %d", resp.StatusCode)
	}
}

func TestOIDCLoginDeniesUnmatchedGroup(t *testing.T) {
	idp := newFakeIdP(t, "nodra-console")
	idp.sub = "u2"
	s, err := New(Config{
		DataDir: t.TempDir(), AdminToken: "adm", AdminUser: "admin", EnrollmentToken: "enroll",
		OIDCIssuerURL: idp.Server.URL, OIDCClientID: "nodra-console", OIDCRedirectURL: "http://cp.local/callback",
		OIDCAdminGroup: "some-other-admin-group", OIDCViewerGroup: "some-other-viewer-group",
	})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	resp, err := noRedirectClient.Get(ts.URL + "/api/v1/auth/oidc/login")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	loc, _ := url.Parse(resp.Header.Get("Location"))
	state, nonce := loc.Query().Get("state"), loc.Query().Get("nonce")
	callbackURL := fmt.Sprintf("%s/api/v1/auth/oidc/callback?code=%s&state=%s", ts.URL, url.QueryEscape(nonce), url.QueryEscape(state))
	resp, err = noRedirectClient.Get(callbackURL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 for an unmatched group, got %d", resp.StatusCode)
	}
}
