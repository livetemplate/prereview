package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

// releaseArchive builds a real release archive for the running platform
// (tar.gz, or zip on Windows) containing the bare prereview binary with
// the given content, exactly as goreleaser ships it. Returns the asset
// name, archive bytes, and the archive's sha256 hex.
func releaseArchive(t *testing.T, tag, binContent string) (assetName string, data []byte, sumHex string) {
	t.Helper()
	tagVer := tag
	if len(tagVer) > 0 && tagVer[0] == 'v' {
		tagVer = tagVer[1:]
	}
	assetName = fmt.Sprintf("prereview_%s_%s_%s.%s", tagVer, runtime.GOOS, runtime.GOARCH, archiveExt())

	var buf bytes.Buffer
	if archiveExt() == "zip" {
		zw := zip.NewWriter(&buf)
		w, err := zw.Create(binaryName())
		if err != nil {
			t.Fatalf("zip create: %v", err)
		}
		if _, err := w.Write([]byte(binContent)); err != nil {
			t.Fatalf("zip write: %v", err)
		}
		if err := zw.Close(); err != nil {
			t.Fatalf("zip close: %v", err)
		}
	} else {
		gw := gzip.NewWriter(&buf)
		tw := tar.NewWriter(gw)
		if err := tw.WriteHeader(&tar.Header{
			Name:     binaryName(),
			Mode:     0o755,
			Size:     int64(len(binContent)),
			Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatalf("tar header: %v", err)
		}
		if _, err := tw.Write([]byte(binContent)); err != nil {
			t.Fatalf("tar write: %v", err)
		}
		if err := tw.Close(); err != nil {
			t.Fatalf("tar close: %v", err)
		}
		if err := gw.Close(); err != nil {
			t.Fatalf("gzip close: %v", err)
		}
	}
	data = buf.Bytes()
	s := sha256.Sum256(data)
	return assetName, data, hex.EncodeToString(s[:])
}

type serverOpts struct {
	wrongChecksum bool // serve a bogus sha in checksums.txt
	archiveStatus int  // if != 0 and != 200, archive endpoint returns this
}

// releaseServer stands in for the GitHub API + asset CDN. apiBase for
// SelfUpdate/checkForUpdate is srv.URL. releasesHits counts how many
// times the "latest release" endpoint was queried (to assert throttle /
// skip behaviour).
func releaseServer(t *testing.T, tag, binContent string, opts serverOpts) (srv *httptest.Server, releasesHits *atomic.Int32, assetName, archiveSum string) {
	t.Helper()
	assetName, archive, sum := releaseArchive(t, tag, binContent)
	archiveSum = sum

	var hits atomic.Int32
	releasesHits = &hits

	mux := http.NewServeMux()
	mux.HandleFunc("/repos/livetemplate/prereview/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		body := ghRelease{TagName: tag}
		body.Assets = []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		}{
			{Name: assetName, URL: srv.URL + "/dl/" + assetName},
			{Name: checksumsName, URL: srv.URL + "/dl/" + checksumsName},
		}
		_ = json.NewEncoder(w).Encode(body)
	})
	mux.HandleFunc("/dl/"+checksumsName, func(w http.ResponseWriter, r *http.Request) {
		sumOut := sum
		if opts.wrongChecksum {
			sumOut = "0000000000000000000000000000000000000000000000000000000000000000"
		}
		fmt.Fprintf(w, "%s  %s\n", sumOut, assetName)
	})
	mux.HandleFunc("/dl/"+assetName, func(w http.ResponseWriter, r *http.Request) {
		if opts.archiveStatus != 0 && opts.archiveStatus != http.StatusOK {
			w.WriteHeader(opts.archiveStatus)
			return
		}
		_, _ = w.Write(archive)
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, releasesHits, assetName, archiveSum
}

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"0.0.1", "0.0.2", -1},
		{"0.0.2", "0.0.1", 1},
		{"1.0.0", "1.0.0", 0},
		{"v0.0.2", "v0.0.1", 1},
		{"v1.2.3", "1.2.3", 0},
		{"", "", 0},
		{"0.10.0", "0.9.0", 1},
		{"1.2.0", "1.2", 0},
		{"2.0.0", "1.99.99", 1},
		{"1.0.0-rc1", "1.0.0", 0},
		{"garbage", "0.0.1", -1},
	}
	for _, c := range cases {
		if got := compareVersions(c.a, c.b); got != c.want {
			t.Errorf("compareVersions(%q,%q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestShouldAutoUpdate(t *testing.T) {
	cases := []struct {
		name       string
		version    string
		noFlag     bool
		envNo      string
		envUpdated string
		want       bool
	}{
		{"released, no opt-out", "0.0.1", false, "", "", true},
		{"dev build", "dev", false, "", "", false},
		{"--no-update flag", "0.0.1", true, "", "", false},
		{"PREREVIEW_NO_UPDATE=1", "0.0.1", false, "1", "", false},
		{"re-exec child guard", "0.0.1", false, "", "v0.0.2", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("PREREVIEW_NO_UPDATE", c.envNo)
			t.Setenv("PREREVIEW_UPDATED", c.envUpdated)
			if got := ShouldAutoUpdate(c.version, c.noFlag); got != c.want {
				t.Errorf("ShouldAutoUpdate(%q,%v) = %v, want %v", c.version, c.noFlag, got, c.want)
			}
		})
	}
}

func TestDetectPackageManager(t *testing.T) {
	brew := PkgManager{"Homebrew", "brew upgrade prereview", "brew uninstall prereview"}
	scoop := PkgManager{"Scoop", "scoop update prereview", "scoop uninstall prereview"}
	cases := []struct {
		name    string
		exePath string
		wantPM  PkgManager
		wantOK  bool
	}{
		{"homebrew arm", "/opt/homebrew/Cellar/prereview/0.3.6/bin/prereview", brew, true},
		{"homebrew intel", "/usr/local/Cellar/prereview/0.3.6/bin/prereview", brew, true},
		{"scoop windows", `C:\Users\me\scoop\apps\prereview\current\prereview.exe`, scoop, true},
		{"scoop posix-sep", "/c/Users/me/scoop/apps/prereview/current/prereview.exe", scoop, true},
		{"system bin", "/usr/local/bin/prereview", PkgManager{}, false},
		{"go install bin", "/home/me/go/bin/prereview", PkgManager{}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pm, ok := DetectPackageManager(c.exePath)
			if ok != c.wantOK || pm != c.wantPM {
				t.Errorf("DetectPackageManager(%q) = (%+v,%v), want (%+v,%v)",
					c.exePath, pm, ok, c.wantPM, c.wantOK)
			}
		})
	}
}

