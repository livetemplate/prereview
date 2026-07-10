package main

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func pidFile(dir string) string { return filepath.Join(dir, "server.pid") }

// deadPID returns a pid that is (almost certainly) not alive. It spawns nothing
// and signals nothing real — it just picks a very high number unlikely to be in
// use, then confirms it's dead via processAlive so the test is self-checking.
func deadPID(t *testing.T) int {
	t.Helper()
	for _, pid := range []int{99999999, 88888888, 77777777} {
		if !processAlive(pid) {
			return pid
		}
	}
	t.Fatal("could not find a definitely-dead pid")
	return 0
}

func TestClaimServerLock_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	release, err := claimServerLock(dir, false)
	if err != nil {
		t.Fatalf("claim on empty dir: %v", err)
	}
	if _, err := os.Stat(pidFile(dir)); err != nil {
		t.Fatalf("pid file not written: %v", err)
	}
	release()
	if _, err := os.Stat(pidFile(dir)); !os.IsNotExist(err) {
		t.Fatalf("release should have removed the pid file, stat err = %v", err)
	}
}

func TestClaimServerLock_DeadPIDReclaims(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(pidFile(dir), []byte(strconv.Itoa(deadPID(t))), 0o644); err != nil {
		t.Fatalf("seed pid file: %v", err)
	}
	release, err := claimServerLock(dir, false)
	if err != nil {
		t.Fatalf("claim over a dead pid should succeed: %v", err)
	}
	defer release()
	data, _ := os.ReadFile(pidFile(dir))
	if got := string(data); got != strconv.Itoa(os.Getpid()) {
		t.Fatalf("pid file = %q, want our pid %d", got, os.Getpid())
	}
}

func TestClaimServerLock_AlivePIDNoReplaceErrors(t *testing.T) {
	dir := t.TempDir()
	// os.Getpid() is alive (it's us), so a non-replacing claim must refuse.
	if err := os.WriteFile(pidFile(dir), []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
		t.Fatalf("seed pid file: %v", err)
	}
	release, err := claimServerLock(dir, false)
	if err == nil {
		release()
		t.Fatal("expected an error claiming over a live server without --replace")
	}
}

func TestClaimServerLock_ReleaseLeavesForeignPID(t *testing.T) {
	dir := t.TempDir()
	release, err := claimServerLock(dir, false)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	// Simulate a later server replacing us: it overwrote server.pid with its
	// own (different) pid. Our deferred release must NOT strip its lock.
	foreign := deadPID(t)
	if err := os.WriteFile(pidFile(dir), []byte(strconv.Itoa(foreign)), 0o644); err != nil {
		t.Fatalf("overwrite pid file: %v", err)
	}
	release()
	data, err := os.ReadFile(pidFile(dir))
	if err != nil {
		t.Fatalf("pid file should still exist: %v", err)
	}
	if got := string(data); got != strconv.Itoa(foreign) {
		t.Fatalf("pid file = %q, want the foreign pid %d preserved", got, foreign)
	}
}
