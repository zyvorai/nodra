// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

//go:build !linux

package serial

import (
	"fmt"
	"runtime"
)

// NewPoller still validates config on every platform (see connector.go); a
// "serial" connector instance is only unusable at Start() time here, kept
// consistent with how connectors/modbus's "rtu" transport behaves off-Linux.
func init() { dialPort = openStubPort }

func openStubPort(PollerConfig) (serialReader, error) {
	return nil, fmt.Errorf("serial connector requires Linux (running on %s)", runtime.GOOS)
}
