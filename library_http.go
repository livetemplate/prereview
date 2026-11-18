package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/livetemplate/prereview/internal/library"
	"github.com/livetemplate/prereview/internal/review"
	"github.com/livetemplate/prereview/internal/skills"
)

// libraryReviewFile records the review server started on the library folder
// (Review in prereview), so the next click reuses it instead of starting another.
const libraryReviewFile = "library-review.json"

type libraryReview struct {
	PID   int    `json:"pid"`
	Port  int    `json:"port"`
	Token string `json:"token,omitempty"` // the child's launch token, from its READY line
}

// libraryReviewHandler serves /_library-review: it starts (or reuses) a review
// server on the library folder — `prereview --agent <library>`, bound to the same
// interface this request arrived on — and redirects to it, so comments and
// suggestions on your library files go to your agent like any review's.
//
// It starts a process, so it's registered behind guardLibraryRoute.
func libraryReviewHandler(libraryDir string) http.HandlerFunc {
	var mu sync.Mutex
	return func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		child, err := ensureLibraryReview(r, libraryDir)
		if err != nil {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusConflict)
			fmt.Fprintf(w, "<!doctype html><meta name=viewport content='width=device-width'><p>Couldn't open a review of the library: %s</p>", html.EscapeString(err.Error()))
			return
		}
		dest := (&url.URL{Scheme: "http", Host: net.JoinHostPort(hostOnly(r.Host), strconv.Itoa(child.Port)), Path: "/"}).String()
		if child.Token != "" {
			dest = launchToken{value: child.Token}.url(dest)
		}
		http.Redirect(w, r, dest, http.StatusSeeOther)
	}
}

// guardLibraryRoute wraps every library route (they start a process, hand out
// the learnings or write them): a cross-site page or a DNS-rebinding one gets 403.
func guardLibraryRoute(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !sameOriginNavigation(r) {
			http.Error(w, "use the Library's own links", http.StatusForbidden)
			return
		}
		h(w, r)
	}
}

// hostOnly strips the port from a host[:port].
func hostOnly(hostport string) string {
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		return h
	}
	return hostport
}

// localIP is the address the request arrived on ("" when unknown).
func localIP(r *http.Request) string {
	if a, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr); ok {
		return hostOnly(a.String())
	}
	return ""
}

// sameOriginNavigation accepts a navigation from this origin (Sec-Fetch-Site
// same-origin) or one the user started (none: typed, bookmarked); a browser
// without Fetch Metadata must send a same-host Referer instead. The Host must be
// one this server is really reached by (trustedHost): under DNS rebinding an
// attacker's name resolves here and its pages count as "same-origin".
func sameOriginNavigation(r *http.Request) bool {
	if !trustedHost(r) {
		return false
	}
	switch r.Header.Get("Sec-Fetch-Site") {
	case "same-origin", "none":
		return true
	case "":
		ref, err := url.Parse(r.Referer())
		return err == nil && ref.Host == r.Host
	}
	return false
}

