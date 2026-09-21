// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"errors"
	"fmt"
)

// ErrConflict is returned when an optimistic update loses the revision race
// after its retries. Callers should re-read and try again, or surface a conflict.
var ErrConflict = errors.New("store revision conflict")

func errUnsupportedDriver(d string) error {
	return fmt.Errorf("unsupported store driver %q (want file|postgres)", d)
}