func TestCheckForUpdate_AlreadyCurrent(t *testing.T) {
	srv, hits, _, _ := releaseServer(t, "v0.0.1", "x", serverOpts{})
	tag, archiveURL, checksumsURL, newer, err := checkForUpdate(context.Background(), srv.Client(), srv.URL, "0.0.1")
	if err != nil {
		t.Fatalf("checkForUpdate: %v", err)
	}
	if newer {
		t.Errorf("newer = true, want false for equal versions")
	}
	if tag != "v0.0.1" {
		t.Errorf("tag = %q, want v0.0.1", tag)
	}
	if archiveURL != "" || checksumsURL != "" {
		t.Errorf("URLs should be empty when not newer, got %q / %q", archiveURL, checksumsURL)
	}
	if hits.Load() != 1 {
		t.Errorf("releases endpoint hit %d times, want 1", hits.Load())
	}
}

func TestCheckForUpdate_NewerAvailable(t *testing.T) {
	srv, _, assetName, _ := releaseServer(t, "v0.0.2", "x", serverOpts{})
	tag, archiveURL, checksumsURL, newer, err := checkForUpdate(context.Background(), srv.Client(), srv.URL, "0.0.1")
	if err != nil {
		t.Fatalf("checkForUpdate: %v", err)
	}
	if !newer {
		t.Fatal("newer = false, want true")
	}
	if tag != "v0.0.2" {
		t.Errorf("tag = %q, want v0.0.2", tag)
	}
	if archiveURL == "" || checksumsURL == "" {
		t.Errorf("expected non-empty URLs, got archive=%q checksums=%q", archiveURL, checksumsURL)
	}
	if filepath.Base(archiveURL) != assetName {
		t.Errorf("archiveURL base = %q, want %q", filepath.Base(archiveURL), assetName)
	}
}

