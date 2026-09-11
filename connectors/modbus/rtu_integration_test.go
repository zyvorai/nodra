// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package modbus

// Hardware-free integration test: spawns a real pty pair via socat and drives
// RTUClient against one end while a fake slave goroutine answers on the
// other. This exercises the actual TCGETS/TCSETS termios configuration and
// the non-blocking read/write loop in rtu.go against a real kernel line
// discipline, not a synthetic byte slice like rtu_test.go's unit tests.
//
// A pty pair has no real baud-rate clocking and no RS485 direction-control
// hook (rtu.go has none today either). This harness proves the framing/CRC/
// termios-configuration logic, not electrical or RS485 conformance — that
// still needs a real board, per docs/HARDWARE_PERMISSIONS.md in the
// zyvor-device-agent repo and the Minewing acceptance guide.

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRTUIntegration(t *testing.T) {
	socatPath, err := exec.LookPath("socat")
	if err != nil {
		t.Skip("socat not on PATH; skipping hardware-free Modbus RTU integration test")
	}

	dir := t.TempDir()
	clientLink := filepath.Join(dir, "client")
	slaveLink := filepath.Join(dir, "slave")

	cmd := exec.Command(socatPath, "-d", "-d",
		"pty,raw,echo=0,link="+clientLink,
		"pty,raw,echo=0,link="+slaveLink,
	)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start socat: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	waitForLink(t, clientLink)
	waitForLink(t, slaveLink)
	// socat needs a moment after creating both ptys to finish wiring the relay.
	time.Sleep(150 * time.Millisecond)

	client := &RTUClient{Device: clientLink, UnitID: 0x11, Baud: 9600, Timeout: 2 * time.Second}

	t.Run("read holding registers happy path", func(t *testing.T) {
		slave := openSlave(t, slaveLink)
		defer slave.Close()

		fakeSlaveDone := make(chan error, 1)
		go func() {
			req := make([]byte, 8)
			if _, err := io.ReadFull(slave, req); err != nil {
				fakeSlaveDone <- err
				return
			}
			if !validCRC(req) {
				fakeSlaveDone <- errors.New("fake slave: bad request CRC")
				return
			}
			resp := appendCRC([]byte{0x11, 0x03, 0x04, 0x00, 0x2a, 0x00, 0x64})
			_, err := slave.Write(resp)
			fakeSlaveDone <- err
		}()

		values, err := client.ReadHoldingRegisters(context.Background(), 0x0000, 2)
		if err != nil {
			t.Fatalf("ReadHoldingRegisters: %v", err)
		}
		if err := <-fakeSlaveDone; err != nil {
			t.Fatalf("fake slave: %v", err)
		}
		if len(values) != 2 || values[0] != 0x002a || values[1] != 0x0064 {
			t.Fatalf("unexpected values: %#v", values)
		}
	})

	t.Run("write single register round trip", func(t *testing.T) {
		slave := openSlave(t, slaveLink)
		defer slave.Close()

		fakeSlaveDone := make(chan error, 1)
		go func() {
			req := make([]byte, 8)
			if _, err := io.ReadFull(slave, req); err != nil {
				fakeSlaveDone <- err
				return
			}
			if !validCRC(req) {
				fakeSlaveDone <- errors.New("fake slave: bad request CRC")
				return
			}
			// Modbus write-single-register response is an echo of the request.
			_, err := slave.Write(req)
			fakeSlaveDone <- err
		}()

		if err := client.WriteSingleRegister(context.Background(), 0x0010, 0x00ff); err != nil {
			t.Fatalf("WriteSingleRegister: %v", err)
		}
		if err := <-fakeSlaveDone; err != nil {
			t.Fatalf("fake slave: %v", err)
		}
	})

	t.Run("corrupted CRC response rejected", func(t *testing.T) {
		slave := openSlave(t, slaveLink)
		defer slave.Close()

		fakeSlaveDone := make(chan error, 1)
		go func() {
			req := make([]byte, 8)
			if _, err := io.ReadFull(slave, req); err != nil {
				fakeSlaveDone <- err
				return
			}
			resp := appendCRC([]byte{0x11, 0x03, 0x02, 0x00, 0x01})
			resp[len(resp)-1] ^= 0xff // corrupt the CRC
			_, err := slave.Write(resp)
			fakeSlaveDone <- err
		}()

		_, err := client.ReadHoldingRegisters(context.Background(), 0x0000, 1)
		if err == nil {
			t.Fatal("expected a CRC error, got nil")
		}
		if !strings.Contains(err.Error(), "CRC") {
			t.Fatalf("expected a CRC error, got: %v", err)
		}
		if err := <-fakeSlaveDone; err != nil {
			t.Fatalf("fake slave: %v", err)
		}
	})

	t.Run("timeout on silence", func(t *testing.T) {
		slave := openSlave(t, slaveLink)
		defer slave.Close()

		// Drain the request so this exercises the read-side timeout, not a
		// write stalling because nothing is consuming the pty buffer.
		drained := make(chan struct{})
		go func() {
			_, _ = io.ReadFull(slave, make([]byte, 8))
			close(drained)
		}()

		impatient := &RTUClient{Device: clientLink, UnitID: 0x11, Baud: 9600, Timeout: 300 * time.Millisecond}
		start := time.Now()
		_, err := impatient.ReadHoldingRegisters(context.Background(), 0x0000, 1)
		elapsed := time.Since(start)

		if err == nil {
			t.Fatal("expected a timeout error, got nil")
		}
		if !strings.Contains(err.Error(), "timeout") {
			t.Fatalf("expected a timeout error, got: %v", err)
		}
		if elapsed > 2*time.Second {
			t.Fatalf("timeout took too long to fire: %s", elapsed)
		}
		<-drained
	})
}

func openSlave(t *testing.T, path string) *os.File {
	t.Helper()
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open fake-slave end %s: %v", path, err)
	}
	return f
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
