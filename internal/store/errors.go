package store

import "fmt"

func errUnsupportedDriver(d string) error {
	return fmt.Errorf("unsupported store driver %q (want file|postgres)", d)
}
