// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package store

import "fmt"

func errUnsupportedDriver(d string) error {
	return fmt.Errorf("unsupported store driver %q (want file|postgres)", d)
}
