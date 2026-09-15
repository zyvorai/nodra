// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package serial

import "github.com/zyvorai/nodra/internal/serialport"

func init() { dialPort = openLinuxPort }

func openLinuxPort(cfg PollerConfig) (serialReader, error) {
	return serialport.Open(cfg.Device, serialport.Config{
		Baud: cfg.Baud, DataBits: cfg.DataBits, Parity: cfg.Parity, StopBits: cfg.StopBits,
	})
}
