// Package flock is a cross-process exclusive file lock: several prereview
// servers (one per review) share the per-user library store, and a read-then-
// append (a version checkpoint's dedupe and sequence number, a log compaction)
// must not interleave between them. An in-process mutex can't see the others.
package flock

import (
	"os"
	"path/filepath"
)

// Lock takes an exclusive lock on path (created if missing), blocking until it
// is free, and returns the function that releases it. The lock is advisory:
// every writer of the guarded data must take it.
func Lock(path string) (unlock func(), err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, err
	}
	if err := lockFile(f); err != nil {
		f.Close()
		return nil, err
	}
	return func() {
		_ = unlockFile(f)
		_ = f.Close()
	}, nil
}
