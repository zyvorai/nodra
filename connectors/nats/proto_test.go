// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package nats

import (
	"bufio"
	"io"
	"strings"
	"testing"
)

// slowReader returns at most chunk bytes per Read call, forcing bufio.Reader
// (and thus readLine) to assemble a line across multiple underlying reads —
// proving the parser doesn't assume a line arrives in one read/write.
type slowReader struct {
	data  []byte
	chunk int
}

func (s *slowReader) Read(p []byte) (int, error) {
	if len(s.data) == 0 {
		return 0, io.EOF
	}
	n := s.chunk
	if n > len(p) {
		n = len(p)
	}
	if n > len(s.data) {
		n = len(s.data)
	}
	copy(p, s.data[:n])
	s.data = s.data[n:]
	return n, nil
}

func TestReadInfoAcrossSlowReads(t *testing.T) {
	r := bufio.NewReader(&slowReader{data: []byte("INFO {\"server_id\":\"x\"}\r\n"), chunk: 3})
	if err := readInfo(r); err != nil {
		t.Fatalf("readInfo: %v", err)
	}
}

func TestReadInfoRejectsUnexpectedLine(t *testing.T) {
	r := bufio.NewReader(strings.NewReader("HELLO\r\n"))
	if err := readInfo(r); err == nil {
		t.Fatal("expected error for non-INFO first line")
	}
}

func TestParseMSGHeaderThreeFields(t *testing.T) {
	subject, n, err := parseMSGHeader("factory.line1 1 11")
	if err != nil {
		t.Fatal(err)
	}
	if subject != "factory.line1" || n != 11 {
		t.Fatalf("subject=%q n=%d", subject, n)
	}
}

func TestParseMSGHeaderFourFields(t *testing.T) {
	subject, n, err := parseMSGHeader("factory.line1 1 reply.1 11")
	if err != nil {
		t.Fatal(err)
	}
	if subject != "factory.line1" || n != 11 {
		t.Fatalf("subject=%q n=%d", subject, n)
	}
}

func TestParseMSGHeaderRejectsBadByteCount(t *testing.T) {
	if _, _, err := parseMSGHeader("factory.line1 1 notanumber"); err == nil {
		t.Fatal("expected error")
	}
}

func TestParseMSGHeaderRejectsMalformed(t *testing.T) {
	if _, _, err := parseMSGHeader("factory.line1"); err == nil {
		t.Fatal("expected error for too few fields")
	}
}

func TestParseMSGHeaderRejectsImplausibleByteCount(t *testing.T) {
	if _, _, err := parseMSGHeader("s 1 999999999999"); err == nil {
		t.Fatal("expected error for implausible byte count")
	}
}

func TestParseMSGHeaderRejectsNegativeByteCount(t *testing.T) {
	if _, _, err := parseMSGHeader("s 1 -5"); err == nil {
		t.Fatal("expected error for negative byte count")
	}
}

func TestBuildConnectOmitsEmptyAuthFields(t *testing.T) {
	b := buildConnect("nodra", "", "", "", "0.2.2")
	s := string(b)
	if strings.Contains(s, `"user"`) || strings.Contains(s, `"pass"`) || strings.Contains(s, `"auth_token"`) {
		t.Fatalf("connect payload should omit empty auth fields: %s", s)
	}
	if !strings.HasPrefix(s, "CONNECT {") || !strings.HasSuffix(s, "}\r\n") {
		t.Fatalf("connect payload malformed: %q", s)
	}
}

func TestBuildConnectIncludesAuthFieldsWhenSet(t *testing.T) {
	b := buildConnect("nodra", "u", "p", "", "0.2.2")
	s := string(b)
	if !strings.Contains(s, `"user":"u"`) || !strings.Contains(s, `"pass":"p"`) {
		t.Fatalf("connect payload missing auth fields: %s", s)
	}
}

func TestBuildSub(t *testing.T) {
	got := string(buildSub("factory.line1", 3))
	if got != "SUB factory.line1 3\r\n" {
		t.Fatalf("got %q", got)
	}
}
