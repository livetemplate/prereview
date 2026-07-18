//go:build browser

package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
)

// The e2e suite drives a real prereview binary, and every test used to build
// its own copy with `go build -o <t.TempDir()>/prereview ..` — 7 build sites
// reached by 135 test entry points. Go's build cache makes runs 2..n a relink
// rather than a recompile, but that is still ~145 redundant link+write cycles
// per suite run, and on a cold cache (CI) the first one pays a full compile
// while every later one pays the link.
//
// Building once per test process removes that entirely. It is lazy rather than
// a TestMain build so a machine with no browser still skips (via findChromium)
// without paying for a compile it will never run, and sync.Once keeps it
// correct if these tests are ever given t.Parallel.
var (
	buildOnce sync.Once
	builtBin  string
	buildDir  string
	buildErr  error
)

// prereviewBinary returns the path to a freshly built prereview binary, built
// exactly once per test process. Every test shares the same binary; none of
// them mutate it.
func prereviewBinary(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		buildDir, buildErr = os.MkdirTemp("", "prereview-e2e-bin")
		if buildErr != nil {
			buildErr = fmt.Errorf("mktemp for e2e binary: %w", buildErr)
			return
		}
		bin := filepath.Join(buildDir, "prereview")
		if out, err := exec.Command("go", "build", "-o", bin, "..").CombinedOutput(); err != nil {
			buildErr = fmt.Errorf("go build: %w\n%s", err, out)
			return
		}
		builtBin = bin
	})
	if buildErr != nil {
		t.Fatalf("%v", buildErr)
	}
	return builtBin
}

// TestMain exists only to remove the shared binary's directory after the whole
// suite has run — a per-test t.Cleanup would delete it out from under the
// tests that follow.
func TestMain(m *testing.M) {
	code := m.Run()
	if buildDir != "" {
		_ = os.RemoveAll(buildDir)
	}
	os.Exit(code)
}