func TestSelfUpdate_DevSkip(t *testing.T) {
	srv, hits, _, _ := releaseServer(t, "v9.9.9", "new", serverOpts{})
	target := filepath.Join(t.TempDir(), "prereview")
	seedFile(t, target, "old")

	tag, err := SelfUpdate(context.Background(), "dev", target, srv.URL, srv.Client(), t.TempDir(), true)
	if !errors.Is(err, ErrDevBuild) {
		t.Fatalf("err = %v, want ErrDevBuild", err)
	}
	if tag != "" {
		t.Errorf("tag = %q, want empty", tag)
	}
	if hits.Load() != 0 {
		t.Errorf("dev build must not contact GitHub; hits = %d", hits.Load())
	}
	if got := slurp(t, target); got != "old" {
		t.Errorf("target mutated to %q, want unchanged", got)
	}
}

func TestSelfUpdate_Throttled(t *testing.T) {
	srv, hits, _, _ := releaseServer(t, "v9.9.9", "new", serverOpts{})
	target := filepath.Join(t.TempDir(), "prereview")
	seedFile(t, target, "old")
	cacheDir := t.TempDir()

	// Pre-seed a fresh cache so the throttle window is active.
	if err := writeUpdateCache(cacheDir, "v0.0.1"); err != nil {
		t.Fatalf("seed cache: %v", err)
	}

	_, err := SelfUpdate(context.Background(), "0.0.1", target, srv.URL, srv.Client(), cacheDir, false)
	if !errors.Is(err, ErrThrottled) {
		t.Fatalf("err = %v, want ErrThrottled", err)
	}
	if hits.Load() != 0 {
		t.Errorf("throttled run must not contact GitHub; hits = %d", hits.Load())
	}

	// force=true (the --update path) must bypass the throttle.
	if _, err := SelfUpdate(context.Background(), "9.9.9", target, srv.URL, srv.Client(), cacheDir, true); !errors.Is(err, ErrAlreadyCurrent) {
		t.Fatalf("forced err = %v, want ErrAlreadyCurrent (throttle bypassed)", err)
	}
	if hits.Load() != 1 {
		t.Errorf("forced run should query once; hits = %d", hits.Load())
	}
}

// TestSelfUpdate_NotThrottledAfterInterval pins that once the cache is
// older than checkInterval, a normal (force=false) run re-checks
// GitHub instead of short-circuiting — i.e. the throttle window is
// honoured and bounded by checkInterval (referenced directly so this
// test tracks any future change to the interval).
func TestSelfUpdate_NotThrottledAfterInterval(t *testing.T) {
	srv, hits, _, _ := releaseServer(t, "v9.9.9", "new", serverOpts{})
	target := filepath.Join(t.TempDir(), "prereview")
	seedFile(t, target, "old")
	cacheDir := t.TempDir()

	// Write a cache stamped older than the throttle window.
	stale, err := json.Marshal(updateCache{
		CheckedAt: time.Now().Add(-2 * checkInterval),
		Latest:    "v0.0.1",
	})
	if err != nil {
		t.Fatalf("marshal cache: %v", err)
	}
	p := updateCachePath(cacheDir)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir cache: %v", err)
	}
	if err := os.WriteFile(p, stale, 0o644); err != nil {
		t.Fatalf("write stale cache: %v", err)
	}

	// current == server tag → it must have actually queried GitHub
	// (ErrAlreadyCurrent), proving the stale cache did NOT throttle.
	_, err = SelfUpdate(context.Background(), "9.9.9", target, srv.URL, srv.Client(), cacheDir, false)
	if !errors.Is(err, ErrAlreadyCurrent) {
		t.Fatalf("stale cache must not throttle; err = %v, want ErrAlreadyCurrent", err)
	}
	if hits.Load() != 1 {
		t.Errorf("expected one GitHub query after the interval elapsed; hits = %d", hits.Load())
	}
}

