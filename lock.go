package main

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// acquireRunLock takes an exclusive, non-blocking flock in dir so a cron run
// and a web run never overlap. The returned function releases the lock.
func acquireRunLock(dir string) (func(), error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("creating output directory %s: %w", dir, err)
	}
	path := filepath.Join(dir, ".kindle-digest.lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening run lock %s: %w", path, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("another digest run is already in progress (%s)", path)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}
