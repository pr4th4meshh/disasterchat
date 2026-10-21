package chat

import "os"

// lockDataDir is not enforced on Windows yet; see lock_unix.go.
func lockDataDir(dir string) (*os.File, error) {
	return nil, os.MkdirAll(dir, 0o700)
}
