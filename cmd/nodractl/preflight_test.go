// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPreflightAndBundle(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			w.Write([]byte(`{"status":"ok"}`))
		case "/readyz":
			w.Write([]byte(`{"status":"ready"}`))
		case "/api/v1/version":
			w.Write([]byte(`{"version":"0.2.2"}`))
		case "/api/v1/overview":
			if r.Header.Get("Authorization") != "Bearer adm" {
				w.WriteHeader(401)
				return
			}
			w.Write([]byte(`{"sites":1,"dead_letters":0,"open_alerts":0}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer ts.Close()
	c := client{base: ts.URL, token: "adm", http: ts.Client()}
	if err := preflight(c, true); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "bundle")
	if err := supportBundle(c, []string{"--out", dir}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "healthz.json"))
	if err != nil || !strings.Contains(string(b), "ok") {
		t.Fatalf("health bundle: %v %s", err, b)
	}
	if _, err := os.ReadFile(filepath.Join(dir, "overview.json")); err != nil {
		t.Fatal(err)
	}
}
