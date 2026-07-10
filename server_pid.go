package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// claimServerLock records that THIS process owns the review server for the store
// under prereviewDir (via prereviewDir/server.pid), refusing to start a second
// server for the same store — two servers fighting over one .prereview/ corrupt
// the queue. If a live server already holds the lock: with replace it is stopped
// (SIGTERM, then Kill) and taken over; without replace an error is returned. The
// returned release removes the pid file only if it still holds our pid, so a
// server that later replaced us keeps its lock when our deferred release runs.
func claimServerLock(prereviewDir string, replace bool) (release func(), err error) {
	if err := os.MkdirAll(prereviewDir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", prereviewDir, err)
	}
	pidPath := filepath.Join(prereviewDir, "server.pid")

	if data, rerr := os.ReadFile(pidPath); rerr == nil {
		if pid, perr := strconv.Atoi(strings.TrimSpace(string(data))); perr == nil && processAlive(pid) {
			if !replace {
				return nil, fmt.Errorf("prereview is already running for this store (pid %d); pass --replace to take over", pid)
			}
			if err := stopServer(pid); err != nil {
				return nil, err
			}
		}
	}

	if err := writePID(pidPath, prereviewDir, os.Getpid()); err != nil {
		return nil, err
	}

	return func() {
		// Only remove the lock if it's still ours. A server that replaced us
		// (after we exited) now owns server.pid; deleting it would strip its
		// lock and let a third server start alongside it.
		data, rerr := os.ReadFile(pidPath)
		if rerr != nil {
			return
		}
		if pid, perr := strconv.Atoi(strings.TrimSpace(string(data))); perr == nil && pid == os.Getpid() {
			_ = os.Remove(pidPath)
		}
	}, nil
}

// stopServer terminates the running server pid and waits for it to exit, so the
// replacement can claim the store cleanly. SIGTERM lets it unwind its deferred
// cleanups (comments auto-save); Kill is the fallback if signalling fails.
func stopServer(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("find running server (pid %d): %w", pid, err)
	}
	if serr := p.Signal(syscall.SIGTERM); serr != nil {
		_ = p.Kill()
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	if processAlive(pid) {
		return fmt.Errorf("existing prereview server (pid %d) did not exit after --replace", pid)
	}
	return nil
}

// writePID writes pid to pidPath atomically: a temp file in the same dir renamed
// over the target, so a reader never sees a half-written pid.
func writePID(pidPath, dir string, pid int) error {
	tmp, err := os.CreateTemp(dir, "server.pid-*")
	if err != nil {
		return fmt.Errorf("create pid temp: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.WriteString(strconv.Itoa(pid)); err != nil {
		tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("write pid temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("close pid temp: %w", err)
	}
	if err := os.Rename(tmpName, pidPath); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("install pid file: %w", err)
	}
	return nil
}

// processAlive reports whether pid names a live process, via signal 0 (which
// checks for existence/permission without delivering a signal).
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}
