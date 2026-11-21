//go:build browser

// End-to-end for --replace: it stops the server holding the store's lock only
// after that server confirms, at the address recorded in server.pid, that it is
// the pid the file names. A pid alone can be recycled by an unrelated process.
//
// Run: go test -tags=browser -run TestE2E_Replace ./e2e/...

package e2e

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

type pidRecord struct {
	PID  int    `json:"pid"`
	Addr string `json:"addr"`
}

func readPIDRecord(t *testing.T, store string) (pidRecord, []byte) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(store, "server.pid"))
	if err != nil {
		t.Fatalf("read server.pid: %v", err)
	}
	var rec pidRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatalf("server.pid is not a record: %v\n%s", err, raw)
	}
	return rec, raw
}

func TestE2E_ReplaceConfirmsBeforeStopping(t *testing.T) {
	one := bootChromeAgainstRepo(t, setupFixtureRepo(t), 1400, 1000, "--agent")
	diag := captureDiag(t, one)
	one.waitReady()

	exited := make(chan struct{})
	go func() { _ = one.cmd.Wait(); close(exited) }()

	rec, raw := readPIDRecord(t, one.store)
	if rec.PID != one.cmd.Process.Pid || rec.Addr == "" {
		t.Fatalf("server.pid = %s, want pid %d with its listen address", raw, one.cmd.Process.Pid)
	}

	// Point the lock at a live process that is not a prereview server.
	sleeper := exec.Command("sleep", "60")
	if err := sleeper.Start(); err != nil {
		t.Fatalf("start sleeper: %v", err)
	}
	sleeperDone := make(chan struct{})
	go func() { _ = sleeper.Wait(); close(sleeperDone) }()
	t.Cleanup(func() { _ = sleeper.Process.Kill(); <-sleeperDone })
	stale, _ := json.Marshal(pidRecord{PID: sleeper.Process.Pid, Addr: rec.Addr})
	if err := os.WriteFile(filepath.Join(one.store, "server.pid"), stale, 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	refused := exec.CommandContext(ctx, one.binary,
		"--base", "HEAD", "--port", "0", "--host", "127.0.0.1", "--agent", "--replace", one.repo)
	refused.Env = prefsIsolatedEnv(one.repo)
	out, err := refused.CombinedOutput()
	if err == nil || ctx.Err() != nil {
		t.Fatalf("--replace over an unconfirmed pid should exit with an error, got err=%v ctx=%v\n%s%s", err, ctx.Err(), out, diag())
	}
	if !strings.Contains(string(out), "can't confirm") {
		t.Errorf("refusal should say the pid could not be confirmed:\n%s", out)
	}
	select {
	case <-sleeperDone:
		t.Fatal("--replace signalled a process it could not confirm")
	default:
	}
	// The first server kept serving.
	if err := chromedp.Run(one.ctx,
		chromedp.Reload(),
		chromedp.WaitVisible(`#files-drawer button.file-btn`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("the first server stopped after a refused --replace: %v%s", err, diag())
	}

	// Restore the real record: now --replace confirms and takes over.
	if err := os.WriteFile(filepath.Join(one.store, "server.pid"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	// A failed takeover is about the FIRST server: startPrereview reports only
	// the replacement's log, so print the first one's too ("servers stopped
	// took=" says how long its shutdown ran).
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("--- first server ---\n%s", one.stderr.String())
		}
	})
	urlTwo, storeTwo, srvTwo, stderrTwo := startPrereview(t, one.binary, one.repo, "--agent", "--replace")
	t.Cleanup(func() { _ = srvTwo.Process.Kill(); _, _ = srvTwo.Process.Wait() })
	select {
	case <-exited:
	case <-time.After(10 * time.Second): // past --replace's own wait (stopGrace: shutdownTimeout + liveFlushTimeout + 1s)
		t.Fatalf("the confirmed server did not exit after --replace\n--- replacement ---\n%s%s", stderrTwo.String(), diag())
	}
	if storeTwo != one.store {
		t.Fatalf("replacement store %q, want %q", storeTwo, one.store)
	}
	if got, _ := readPIDRecord(t, storeTwo); got.PID != srvTwo.Process.Pid || got.Addr == "" {
		t.Fatalf("server.pid after takeover = %+v, want pid %d with an address", got, srvTwo.Process.Pid)
	}
	if err := chromedp.Run(one.ctx,
		chromedp.Navigate(urlTwo),
		chromedp.WaitVisible(`#files-drawer button.file-btn`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("the replacement server did not serve the review: %v\n--- replacement ---\n%s%s", err, stderrTwo.String(), diag())
	}
}
