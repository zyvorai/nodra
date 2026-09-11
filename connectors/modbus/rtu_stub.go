// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

//go:build !linux

package modbus

import (
	"context"
	"fmt"
	"runtime"
	"time"
)

// RTUClient is unsupported outside Linux: Modbus RTU relies on Linux termios
// ioctls to configure the serial line. Kept so the modbus package still
// builds and the "rtu" transport fails with a clear error instead of a
// missing symbol on other platforms.
type RTUClient struct {
	Device   string
	UnitID   byte
	Baud     int
	DataBits int
	Parity   string
	StopBits int
	Timeout  time.Duration
}

func (c *RTUClient) ReadHoldingRegisters(context.Context, uint16, uint16) ([]uint16, error) {
	return nil, unsupportedErr()
}

func (c *RTUClient) WriteSingleRegister(context.Context, uint16, uint16) error {
	return unsupportedErr()
}

func baudConstant(int) (uint32, error) {
	return 0, unsupportedErr()
}

func unsupportedErr() error {
	return fmt.Errorf("modbus RTU transport requires Linux (running on %s)", runtime.GOOS)
}
