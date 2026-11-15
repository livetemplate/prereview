package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
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

	"github.com/livetemplate/prereview/internal/review"
)

// libraryReviewFile records the review server started on the library folder
// (Review in prereview), so the next click reuses it instead of starting another.
const libraryReviewFile = "library-review.json"

type libraryReview struct {
	PID  int `json:"pid"`
	Port int `json:"port"`
}

// libraryReviewHandler serves /_library-review: it starts (or reuses) a review
// server on the library folder — `prereview --agent <library>`, bound to the same
// interface this request arrived on — and redirects to it, so comments and
// suggestions on your library files go to your agent like any review's.
//
// A GET that starts a process must not be triggerable from another site: only a
// same-origin navigation (the Library's link) or one the user typed is honoured.
func libraryReviewHandler(libraryDir string) http.HandlerFunc {
	var mu sync.Mutex
	return func(w http.ResponseWriter, r *http.Request) {
		if !sameOriginNavigation(r) {
			http.Error(w, "open the library review from the Library's link", http.StatusForbidden)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		port, err := ensureLibraryReview(r, libraryDir)
		if err != nil {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusConflict)
			fmt.Fprintf(w, "<!doctype html><meta name=viewport content='width=device-width'><p>Couldn't open a review of the library: %s</p>", html.EscapeString(err.Error()))
			return
		}
		host := r.Host
		if h, _, err := net.SplitHostPort(r.Host); err == nil {
			host = h
		}
		http.Redirect(w, r, (&url.URL{Scheme: "http", Host: net.JoinHostPort(host, strconv.Itoa(port)), Path: "/"}).String(), http.StatusSeeOther)
	}
}

// sameOriginNavigation accepts a navigation from this origin (Sec-Fetch-Site
// same-origin) or one the user started (none: typed, bookmarked); a browser
// without Fetch Metadata must send a same-host Referer instead.
func sameOriginNavigation(r *http.Request) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "same-origin", "none":
		return true
	case "":
		ref, err := url.Parse(r.Referer())
		return err == nil && ref.Host == r.Host
	}
	return false
}

// ensureLibraryReview returns the port of the library review server, starting one
// when none recorded is alive.
func ensureLibraryReview(r *http.Request, libraryDir string) (int, error) {
	store := review.LibraryStoreDir(libraryDir)
	rec := filepath.Join(store, libraryReviewFile)
	var cur libraryReview
	if b, err := os.ReadFile(rec); err == nil && json.Unmarshal(b, &cur) == nil && processAlive(cur.PID) && cur.Port > 0 {
		return cur.Port, nil
	}
	if err := os.MkdirAll(store, 0o755); err != nil {
		return 0, err
	}
	bind := "127.0.0.1"
	if a, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr); ok {
		if h, _, err := net.SplitHostPort(a.String()); err == nil {
			bind = h
		}
	}
	exe, err := os.Executable()
	if err != nil {
		return 0, err
	}
	logf, err := os.OpenFile(filepath.Join(store, "library-review.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return 0, err
	}
	cmd := exec.Command(exe, "--agent", "--host", bind, "--port", "0", libraryDir)
	cmd.Stderr = logf
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		logf.Close()
		return 0, err
	}
	if err := cmd.Start(); err != nil {
		logf.Close()
		return 0, err
	}
	go func() { _ = cmd.Wait(); logf.Close() }()

	ready := make(chan int, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		sent := false
		for sc.Scan() {
			if u, ok := strings.CutPrefix(sc.Text(), "READY "); ok && !sent {
				if pu, err := url.Parse(strings.TrimSpace(u)); err == nil {
					if p, err := strconv.Atoi(pu.Port()); err == nil {
						ready <- p
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
	case p, ok := <-ready:
		if !ok {
			return 0, errors.New("it exited before it was ready (see " + filepath.Join(store, "library-review.log") + "); a review of the library may already be running")
		}
		b, _ := json.Marshal(libraryReview{PID: cmd.Process.Pid, Port: p})
		if err := os.WriteFile(rec, b, 0o644); err != nil {
			return 0, err
		}
		return p, nil
	case <-time.After(15 * time.Second):
		_ = cmd.Process.Kill()
		return 0, errors.New("it didn't start within 15s")
	}
}
