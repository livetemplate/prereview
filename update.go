package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/minio/selfupdate"
)

// Sentinel errors. Their message text is what the `--update` command
// prints to the user, so keep them human-readable. The on-run path
// treats the "expected, not really a failure" ones as no-ops.
var (
	errDevBuild         = errors.New("not a released build (version is \"dev\"); install a release from https://github.com/livetemplate/prereview/releases or via `go install github.com/livetemplate/prereview@latest`")
	errGoBuildCache     = errors.New("running from the go build cache (go run); self-update is only for installed release binaries")
	errAlreadyCurrent   = errors.New("already on the latest version")
	errThrottled        = errors.New("update check skipped (already checked within the last 24h)")
	errUnwritable       = errors.New("the prereview binary is not writable; reinstall it somewhere you own or run with elevated permissions")
	errChecksumMismatch = errors.New("checksum mismatch: the downloaded archive is corrupt or has been tampered with")
)

const (
	githubAPIBase  = "https://api.github.com"
	updateRepoPath = "/repos/livetemplate/prereview/releases/latest"
	checkInterval  = 24 * time.Hour
	checksumsName  = "checksums.txt"
)

// binaryName is the bare binary filename inside a release archive for
// the running platform (goreleaser ships it un-nested).
func binaryName() string {
	if runtime.GOOS == "windows" {
		return "prereview.exe"
	}
	return "prereview"
}

// archiveExt is the release archive extension for the running platform
// (matches .goreleaser.yml: zip on windows, tar.gz elsewhere).
func archiveExt() string {
	if runtime.GOOS == "windows" {
		return "zip"
	}
	return "tar.gz"
}

// compareVersions compares dotted numeric versions, ignoring a leading
// "v" and any pre-release suffix on a component. Returns -1 if a<b, 0
// if equal, +1 if a>b. Unparseable components count as 0, so a garbled
// remote tag never reports "newer" and never triggers an update.
func compareVersions(a, b string) int {
	pa := splitVersion(a)
	pb := splitVersion(b)
	for i := range 3 {
		if pa[i] < pb[i] {
			return -1
		}
		if pa[i] > pb[i] {
			return 1
		}
	}
	return 0
}

func splitVersion(v string) [3]int {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	var out [3]int
	parts := strings.SplitN(v, ".", 3)
	for i := range min(len(parts), 3) {
		seg := parts[i]
		// Drop a pre-release/build suffix like "1-rc2" or "1+meta".
		for j := range len(seg) {
			if seg[j] < '0' || seg[j] > '9' {
				seg = seg[:j]
				break
			}
		}
		n, _ := strconv.Atoi(seg)
		out[i] = n
	}
	return out
}

// resolveExecutablePath returns the real on-disk path of the running
// binary with symlinks resolved (Homebrew/asdf/nix wrappers), or
// errGoBuildCache when running via `go run`.
func resolveExecutablePath() (string, error) {
	p, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve executable: %w", err)
	}
	if rp, err := filepath.EvalSymlinks(p); err == nil {
		p = rp
	}
	if strings.Contains(p, string(filepath.Separator)+"go-build") {
		return "", errGoBuildCache
	}
	return p, nil
}

// shouldAutoUpdate is the pure predicate gating the on-run check. It is
// false for dev builds, when opted out via flag/env, and inside a
// re-exec'd child (PREREVIEW_UPDATED set) to prevent an update loop.
func shouldAutoUpdate(version string, noUpdateFlag bool) bool {
	return version != "dev" &&
		!noUpdateFlag &&
		os.Getenv("PREREVIEW_NO_UPDATE") != "1" &&
		os.Getenv("PREREVIEW_UPDATED") == ""
}

type updateCache struct {
	CheckedAt time.Time `json:"checked_at"`
	Latest    string    `json:"latest"`
}

func updateCachePath(cacheDir string) string {
	return filepath.Join(cacheDir, "prereview", "update-check.json")
}

// readUpdateCache never errors: a missing or corrupt cache is simply
// "never checked" (zero time), which forces a fresh check.
func readUpdateCache(cacheDir string) (time.Time, string) {
	if cacheDir == "" {
		return time.Time{}, ""
	}
	b, err := os.ReadFile(updateCachePath(cacheDir))
	if err != nil {
		return time.Time{}, ""
	}
	var c updateCache
	if json.Unmarshal(b, &c) != nil {
		return time.Time{}, ""
	}
	return c.CheckedAt, c.Latest
}

