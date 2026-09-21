// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLoginRateLimit(t *testing.T) {
	srv, err := New(Config{DataDir: t.TempDir(), AdminToken: "adm", AdminUser: "admin", AdminPassword: "secret", EnrollmentToken: "enroll"})
	if err != nil {
		t.Fatal(err)
	}
	h := srv.Handler()
	post := func() int {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/api/v1/auth/login", strings.NewReader(`{"username":"admin","password":"no"}`))
		req.RemoteAddr = "203.0.113.8:9"
		h.ServeHTTP(rr, req)
		return rr.Code
	}
	for i := 0; i < 5; i++ {
		if code := post(); code != 401 {
			t.Fatalf("attempt %d: %d", i, code)
		}
	}
	if code := post(); code != 429 {
		t.Fatalf("want 429 got %d", code)
	}
}

func TestEnrollRateLimit(t *testing.T) {
	srv, err := New(Config{DataDir: t.TempDir(), AdminToken: "adm", AdminPassword: "secret", EnrollmentToken: "enroll"})
	if err != nil {
		t.Fatal(err)
	}
	h := srv.Handler()
	post := func() int {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/api/v1/enroll", strings.NewReader(`{"name":"edge","enrollment_token":"no"}`))
		req.RemoteAddr = "203.0.113.9:9"
		h.ServeHTTP(rr, req)
		return rr.Code
	}
	for i := 0; i < 5; i++ {
		if code := post(); code != 401 {
			t.Fatalf("attempt %d: %d", i, code)
		}
	}
	if code := post(); code != 429 {
		t.Fatalf("want 429 got %d", code)
	}
}
