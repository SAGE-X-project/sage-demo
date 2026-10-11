// SPDX-License-Identifier: LGPL-3.0-or-later
package demo

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// lock takes the demo directory's lock, so that two runs never share signer
// sockets, host state or a ledger. The returned function releases it.
func lock(dir string) (func(), error) {
	f, err := os.OpenFile(filepath.Join(dir, ".lock"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("another demo command is using %s", dir)
	}
	return func() { _ = f.Close() }, nil
}
