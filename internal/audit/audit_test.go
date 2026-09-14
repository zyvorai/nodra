// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package audit

import (
	"testing"
	"time"
)

func TestCursorRoundTrip(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Nanosecond)
	c := encodeCursor(now, "aud_abc123")
	gotT, gotID, ok := decodeCursor(c)
	if !ok {
		t.Fatal("decode failed")
	}
	if !gotT.Equal(now) || gotID != "aud_abc123" {
		t.Fatalf("got t=%s id=%s, want t=%s id=aud_abc123", gotT, gotID, now)
	}
}

func TestDecodeCursorInvalid(t *testing.T) {
	if _, _, ok := decodeCursor("garbage"); ok {
		t.Fatal("expected decode failure for garbage input")
	}
	if _, _, ok := decodeCursor(""); ok {
		t.Fatal("expected decode failure for empty input")
	}
}

func TestOpenUnsupportedDriver(t *testing.T) {
	if _, err := Open("mongo", t.TempDir(), "", Options{}); err == nil {
		t.Fatal("expected error for unsupported driver")
	}
}

func TestOpenFileDriver(t *testing.T) {
	s, err := Open("file", t.TempDir(), "", Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, ok := s.(*FileLog); !ok {
		t.Fatalf("expected *FileLog, got %T", s)
	}
}

func TestOpenDefaultDriverIsFile(t *testing.T) {
	s, err := Open("", t.TempDir(), "", Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, ok := s.(*FileLog); !ok {
		t.Fatalf("expected *FileLog, got %T", s)
	}
}
