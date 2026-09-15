// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

//go:build !linux

package serialport

import (
	"fmt"
	"runtime"
	"time"
)

// Config is unsupported outside Linux: serial line configuration relies on
// Linux termios ioctls. Kept so packages that build against serialport still
// compile, and fail with a clear error instead of a missing symbol.
type Config struct {
	Baud     int
	DataBits int
	StopBits int
	Parity   string
	Timeout  time.Duration
}

type Port struct{}

func Open(string, Config) (*Port, error) { return nil, unsupportedErr() }

func (p *Port) Read([]byte) (int, error)  { return 0, unsupportedErr() }
func (p *Port) Write([]byte) (int, error) { return 0, unsupportedErr() }
func (p *Port) Close() error              { return nil }
func (p *Port) Timeout() time.Duration    { return 0 }

func BaudConstant(int) (uint32, error) { return 0, unsupportedErr() }

func unsupportedErr() error {
	return fmt.Errorf("serial port support requires Linux (running on %s)", runtime.GOOS)
}
