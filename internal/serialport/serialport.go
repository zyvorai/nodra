// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

//go:build linux

// Package serialport configures and opens Linux serial devices via termios
// ioctls, shared by any connector that talks to a real serial line (Modbus
// RTU, the generic passthrough serial connector). It knows nothing about any
// wire protocol — framing/CRC/etc. stay in the connector that uses it.
package serialport

import (
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"
	"time"
	"unsafe"
)

// Config configures the serial line. Baud/DataBits/StopBits/Parity mirror
// the fields modbus.RTUClient already exposed before this package existed.
type Config struct {
	Baud     int
	DataBits int
	StopBits int
	Parity   string
	Timeout  time.Duration
}

func (c Config) timeout() time.Duration {
	if c.Timeout <= 0 {
		return 3 * time.Second
	}
	return c.Timeout
}

// Port is an opened, termios-configured, non-blocking serial device.
type Port struct {
	*os.File
	cfg Config
}

// Open opens device and configures it per cfg. It opens the device fresh
// each call (no persistent handle cached here) so reconnect/recovery after
// an unplug/replug is deterministic, matching modbus.RTUClient's original
// per-transaction-open behavior.
func Open(device string, cfg Config) (*Port, error) {
	if device == "" {
		return nil, errors.New("serial device is required")
	}
	file, err := os.OpenFile(device, os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, err
	}
	if err = configure(file.Fd(), cfg); err != nil {
		file.Close()
		return nil, err
	}
	if err = syscall.SetNonblock(int(file.Fd()), true); err != nil {
		file.Close()
		return nil, err
	}
	return &Port{File: file, cfg: cfg}, nil
}

// Timeout returns the configured round-trip/read timeout (default 3s).
func (p *Port) Timeout() time.Duration { return p.cfg.timeout() }

// Read reads once from the device. A termios line configured with
// VMIN=0/VTIME=0 (set in configure) returns a 0-byte read - which Go's
// os.File surfaces as io.EOF - the moment no data happens to be buffered
// yet. That is normal non-canonical "nothing available right now" behavior
// for a character device, not an actual end-of-file condition, so - like
// EAGAIN/EWOULDBLOCK - it is reported here as (0, nil) rather than an error,
// letting callers poll/retry without special-casing serial errno values.
func (p *Port) Read(buf []byte) (int, error) {
	n, err := p.File.Read(buf)
	if n == 0 && err != nil && isReadRetryable(err) {
		return 0, nil
	}
	return n, err
}

// Write writes once to the device, treating EAGAIN/EWOULDBLOCK (the device
// isn't ready to accept more bytes yet) as (0, nil) rather than an error.
func (p *Port) Write(buf []byte) (int, error) {
	n, err := p.File.Write(buf)
	if n == 0 && err != nil && isWriteRetryable(err) {
		return 0, nil
	}
	return n, err
}

func isReadRetryable(err error) bool {
	return errors.Is(err, syscall.EAGAIN) || errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, io.EOF)
}
func isWriteRetryable(err error) bool {
	return errors.Is(err, syscall.EAGAIN) || errors.Is(err, syscall.EWOULDBLOCK)
}

func configure(fd uintptr, cfg Config) error {
	var term syscall.Termios
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, uintptr(syscall.TCGETS), uintptr(unsafe.Pointer(&term)))
	if errno != 0 {
		return errno
	}
	term.Iflag = 0
	term.Oflag = 0
	term.Lflag = 0
	term.Cflag |= syscall.CREAD | syscall.CLOCAL
	term.Cflag &^= syscall.CSIZE | syscall.PARENB | syscall.PARODD | syscall.CSTOPB

	switch cfg.DataBits {
	case 0, 8:
		term.Cflag |= syscall.CS8
	case 7:
		term.Cflag |= syscall.CS7
	default:
		return fmt.Errorf("unsupported serial data bits %d", cfg.DataBits)
	}
	switch cfg.Parity {
	case "", "none", "N", "n":
	case "even", "E", "e":
		term.Cflag |= syscall.PARENB
	case "odd", "O", "o":
		term.Cflag |= syscall.PARENB | syscall.PARODD
	default:
		return fmt.Errorf("unsupported serial parity %q", cfg.Parity)
	}
	if cfg.StopBits == 2 {
		term.Cflag |= syscall.CSTOPB
	} else if cfg.StopBits != 0 && cfg.StopBits != 1 {
		return fmt.Errorf("unsupported serial stop bits %d", cfg.StopBits)
	}

	speed, err := BaudConstant(cfg.Baud)
	if err != nil {
		return err
	}
	term.Cflag &^= 0x100f // Linux asm-generic CBAUD mask
	term.Cflag |= speed
	term.Ispeed = speed
	term.Ospeed = speed
	term.Cc[syscall.VMIN] = 0
	term.Cc[syscall.VTIME] = 0
	_, _, errno = syscall.Syscall(syscall.SYS_IOCTL, fd, uintptr(syscall.TCSETS), uintptr(unsafe.Pointer(&term)))
	if errno != 0 {
		return errno
	}
	return nil
}

// BaudConstant maps a baud rate to its termios speed constant. Exported so
// callers (e.g. a connector's config validation) can reject an unsupported
// baud rate eagerly, before ever trying to open a device.
func BaudConstant(baud int) (uint32, error) {
	if baud == 0 {
		baud = 9600
	}
	values := map[int]uint32{
		1200: syscall.B1200, 2400: syscall.B2400, 4800: syscall.B4800,
		9600: syscall.B9600, 19200: syscall.B19200, 38400: syscall.B38400,
		57600: syscall.B57600, 115200: syscall.B115200, 230400: syscall.B230400,
	}
	value, ok := values[baud]
	if !ok {
		return 0, fmt.Errorf("unsupported serial baud %d", baud)
	}
	return value, nil
}
