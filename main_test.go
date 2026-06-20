package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestReviewPath(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"no args defaults to cwd", nil, "."},
		{"empty slice defaults to cwd", []string{}, "."},
		{"positional file", []string{"./PLAN.md"}, "./PLAN.md"},
		{"positional dir", []string{"../service"}, "../service"},
		{"first positional wins", []string{"a", "b"}, "a"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := reviewPath(c.args); got != c.want {
				t.Errorf("reviewPath(%v) = %q, want %q", c.args, got, c.want)
			}
		})
	}
}

func TestInstallSkill(t *testing.T) {
	home := t.TempDir()

	path, err := installSkill(home)
	if err != nil {
		t.Fatalf("installSkill: %v", err)
	}

	wantPath := filepath.Join(home, ".claude", "skills", "prereview", "SKILL.md")
	if path != wantPath {
		t.Errorf("returned path = %q, want %q", path, wantPath)
	}

	// The filename must be exactly uppercase SKILL.md — a lowercase
	// skill.md is silently ignored by Claude Code (the trap this
	// command exists to prevent).
	if _, err := os.Stat(wantPath); err != nil {
		t.Fatalf("SKILL.md not written: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "skills", "prereview", "skill.md")); !os.IsNotExist(err) {
		t.Errorf("a lowercase skill.md must NOT be created (got err=%v)", err)
	}

	got, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatalf("read SKILL.md: %v", err)
	}
	if string(got) != skillMD {
		t.Errorf("SKILL.md content doesn't match the embedded skill")
	}

	ref, err := os.ReadFile(filepath.Join(home, ".claude", "skills", "prereview", "reference.md"))
	if err != nil {
		t.Fatalf("read reference.md: %v", err)
	}
	if string(ref) != skillReferenceMD {
		t.Errorf("reference.md content doesn't match the embedded reference")
	}

	// Idempotent: re-running overwrites cleanly (skill upgrade path).
	if _, err := installSkill(home); err != nil {
		t.Fatalf("installSkill second run: %v", err)
	}
}

// TestSyncInstalledSkill covers the auto-refresh contract used by every
// binary-upgrade path (--update re-exec, brew, scoop, go install): keep an
// ALREADY-installed skill matching the embedded copy, never create one for
// a user who opted out.
func TestSyncInstalledSkill(t *testing.T) {
	skillPathIn := func(home string) string {
		return filepath.Join(home, ".claude", "skills", "prereview", "SKILL.md")
	}
	refPathIn := func(home string) string {
		return filepath.Join(home, ".claude", "skills", "prereview", "reference.md")
	}

	t.Run("not installed → no-op, never creates the skill", func(t *testing.T) {
		home := t.TempDir()
		changed, err := syncInstalledSkill(home)
		if err != nil {
			t.Fatalf("syncInstalledSkill: %v", err)
		}
		if changed {
			t.Error("changed = true, want false (skill was never installed)")
		}
		if _, err := os.Stat(filepath.Join(home, ".claude", "skills", "prereview")); !os.IsNotExist(err) {
			t.Errorf("skill dir must NOT be created for an opted-out user (err=%v)", err)
		}
	})

	t.Run("installed and current → no-op", func(t *testing.T) {
		home := t.TempDir()
		if _, err := installSkill(home); err != nil {
			t.Fatalf("installSkill: %v", err)
		}
		changed, err := syncInstalledSkill(home)
		if err != nil {
			t.Fatalf("syncInstalledSkill: %v", err)
		}
		if changed {
			t.Error("changed = true, want false (already in sync)")
		}
	})

	t.Run("stale SKILL.md → rewritten to embedded copy", func(t *testing.T) {
		home := t.TempDir()
		if _, err := installSkill(home); err != nil {
			t.Fatalf("installSkill: %v", err)
		}
		// Simulate a binary upgrade: on-disk skill is from the old version.
		mustWriteFile(t, skillPathIn(home), []byte("# old skill from a previous prereview version"))

		changed, err := syncInstalledSkill(home)
		if err != nil {
			t.Fatalf("syncInstalledSkill: %v", err)
		}
		if !changed {
			t.Fatal("changed = false, want true (SKILL.md was stale)")
		}
		got, err := os.ReadFile(skillPathIn(home))
		if err != nil {
			t.Fatalf("read SKILL.md: %v", err)
		}
		if string(got) != skillMD {
			t.Error("SKILL.md was not refreshed to the embedded copy")
		}
	})

	t.Run("missing reference.md → restored", func(t *testing.T) {
		home := t.TempDir()
		if _, err := installSkill(home); err != nil {
			t.Fatalf("installSkill: %v", err)
		}
		// SKILL.md stays current but its companion vanished — sync restores it.
		if err := os.Remove(refPathIn(home)); err != nil {
			t.Fatalf("remove reference.md: %v", err)
		}
		changed, err := syncInstalledSkill(home)
		if err != nil {
			t.Fatalf("syncInstalledSkill: %v", err)
		}
		if !changed {
			t.Fatal("changed = false, want true (reference.md was missing)")
		}
		got, err := os.ReadFile(refPathIn(home))
		if err != nil {
			t.Fatalf("read reference.md: %v", err)
		}
		if string(got) != skillReferenceMD {
			t.Error("reference.md was not restored to the embedded copy")
		}
	})
}

