// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package durable

import (
	"fmt"
	"os"
	"path/filepath"
)

// AtomicWrite writes data to a temporary file in the same directory, fsyncs it,
// renames it into place, then fsyncs the parent directory. This makes normal
// process crashes and power-loss windows materially safer than os.WriteFile+rename.
func AtomicWrite(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".nodra-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		_ = f.Close()
		if !ok {
			_ = os.Remove(tmp)
		}
	}()
	if err = f.Chmod(mode); err != nil {
		return err
	}
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(tmp, path); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open parent for sync: %w", err)
	}
	defer d.Close()
	if err = d.Sync(); err != nil {
		return fmt.Errorf("sync parent: %w", err)
	}
	ok = true
	return nil
}
