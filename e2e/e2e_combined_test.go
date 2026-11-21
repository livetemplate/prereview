//go:build browser

// End-to-end tests for #114: `prereview --external <url> <path>` reviews the
// path and annotates the live site in one session, one store and one queue.
// Run with: go test -tags=browser -run 'TestCombined|TestE2E_Combined' ./e2e/
package e2e

import (
	"bufio"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/livetemplate/prereview/csv"
)

// launchCombined starts a combined session without a browser and returns every
// preamble line keyed by its tag (READY, PROXY, REPO, STORE), and the cmd.
func launchCombined(t *testing.T, target, path string, extraArgs ...string) (map[string]string, *exec.Cmd) {
	t.Helper()
	args := append([]string{"--external", target, "--port", "0", "--host", "127.0.0.1", "--no-update"}, extraArgs...)
	cmd := exec.Command(prereviewBinary(t), append(args, path)...)
	cmd.Env = prefsIsolatedEnv(path)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr := newBytesBuf()
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })
	done := make(chan map[string]string, 1)
	go func() {
		lines := map[string]string{}
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			tag, rest, _ := strings.Cut(sc.Text(), " ")
			if _, seen := lines[tag]; !seen {
				lines[tag] = rest
			}
			if tag == "STORE" {
				done <- lines
				_, _ = io.Copy(io.Discard, stdout)
				return
			}
		}
		done <- lines
	}()
	select {
	case lines := <-done:
		if lines["STORE"] == "" {
			t.Fatalf("no STORE line: %v\nstderr: %s", lines, stderr.String())
		}
		return lines, cmd
	case <-time.After(15 * time.Second):
		t.Fatalf("combined launch never printed STORE\nstderr: %s", stderr.String())
	}
	return nil, nil
}

// TestCombined_Launch pins Phase 1: a path with --external runs the repo review
// (default store, a v0 version, a live import drain) and serves the proxy.
func TestCombined_Launch(t *testing.T) {
	app := newLiveApp(t)
	repo := setupImportRepo(t)
	lines, _ := launchCombined(t, app.srv.URL, repo, "--agent")

	for _, tag := range []string{"READY", "PROXY", "REPO", "STORE"} {
		if lines[tag] == "" {
			t.Errorf("preamble lacks %s: %v", tag, lines)
		}
	}
	if want := filepath.Join(repo, ".prereview"); lines["STORE"] != want {
		t.Errorf("STORE = %q, want the repo's default store %q", lines["STORE"], want)
	}

	// The UI and the proxy both answer, behind the one launch token.
	client := tokenClient(t, lines["READY"])
	resp, err := client.Get(lines["PROXY"])
	if err != nil {
		t.Fatalf("proxy: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "PAGE ONE") {
		t.Errorf("the proxy didn't serve the live site: %d %.200s", resp.StatusCode, body)
	}

	// The repo-only machinery runs: a baseline version and the import drain.
	if entries, err := os.ReadDir(filepath.Join(lines["STORE"], "versions")); err != nil || len(entries) == 0 {
		t.Errorf("no baseline version in a combined session (%v)", err)
	}
	f := filepath.Join(t.TempDir(), "import.json")
	if err := os.WriteFile(f, []byte(`{"remote_id":"r1","source":"Docs","file":"plan.md","quote":"The rollout starts small.","body":"Too vague?"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(prereviewBinary(t), "import", "--out", lines["STORE"], "--file", f).CombinedOutput(); err != nil {
		t.Fatalf("import: %v: %s", err, out)
	}
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		rows, _ := csv.Read(filepath.Join(lines["STORE"], "comments.csv"))
		if len(rows) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the import never drained into comments.csv: %+v", rows)
		}
	}
}

// TestCombined_RefusesSingleFile pins decision 8: a live-site annotation has no
// file, so a single-file scope would hide it; the launch says so instead.
func TestCombined_RefusesSingleFile(t *testing.T) {
	repo := setupImportRepo(t)
	cmd := exec.Command(prereviewBinary(t), "--external", "http://127.0.0.1:1", "--port", "0", "--host", "127.0.0.1", "--no-update", filepath.Join(repo, "plan.md"))
	cmd.Env = prefsIsolatedEnv(repo)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("a single-file combined launch must fail; got: %s", out)
	}
	if !strings.Contains(string(out), "single file") {
		t.Errorf("the error should name the single-file restriction: %s", out)
	}
}
