// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package serialport

import (
	"os"
	"testing"
)

func TestBaudValidation(t *testing.T) {
	if _, err := BaudConstant(115200); err != nil {
		t.Fatal(err)
	}
	if _, err := BaudConstant(12345); err == nil {
		t.Fatal("unsupported baud unexpectedly accepted")
	}
}

func TestBaudDefaultsWhenZero(t *testing.T) {
	got, err := BaudConstant(0)
	if err != nil {
		t.Fatal(err)
	}
	want, err := BaudConstant(9600)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("baud 0 should default to 9600's constant, got %v want %v", got, want)
	}
}

func TestOpenRejectsEmptyDevice(t *testing.T) {
	if _, err := Open("", Config{}); err == nil {
		t.Fatal("expected error for empty device")
	}
}

// configureOnPty exercises configure() against a real pty master, the only
// kind of fd in this sandbox that answers TCGETS/TCSETS like a real serial
// line would (a plain file such as /dev/null fails TCGETS with ENOTTY
// before configure() ever reaches its own validation, which would make a
// test against it pass for the wrong reason).
func configureOnPty(t *testing.T, cfg Config) error {
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		t.Skipf("no /dev/ptmx available in this sandbox: %v", err)
	}
	defer master.Close()
	return configure(master.Fd(), cfg)
}

func TestConfigureRejectsUnsupportedDataBits(t *testing.T) {
	if err := configureOnPty(t, Config{DataBits: 6}); err == nil {
		t.Fatal("expected error for unsupported data bits")
	}
}

func TestConfigureRejectsUnsupportedParity(t *testing.T) {
	if err := configureOnPty(t, Config{Parity: "mark"}); err == nil {
		t.Fatal("expected error for unsupported parity")
	}
}

func TestConfigureRejectsUnsupportedStopBits(t *testing.T) {
	if err := configureOnPty(t, Config{StopBits: 3}); err == nil {
		t.Fatal("expected error for unsupported stop bits")
	}
}

func TestConfigureAcceptsDefaults(t *testing.T) {
	if err := configureOnPty(t, Config{Baud: 9600}); err != nil {
		t.Fatalf("expected default 8N1 config to be accepted: %v", err)
	}
}