func writeUpdateCache(cacheDir, latest string) error {
	if cacheDir == "" {
		return nil
	}
	p := updateCachePath(cacheDir)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(updateCache{CheckedAt: time.Now().UTC(), Latest: latest})
	if err != nil {
		return err
	}
	return os.WriteFile(p, b, 0o644)
}

type ghRelease struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

// checkForUpdate asks GitHub for the latest release and resolves the
// download URLs for this platform. archiveURL/checksumsURL are only set
// when a strictly newer version exists. apiBase is parameterised so
// tests can point it at an httptest server; the asset download URLs are
// taken verbatim from the response (they point at github.com in prod).
func checkForUpdate(ctx context.Context, client *http.Client, apiBase, current string) (tag, archiveURL, checksumsURL string, newer bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiBase+updateRepoPath, nil)
	if err != nil {
		return "", "", "", false, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := client.Do(req)
	if err != nil {
		return "", "", "", false, fmt.Errorf("query latest release: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusForbidden, http.StatusTooManyRequests:
		// Anonymous GitHub API rate limit hit — treat as "skip", not a
		// hard failure, so a normal run still starts.
		return "", "", "", false, errThrottled
	default:
		return "", "", "", false, fmt.Errorf("latest release: unexpected status %s", resp.Status)
	}

	var rel ghRelease
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&rel); err != nil {
		return "", "", "", false, fmt.Errorf("decode release json: %w", err)
	}
	tag = rel.TagName
	tagVer := strings.TrimPrefix(tag, "v")
	if compareVersions(tagVer, strings.TrimPrefix(current, "v")) <= 0 {
		return tag, "", "", false, nil
	}

	wantArchive := fmt.Sprintf("prereview_%s_%s_%s.%s", tagVer, runtime.GOOS, runtime.GOARCH, archiveExt())
	for _, a := range rel.Assets {
		switch a.Name {
		case wantArchive:
			archiveURL = a.URL
		case checksumsName:
			checksumsURL = a.URL
		}
	}
	if archiveURL == "" {
		return tag, "", "", false, fmt.Errorf("release %s has no asset %q", tag, wantArchive)
	}
	if checksumsURL == "" {
		return tag, "", "", false, fmt.Errorf("release %s has no %s", tag, checksumsName)
	}
	return tag, archiveURL, checksumsURL, true, nil
}

// performUpdate downloads the archive, verifies its sha256 against the
// release checksums.txt, extracts the binary, and atomically swaps it
// over targetPath via minio/selfupdate (which handles the cross-platform
// in-use-file replace, incl. Windows rename-aside).
func performUpdate(ctx context.Context, client *http.Client, archiveURL, checksumsURL, targetPath string) error {
	sums, err := fetchChecksums(ctx, client, checksumsURL)
	if err != nil {
		return err
	}

	archiveBase := path.Base(mustURLPath(archiveURL))
	wantSum, ok := sums[archiveBase]
	if !ok {
		return fmt.Errorf("%s has no entry for %s", checksumsName, archiveBase)
	}

	// Download into the target's directory so the later atomic rename by
	// selfupdate is same-filesystem; remove the temp on every path.
	tmp, err := os.CreateTemp(filepath.Dir(targetPath), ".prereview-update-*")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if err := downloadVerified(ctx, client, archiveURL, tmp, wantSum); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}

	binReader, closer, err := openArchiveBinary(tmpPath, archiveBase)
	if err != nil {
		return err
	}
	defer closer()

	if err := selfupdate.Apply(binReader, selfupdate.Options{
		TargetPath: targetPath,
		TargetMode: 0o755,
	}); err != nil {
		return fmt.Errorf("apply update: %w", err)
	}
	return nil
}

func mustURLPath(rawURL string) string {
	if i := strings.IndexByte(rawURL, '?'); i >= 0 {
		rawURL = rawURL[:i]
	}
	return rawURL
}

func fetchChecksums(ctx context.Context, client *http.Client, url string) (map[string]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch checksums: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch checksums: status %s", resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read checksums: %w", err)
	}
	sums := make(map[string]string)
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		// sha256sum format: "<hex>  <name>"; some tools prefix the name
		// with '*' for binary mode.
		sums[strings.TrimPrefix(f[len(f)-1], "*")] = strings.ToLower(f[0])
	}
	return sums, nil
}

