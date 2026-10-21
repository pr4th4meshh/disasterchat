//go:build !windows

package chat

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// lockDataDir stops a second node using the same directory: it would load
// the same key, be the same peer, and silently never see the first one.
// The OS drops the lock when the process exits, even if it crashed.
func lockDataDir(dir string) (*os.File, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("another DisasterChat is already using %s; give this one its own, e.g. --data /tmp/dc2", dir)
	}
	return f, nil
}