func TestSelfUpdate_UpdatesAndSwaps(t *testing.T) {
	srv, _, _, _ := releaseServer(t, "v0.0.2", "NEW BINARY BYTES", serverOpts{})
	dir := t.TempDir()
	target := filepath.Join(dir, "prereview")
	seedFile(t, target, "OLD BINARY BYTES")
	cacheDir := t.TempDir()

	tag, err := SelfUpdate(context.Background(), "0.0.1", target, srv.URL, srv.Client(), cacheDir, false)
	if err != nil {
		t.Fatalf("SelfUpdate: %v", err)
	}
	if tag != "v0.0.2" {
		t.Errorf("tag = %q, want v0.0.2", tag)
	}
	if got := slurp(t, target); got != "NEW BINARY BYTES" {
		t.Errorf("target content = %q, want swapped to new", got)
	}
	if _, latest := readUpdateCache(cacheDir); latest != "v0.0.2" {
		t.Errorf("cache latest = %q, want v0.0.2", latest)
	}
}

func TestSelfUpdate_BadChecksum(t *testing.T) {
	srv, _, _, _ := releaseServer(t, "v0.0.2", "NEW", serverOpts{wrongChecksum: true})
	dir := t.TempDir()
	target := filepath.Join(dir, "prereview")
	seedFile(t, target, "OLD")
	cacheDir := t.TempDir()

	_, err := SelfUpdate(context.Background(), "0.0.1", target, srv.URL, srv.Client(), cacheDir, false)
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("err = %v, want ErrChecksumMismatch", err)
	}
	if got := slurp(t, target); got != "OLD" {
		t.Errorf("target mutated to %q on bad checksum, want untouched", got)
	}
	// A failed download must NOT write the cache (else retries throttle).
	if _, latest := readUpdateCache(cacheDir); latest != "" {
		t.Errorf("cache written %q on failed update, want unwritten", latest)
	}
}

func TestSelfUpdate_FailedDownloadDoesNotThrottle(t *testing.T) {
	srv, hits, _, _ := releaseServer(t, "v0.0.2", "NEW", serverOpts{archiveStatus: http.StatusInternalServerError})
	dir := t.TempDir()
	target := filepath.Join(dir, "prereview")
	seedFile(t, target, "OLD")
	cacheDir := t.TempDir()

	if _, err := SelfUpdate(context.Background(), "0.0.1", target, srv.URL, srv.Client(), cacheDir, false); err == nil {
		t.Fatal("expected error from failed archive download, got nil")
	}
	if _, latest := readUpdateCache(cacheDir); latest != "" {
		t.Fatalf("cache written %q after failed download — would throttle retries", latest)
	}
	// Next launch must hit GitHub again rather than be throttled.
	_, err := SelfUpdate(context.Background(), "0.0.1", target, srv.URL, srv.Client(), cacheDir, false)
	if err == nil {
		t.Fatal("expected error on retry, got nil")
	}
	if hits.Load() != 2 {
		t.Errorf("releases endpoint hit %d times, want 2 (no throttle after failure)", hits.Load())
	}
}

func TestSelfUpdate_UnwritableTarget(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root bypasses directory permission bits")
	}
	srv, hits, _, _ := releaseServer(t, "v0.0.2", "NEW", serverOpts{})
	dir := filepath.Join(t.TempDir(), "ro")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	target := filepath.Join(dir, "prereview")
	seedFile(t, target, "OLD")
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	// Restore so t.TempDir cleanup can remove the tree.
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	_, err := SelfUpdate(context.Background(), "0.0.1", target, srv.URL, srv.Client(), t.TempDir(), true)
	if !errors.Is(err, ErrUnwritable) {
		t.Fatalf("err = %v, want ErrUnwritable", err)
	}
	if hits.Load() != 0 {
		t.Errorf("unwritable target should fail before contacting GitHub; hits = %d", hits.Load())
	}
	if got := slurp(t, target); got != "OLD" {
		t.Errorf("target mutated to %q, want untouched", got)
	}
}

func TestResolveExecutablePath(t *testing.T) {
	// Under `go test` the test binary lives in the go-build cache, so
	// this asserts the go-build guard fires (the same guard that stops
	// `go run`-launched processes from trying to self-update). The
	// happy path (a real installed binary returning its resolved path)
	// is covered by the P5 end-to-end manual check.
	p, err := ResolveExecutablePath()
	if !errors.Is(err, ErrGoBuildCache) {
		t.Fatalf("ResolveExecutablePath under go test = (%q, %v), want ErrGoBuildCache", p, err)
	}
	if p != "" {
		t.Errorf("path = %q, want empty when ErrGoBuildCache", p)
	}
}

func seedFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func slurp(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}