func downloadVerified(ctx context.Context, client *http.Client, url string, dst io.Writer, wantSum string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download archive: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download archive: status %s", resp.Status)
	}
	h := sha256.New()
	// 100 MB hard cap guards against a hostile/broken server streaming
	// unbounded data; real archives are a few MB.
	if _, err := io.Copy(io.MultiWriter(dst, h), io.LimitReader(resp.Body, 100<<20)); err != nil {
		return fmt.Errorf("download archive: %w", err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, wantSum) {
		return errChecksumMismatch
	}
	return nil
}

// openArchiveBinary returns a reader over the prereview binary entry
// inside the downloaded archive. archiveBase decides tar.gz vs zip.
func openArchiveBinary(archivePath, archiveBase string) (io.Reader, func(), error) {
	want := binaryName()

	if strings.HasSuffix(archiveBase, ".zip") {
		zr, err := zip.OpenReader(archivePath)
		if err != nil {
			return nil, func() {}, fmt.Errorf("open zip: %w", err)
		}
		for _, f := range zr.File {
			if path.Base(f.Name) != want {
				continue
			}
			rc, err := f.Open()
			if err != nil {
				zr.Close()
				return nil, func() {}, fmt.Errorf("open %s in zip: %w", want, err)
			}
			return rc, func() { rc.Close(); zr.Close() }, nil
		}
		zr.Close()
		return nil, func() {}, fmt.Errorf("%s not found in archive", want)
	}

	f, err := os.Open(archivePath)
	if err != nil {
		return nil, func() {}, err
	}
	gz, err := gzip.NewReader(f)
	if err != nil {
		f.Close()
		return nil, func() {}, fmt.Errorf("gunzip: %w", err)
	}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			gz.Close()
			f.Close()
			return nil, func() {}, fmt.Errorf("read tar: %w", err)
		}
		if hdr.Typeflag == tar.TypeReg && path.Base(hdr.Name) == want {
			return tr, func() { gz.Close(); f.Close() }, nil
		}
	}
	gz.Close()
	f.Close()
	return nil, func() {}, fmt.Errorf("%s not found in archive", want)
}

// selfUpdate is the single orchestrator used by both `--update`
// (force=true, bypasses the 24h throttle) and the on-run check
// (force=false). On success it returns the new tag and nil; the
// "expected non-update" outcomes are returned as sentinel errors so the
// caller can decide whether to surface or silently ignore them.
func selfUpdate(ctx context.Context, current, targetPath, apiBase string, client *http.Client, cacheDir string, force bool) (newTag string, err error) {
	if current == "dev" {
		return "", errDevBuild
	}

	if !force {
		if checkedAt, _ := readUpdateCache(cacheDir); !checkedAt.IsZero() && time.Since(checkedAt) < checkInterval {
			return "", errThrottled
		}
	}

	if perr := (&selfupdate.Options{TargetPath: targetPath}).CheckPermissions(); perr != nil {
		return "", fmt.Errorf("%w (%v)", errUnwritable, perr)
	}

	tag, archiveURL, checksumsURL, newer, err := checkForUpdate(ctx, client, apiBase, current)
	if err != nil {
		return "", err
	}

	if !newer {
		// Steady state (the dominant case): record the check so the
		// throttle holds regardless of launch frequency.
		_ = writeUpdateCache(cacheDir, tag)
		return tag, errAlreadyCurrent
	}
	if err := performUpdate(ctx, client, archiveURL, checksumsURL, targetPath); err != nil {
		// Do NOT write the cache on a failed download — otherwise a
		// transient CDN/timeout/checksum failure would throttle retries
		// for 24h, stranding the user on the old binary.
		return "", err
	}
	_ = writeUpdateCache(cacheDir, tag)
	return tag, nil
}

// reexec replaces the current process with the freshly-installed binary
// so the in-flight invocation runs the new version. PREREVIEW_UPDATED
// makes the child skip its own update check (loop guard). On unix this
// never returns on success (the image is replaced); on Windows it spawns
// a child, waits, and exits with the child's status.
func reexec(exe, newTag string) error {
	env := append(os.Environ(), "PREREVIEW_UPDATED="+newTag)
	if runtime.GOOS == "windows" {
		cmd := exec.Command(exe, os.Args[1:]...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		cmd.Env = env
		if err := cmd.Run(); err != nil {
			if ee, ok := errors.AsType[*exec.ExitError](err); ok {
				os.Exit(ee.ExitCode())
			}
			return err
		}
		os.Exit(0)
	}
	return syscall.Exec(exe, os.Args, env)
}
