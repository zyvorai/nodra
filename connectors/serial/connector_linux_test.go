// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package serial

// Hardware-free integration test: spawns a real pty pair via socat (same
// harness as connectors/modbus/rtu_integration_test.go) and drives the real
// openLinuxPort/serialport.Open path against one end while a fake device
// goroutine writes scripted, delimited frames on the other, proving the
// actual termios-configured, non-blocking read path frames real bytes
// correctly - not just the in-memory fake used by connector_test.go.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/zyvorai/nodra/pkg/connector"
)

func TestSerialConnectorIntegration(t *testing.T) {
	socatPath, err := exec.LookPath("socat")
	if err != nil {
		t.Skip("socat not on PATH; skipping hardware-free serial connector integration test")
	}

	dir := t.TempDir()
	clientLink := filepath.Join(dir, "client")
	deviceLink := filepath.Join(dir, "device")

	cmd := exec.Command(socatPath, "-d", "-d",
		"pty,raw,echo=0,link="+clientLink,
		"pty,raw,echo=0,link="+deviceLink,
	)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start socat: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	waitForLink(t, clientLink)
	waitForLink(t, deviceLink)
	time.Sleep(150 * time.Millisecond)

	device, err := os.OpenFile(deviceLink, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open fake-device end %s: %v", deviceLink, err)
	}
	defer device.Close()

	p := &Poller{name: "s", cfg: PollerConfig{Device: clientLink, Topic: "t", Framing: "delimiter"}, delimiter: '\n', maxFrame: 65536}
	port, err := openLinuxPort(p.cfg)
	if err != nil {
		t.Fatalf("openLinuxPort: %v", err)
	}
	defer port.Close()

	writeDone := make(chan error, 1)
	go func() {
		_, err := device.Write([]byte("hello\nworld\n"))
		writeDone <- err
	}()

	events := make(chan connector.Event, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go func() {
		_ = p.readFrames(ctx, port, func(_ context.Context, ev connector.Event) error {
			events <- ev
			return nil
		})
	}()

	if err := <-writeDone; err != nil {
		t.Fatalf("write to fake device: %v", err)
	}

	var got []string
	for len(got) < 2 {
		select {
		case ev := <-events:
			got = append(got, string(ev.Payload))
		case <-ctx.Done():
			t.Fatalf("timed out waiting for frames, got so far: %v", got)
		}
	}
	if got[0] != "hello" || got[1] != "world" {
		t.Fatalf("got=%v, want [hello world]", got)
	}
}

func waitForLink(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Lstat(path); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("socat pty link %s did not appear in time", path)
}