// TestStaticFallback covers the two contracts staticFallback owns:
//
//  1. **Served from disk**: GET/HEAD for an allowlisted extension that
//     resolves to a real file under root returns the file (correct body
//     + Content-Type) and the next handler is NEVER reached.
//
//  2. **Falls through**: anything else (wrong method, non-allowlisted
//     extension, dot-component path, root "/") reaches the next handler
//     unchanged — preserving SPA, WebSocket, and POST/PUT routing.
//
// The "missing allowlisted file → 404" case is in between: a real intent
// signal (asking for an image), but no file → next NOT reached, 404
// returned, so DevTools shows the real problem instead of HTML masquerading
// as a PNG.
func TestStaticFallback(t *testing.T) {
	root := t.TempDir()
	// Real file at root/image.png — body is arbitrary, we only check
	// echo-through and Content-Type.
	pngBody := []byte("\x89PNG\r\n\x1a\nfake-but-recognisable")
	mustWriteFile(t, filepath.Join(root, "image.png"), pngBody)
	// Nested asset to verify subdirectory paths work.
	mustWriteFile(t, filepath.Join(root, "mockups", "screenshots", "dashboard.png"), pngBody)
	// A non-asset markdown file — must NOT be served (extension excluded).
	mustWriteFile(t, filepath.Join(root, "README.md"), []byte("# hi"))
	// A directory whose name happens to look like a PNG — info.IsDir
	// must reject it.
	if err := os.Mkdir(filepath.Join(root, "weird.png"), 0o755); err != nil {
		t.Fatalf("mkdir weird.png: %v", err)
	}
	// Dot-prefixed directories that must NEVER be reachable.
	mustWriteFile(t, filepath.Join(root, ".git", "config"), []byte("secret"))
	mustWriteFile(t, filepath.Join(root, ".prereview", "comments.csv"), []byte("secret"))
	// Allowlisted-ext file under a dot dir — proves dot-component check
	// fires before file-existence check.
	mustWriteFile(t, filepath.Join(root, ".prereview", "evil.png"), pngBody)
	// Uppercase extension — case-insensitive lookup must serve it.
	mustWriteFile(t, filepath.Join(root, "SHOUT.PNG"), pngBody)
	// Real HTML file — the iframe preview consumer drives this allowlist
	// entry, so cover both the served and missing-file behaviour.
	htmlBody := []byte("<!doctype html><html><body>hi</body></html>")
	mustWriteFile(t, filepath.Join(root, "index.html"), htmlBody)

	cases := []struct {
		name         string
		method       string
		path         string
		wantStatus   int
		wantBody     string // exact body (when served from disk); "" = no body check
		wantCType    string // prefix match on Content-Type (mime adds charset etc.)
		wantNextHit  bool   // did next.ServeHTTP get called?
	}{
		{
			name: "served: allowlisted ext, file present",
			method: http.MethodGet, path: "/image.png",
			wantStatus: 200, wantBody: string(pngBody),
			wantCType: "image/png",
		},
		{
			name: "served: nested path",
			method: http.MethodGet, path: "/mockups/screenshots/dashboard.png",
			wantStatus: 200, wantBody: string(pngBody),
			wantCType: "image/png",
		},
		{
			name: "served: uppercase extension (case-insensitive allowlist)",
			method: http.MethodGet, path: "/SHOUT.PNG",
			wantStatus: 200, wantBody: string(pngBody),
			wantCType: "image/png",
		},
		{
			name: "served: HEAD returns headers, empty body",
			method: http.MethodHead, path: "/image.png",
			wantStatus: 200, wantBody: "", wantCType: "image/png",
		},
		{
			name: "404: allowlisted ext, file missing",
			method: http.MethodGet, path: "/does-not-exist.png",
			wantStatus: 404,
		},
		{
			name: "served: .html for iframe preview",
			method: http.MethodGet, path: "/index.html",
			wantStatus: 200, wantBody: string(htmlBody),
			wantCType: "text/html",
		},
		{
			name: "404: .html missing (broken iframe shows blank, not the SPA shell)",
			method: http.MethodGet, path: "/missing.html",
			wantStatus: 404,
		},
		{
			name: "404: allowlisted ext, target is a directory",
			method: http.MethodGet, path: "/weird.png",
			wantStatus: 404,
		},
		{
			name: "404: traversal via ../ escapes root",
			method: http.MethodGet, path: "/../etc/passwd.png",
			// path.Clean normalises "/../etc/..." to "/etc/...", which
			// then misses under root → 404.
			wantStatus: 404,
		},
		{
			name: "404: URL-encoded traversal (stdlib decodes before us)",
			method: http.MethodGet, path: "/%2e%2e/etc/passwd.png",
			wantStatus: 404,
		},
		{
			// Catches both the SPA HTML render AND the WS upgrade handshake:
			// the WS client connects to ws://host/ (location.pathname is /),
			// and livetemplate dispatches WS vs HTML by Upgrade header,
			// not path. So our `cleaned == "/"` short-circuit hands BOTH
			// flavours to the LiveHandler untouched.
			name: "fallthrough: GET / (SPA HTML + WS upgrade share this path)",
			method: http.MethodGet, path: "/",
			wantNextHit: true,
		},
		{
			name: "fallthrough: .git/config (dot-component rejected)",
			method: http.MethodGet, path: "/.git/config",
			wantNextHit: true,
		},
		{
			name: "fallthrough: .prereview/evil.png (dot-component fires before ext + existence)",
			method: http.MethodGet, path: "/.prereview/evil.png",
			wantNextHit: true,
		},
		{
			name: "fallthrough: .md (extension not in allowlist)",
			method: http.MethodGet, path: "/README.md",
			wantNextHit: true,
		},
		{
			name: "fallthrough: no extension",
			method: http.MethodGet, path: "/some/spa/route",
			wantNextHit: true,
		},
		{
			name: "fallthrough: POST allowlisted-ext (writes never go through us)",
			method: http.MethodPost, path: "/image.png",
			wantNextHit: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var nextHit bool
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				nextHit = true
				w.WriteHeader(http.StatusTeapot) // sentinel, distinguishes from 200/404
			})
			h := staticFallback(root, next)

			req := httptest.NewRequest(tc.method, tc.path, nil)
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)

			if nextHit != tc.wantNextHit {
				t.Errorf("next.ServeHTTP called = %v, want %v (status=%d body=%q)",
					nextHit, tc.wantNextHit, rr.Code, rr.Body.String())
			}
			if !tc.wantNextHit {
				if rr.Code != tc.wantStatus {
					t.Errorf("status = %d, want %d (body=%q)", rr.Code, tc.wantStatus, rr.Body.String())
				}
				if tc.wantBody != "" && rr.Body.String() != tc.wantBody {
					t.Errorf("body = %q, want %q", rr.Body.String(), tc.wantBody)
				}
				if tc.wantCType != "" && !strings.HasPrefix(rr.Header().Get("Content-Type"), tc.wantCType) {
					t.Errorf("Content-Type = %q, want prefix %q",
						rr.Header().Get("Content-Type"), tc.wantCType)
				}
			}
		})
	}
}

// TestStaticFallbackSymlinkEscape pins the second traversal defense:
// a symlink inside root pointing OUTSIDE root must not exfiltrate the
// target file. EvalSymlinks-then-prefix-check catches this even though
// the path itself has no dot-components or ../ to spot syntactically.
func TestStaticFallbackSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on Windows; prereview e2e is Linux-only")
	}
	root := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.png")
	mustWriteFile(t, secret, []byte("secret-png-bytes"))

	// root/leak.png -> outside/secret.png
	if err := os.Symlink(secret, filepath.Join(root, "leak.png")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	var nextHit bool
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { nextHit = true })
	h := staticFallback(root, next)

	req := httptest.NewRequest(http.MethodGet, "/leak.png", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (body=%q)", rr.Code, rr.Body.String())
	}
	if nextHit {
		t.Errorf("next handler must NOT be called for an out-of-root symlink (it's an allowlisted ext signal)")
	}
	if strings.Contains(rr.Body.String(), "secret-png-bytes") {
		t.Errorf("leaked secret bytes: %q", rr.Body.String())
	}
}

func mustWriteFile(t *testing.T, path string, body []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