// trustedHost reports a Host header naming this machine: localhost, an IP literal
// (loopback, or the address the request arrived on) or a Tailscale MagicDNS name
// — never an arbitrary domain that merely resolves here.
func trustedHost(r *http.Request) bool {
	host := strings.TrimSuffix(strings.ToLower(hostOnly(r.Host)), ".")
	if host == "localhost" || strings.HasSuffix(host, ".ts.net") {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || net.ParseIP(localIP(r)).Equal(ip)
}

// ensureLibraryReview returns the library review server's record (its port and
// launch token), starting one when none recorded is alive.
func ensureLibraryReview(r *http.Request, libraryDir string) (libraryReview, error) {
	store := review.LibraryStoreDir(libraryDir)
	rec := filepath.Join(store, libraryReviewFile)
	// Reuse only a server that is still the library store's owner: its pid holds
	// the store's server.pid (a recycled pid, or a stale record, doesn't).
	var cur libraryReview
	if b, err := os.ReadFile(rec); err == nil && json.Unmarshal(b, &cur) == nil && cur.Port > 0 && processAlive(cur.PID) {
		if owner, ok := readServerPID(filepath.Join(store, serverPIDFileName)); ok && owner == cur.PID {
			return cur, nil
		}
	}
	if err := os.MkdirAll(store, 0o755); err != nil {
		return libraryReview{}, err
	}
	bind := localIP(r)
	if bind == "" {
		bind = "127.0.0.1"
	}
	exe, err := os.Executable()
	if err != nil {
		return libraryReview{}, err
	}
	logf, err := os.OpenFile(filepath.Join(store, "library-review.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return libraryReview{}, err
	}
	// --no-update: the parent already decided about updates for this launch.
	cmd := exec.Command(exe, "--agent", "--no-update", "--host", bind, "--port", "0", libraryDir)
	cmd.Stderr = logf
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		logf.Close()
		return libraryReview{}, err
	}
	if err := cmd.Start(); err != nil {
		logf.Close()
		return libraryReview{}, err
	}
	go func() {
		_ = cmd.Wait()
		logf.Close()
		// It's gone: drop the record, unless a newer server has replaced it.
		if b, err := os.ReadFile(rec); err == nil {
			var r libraryReview
			if json.Unmarshal(b, &r) == nil && r.PID == cmd.Process.Pid {
				_ = os.Remove(rec)
			}
		}
	}()

	ready := make(chan libraryReview, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		sent := false
		for sc.Scan() {
			if u, ok := strings.CutPrefix(sc.Text(), "READY "); ok && !sent {
				if pu, err := url.Parse(strings.TrimSpace(u)); err == nil {
					if p, err := strconv.Atoi(pu.Port()); err == nil {
						ready <- libraryReview{PID: cmd.Process.Pid, Port: p, Token: pu.Query().Get(tokenParam)}
						sent = true
					}
				}
			}
		}
		_, _ = io.Copy(io.Discard, stdout) // keep the agent stream drained
		if !sent {
			close(ready)
		}
	}()
	select {
	case child, ok := <-ready:
		if !ok {
			return libraryReview{}, errors.New("it exited before it was ready (see " + filepath.Join(store, "library-review.log") + "); a review of the library may already be running")
		}
		b, _ := json.Marshal(child)
		// 0600: the record holds the child's token. WriteFile keeps an existing
		// file's mode, so set it too.
		if err := os.WriteFile(rec, b, 0o600); err != nil {
			return libraryReview{}, err
		}
		_ = os.Chmod(rec, 0o600)
		return child, nil
	case <-time.After(15 * time.Second):
		_ = cmd.Process.Kill()
		return libraryReview{}, errors.New("it didn't start within 15s")
	}
}

// libraryExportHandler serves /_library-export: this machine's learnings as one
// log file (tombstones included, so a delete travels) to carry to another
// machine and import there.
func libraryExportHandler(libraryDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := fmt.Sprintf("prereview-learnings-%s-%s.jsonl", slugBase(library.Machine()), time.Now().Format("2006-01-02"))
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
		if err := library.Export(libraryDir, w); err != nil {
			slog.Warn("library export", "err", err) // headers are out; the download is short
		}
	}
}

// maxImportBytes bounds an imported learnings file.
const maxImportBytes = 16 << 20

// libraryImportHandler accepts /_library-import: a learnings file exported on
// another machine, merged into this library (newer records win, the same file
// twice is a no-op). A plain form post (the page reloads), never a cross-site one.
func libraryImportHandler(libraryDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "import from the Library's form", http.StatusForbidden)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxImportBytes)
		f, _, err := r.FormFile("file")
		if err != nil {
			importResult(w, http.StatusBadRequest, "Choose a learnings file exported from prereview first.")
			return
		}
		defer f.Close()
		n, toAccept, err := library.Import(libraryDir, f)
		if err != nil {
			importResult(w, http.StatusInternalServerError, err.Error())
			return
		}
		msg := fmt.Sprintf("Imported %d learning%s.", n, map[bool]string{true: "", false: "s"}[n == 1]) // a one-off; no plural helper in main
		if toAccept > 0 {
			msg += fmt.Sprintf(" %d of them wait for your accept in Proposed: an imported learning never changes your skills or checks until you accept it here.", toAccept)
		}
		if n == 0 {
			msg = "Nothing new: this library already has everything in that file."
		}
		importResult(w, http.StatusOK, msg)
	}
}

// importResult is the page an import lands on, with the way back.
func importResult(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	fmt.Fprintf(w, `<!doctype html><meta name=viewport content="width=device-width"><title>Import learnings</title>`+
		`<link rel="stylesheet" href="/pico.min.css"><main class="container"><p class="import-result">%s</p><p><a href="/">Back to the review</a> — the new learnings are in Library → Learnings.</p></main>`,
		html.EscapeString(msg))
}

// librarySkillPatchHandler serves /_library-skill-patch?name=…: the overlay of
// an imported skill (the learnings scoped to it) as a unified diff, for you to
// apply in the skill's folder. prereview never writes the skill itself.
func librarySkillPatchHandler(libraryDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("name")
		for _, imp := range skills.Imports(libraryDir) {
			if imp.Name != name {
				continue
			}
			patch, err := skills.OverlayPatch(imp, skills.Overlay(library.LoadLearnings(libraryDir), name))
			if err != nil || patch == "" {
				http.Error(w, "nothing to export for "+name, http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "text/x-diff; charset=utf-8")
			w.Header().Set("Content-Disposition", `attachment; filename="`+slugBase(name)+`-learned.patch"`)
			_, _ = io.WriteString(w, patch)
			return
		}
		http.Error(w, "no imported skill "+name, http.StatusNotFound)
	}
}
