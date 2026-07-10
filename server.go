package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/livetemplate/livetemplate"
	"github.com/livetemplate/prereview/csv"
	"github.com/livetemplate/prereview/gitdiff"
	"github.com/livetemplate/prereview/internal/assets"
	"github.com/livetemplate/prereview/internal/netaddr"
	"github.com/livetemplate/prereview/internal/proxy"
	"github.com/livetemplate/prereview/internal/review"
)

func run(repo, base, host string, explicitHost, explicitBase bool, port int, agentMode, skillUpdated bool, out string, replace bool) error {
	absRepo, err := filepath.Abs(repo)
	if err != nil {
		return fmt.Errorf("resolve repo path: %w", err)
	}
	tgt, err := resolveTarget(absRepo)
	if err != nil {
		return err
	}
	// Normalize: RepoPath is always a directory from here on, so the
	// .prereview/ store and every filepath.Join(absRepo, relPath) stay
	// valid whether the path was a repo, a loose dir, or a single file.
	absRepo = tgt.RepoPath

	// Clean working tree + no explicit --base: the HEAD diff is empty and there
	// is nothing to review, so diff against the empty tree instead — every
	// tracked line then appears added and is commentable. Best-effort: any git
	// error leaves base=HEAD and never fails the launch, and an explicitly
	// requested base is always honored as-is.
	if !explicitBase && base == "HEAD" && !tgt.NoGit {
		if clean, err := gitdiff.WorktreeClean(absRepo); err == nil && clean {
			if empty, err := gitdiff.EmptyTreeHash(absRepo); err == nil && empty != "" {
				base = empty
				slog.Info("clean working tree — reviewing the whole tree against the empty base")
			}
		}
	}

	// The .prereview/ store defaults to the review root; --out redirects it
	// (e.g. to keep a read-only checkout pristine). storeRoot is what we print
	// as REPO so the agent polls the right .prereview/.
	storeRoot, err := resolveStoreRoot(out, absRepo)
	if err != nil {
		return err
	}

	// Claim the per-store server lock BEFORE openStore: a non-replacing second
	// launch must error out before openStore wipes a live server's events.jsonl.
	release, err := claimServerLock(filepath.Join(storeRoot, ".prereview"), replace)
	if err != nil {
		return err
	}
	defer release()

	startedAt := time.Now()
	csvPath, csvWriter, err := openStore(storeRoot)
	if err != nil {
		return err
	}
	emitter := newStreamEmitter(agentMode, csvPath)

	// Load any existing comments from disk so a restart resumes the session.
	existing, err := csv.Read(csvPath)
	if err != nil {
		return fmt.Errorf("read existing csv: %w", err)
	}
	initialComments := make([]review.Comment, 0, len(existing))
	for _, r := range existing {
		initialComments = append(initialComments, review.Comment{
			ID:       r.ID,
			File:     r.File,
			FromLine: r.FromLine,
			ToLine:   r.ToLine,
			FromCol:  r.FromCol,
			ToCol:    r.ToCol,
			Side:     r.Side,
			Body:     r.Body,
			Created:  r.CreatedAt,
			Resolved: r.Resolved,
			Hidden:   r.Hidden,
			Draft:    r.Draft,
		})
	}

	// livetemplate.New requires templates as files on disk. Stage the embedded
	// split set to a temp dir for the lifetime of the process. Same workaround
	// used by tinkerdown — see tinkerdown/internal/server/websocket.go:465.
	tmplFiles, cleanup, err := stageTemplates(templatesFS)
	if err != nil {
		return fmt.Errorf("stage templates: %w", err)
	}
	defer cleanup()

	tmpl, err := livetemplate.New("prereview",
		livetemplate.WithParseFiles(tmplFiles...),
		// Diff payloads are large, highly repetitive HTML (1000+
		// `<div class="line-row"><button…` rows). permessage-deflate
		// compresses that ~10x on the wire — the dominant win for the
		// iPhone-over-Tailscale path where transfer time, not localhost
		// render, is the file-switch bottleneck.
		livetemplate.WithWebSocketCompression(),
	)
	if err != nil {
		return fmt.Errorf("livetemplate.New: %w", err)
	}

	// Quit action signals here so the HTTP server can shut down gracefully
	// AFTER the framework has rendered the "stopping…" state back to the client.
	shutdownReq := make(chan struct{}, 1)

	controller := &review.PrereviewController{
		RepoPath:    absRepo,
		Base:        base,
		NoGit:       tgt.NoGit,
		SingleFile:  tgt.SingleFile,
		CSVPath:     csvPath,
		UIPrefsPath: uiPrefsPath(),
		Version:     version,
		CSVWriter:   csvWriter,
		AgentMode:   agentMode,
		Emitter:     emitter,
		ShutdownReq: shutdownReq,
	}

	// Artifact version store (#90): the safety net that makes the agent's
	// continuous, uncommitted edits reversible. Baseline v0 is captured now —
	// once, at startup, BEFORE the agent touches anything and before any tab
	// connects — so the reviewer can always roll back to the original working
	// tree. (Mount re-runs per connect/refresh and must NOT re-baseline.) A store
	// that fails to open just disables versioning; it never blocks a review.
	if vs, verr := review.NewVersionStore(review.VersionsDir(csvPath)); verr != nil {
		slog.Warn("version store init failed; versioning disabled", "err", verr)
	} else {
		controller.Versions = vs
		controller.CheckpointBaseline()
	}

	initial := &review.PrereviewState{
		RepoPath:  absRepo,
		Base:      base,
		NoGit:     tgt.NoGit,
		StartedAt: startedAt.Format("2006-01-02 15:04:05"),
		CSVPath:   csvPath,
		Comments:  initialComments,
		AgentMode: agentMode,
	}

	mux := http.NewServeMux()
	// The catch-all `/` route owns the SPA — but http.ServeMux routes
	// every unmatched GET to it, so a relative-path image reference in
	// reviewed markdown (e.g. `<img src="mockups/foo.png">`) gets back the
	// SPA HTML shell instead of PNG bytes. staticFallback intercepts
	// GET/HEAD for allowlisted asset extensions and serves them from the
	// repo root; everything else (POSTs, WS upgrades, non-asset paths)
	// falls through to the LiveHandler unchanged.
	mux.Handle("/", staticFallback(absRepo, tmpl.Handle(controller, livetemplate.AsState(initial))))
	registerAssetRoutes(mux)

	// Decide what to actually bind to. On a remote (SSH) box with no
	// explicit --host, prefer this host's Tailscale IP: reachable from
	// the user's phone over the tailnet, never exposed to the public
	// internet the way --host 0.0.0.0 would. Locally, unchanged: the
	// historical 127.0.0.1 default.
	tsIP, magicDNS := netaddr.TailscaleIPv4()
	bindHost, warn := netaddr.ResolveBindHost(explicitHost, host, netaddr.IsRemoteBox(), tsIP)
	if warn != "" {
		fmt.Fprintf(os.Stderr, "prereview: %s\n", warn)
	}

	addr := net.JoinHostPort(bindHost, fmt.Sprintf("%d", port))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", addr, err)
	}
	actual := ln.Addr().(*net.TCPAddr)

	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	url := fmt.Sprintf("http://%s:%d", bindHost, actual.Port)
	// READY is the canonical, machine-parsed line: the skill and the e2e
	// harness read the first `READY ` line and nothing else. It now
	// already points at a reachable address — loopback locally, the
	// Tailscale IP on a remote box — so the skill only has to render it
	// as a clickable link.
	fmt.Printf("READY %s\n", url)
	// ALT lines are additive, human-facing alternatives the harness
	// ignores by contract: chiefly the MagicDNS hostname, far nicer to
	// tap on a phone than a 100.x octet string. Same format, one per line.
	for _, alt := range netaddr.AltURLs(bindHost, tsIP, magicDNS, actual.Port) {
		fmt.Printf("ALT %s\n", alt)
	}
	// Print the resolved review directory so the agent can locate the store
	// under <dir>/.prereview/ even when the path was a single file (RepoPath
	// is normalized to the file's parent). For a git repo this equals the
	// path argument, so the existing contract is unchanged.
	fmt.Printf("REPO %s\n", storeRoot)
	slog.Info("prereview started", "url", url, "repo", absRepo, "store", storeRoot, "base", base, "noGit", tgt.NoGit, "bindHost", bindHost)

	// Emit the `ready` event AFTER the plaintext preamble so the agent's
	// READY/REPO parse is never interleaved with JSON. No-op when not in agent mode.
	emitReady(emitter, storeRoot, csvPath, skillUpdated)

	// Watch the agent's inbound status file (.prereview/llm-status.json) and
	// push each change to every open tab. Agent mode only — that's when an
	// agent is running and writing status. Stops on shutdown.
	if agentMode {
		stopWatch := make(chan struct{})
		defer close(stopWatch)
		go controller.WatchLLMStatus(stopWatch, review.LLMStatusPollInterval)
	}

	return serveAndWait(srv, ln, nil, shutdownReq)
}

// newStreamEmitter builds the agent-mode event emitter targeting
// <store>/.prereview/events.jsonl (durable mirror) and stdout (live channel),
// or returns nil outside agent mode so callers attach it unconditionally.
func newStreamEmitter(agentMode bool, csvPath string) *review.EventStream {
	if !agentMode {
		return nil
	}
	return review.NewEventStream(os.Stdout, review.EventsPath(csvPath))
}

// emitReady emits the one-shot `ready` event in agent mode; a nil emitter
// (non-agent session) is a no-op. A write failure is logged, not fatal — the
// review server must run regardless. The session always starts unpaused
// (openStore cleared any stale paused marker), so ready reports paused=false.
// skillUpdated reports whether this launch refreshed the installed skill, so the
// agent knows its loaded skill is stale (see syncInstalledSkill).
func emitReady(emitter *review.EventStream, storeRoot, csvPath string, skillUpdated bool) {
	if emitter == nil {
		return
	}
	if err := emitter.EmitReady(storeRoot, csvPath, false, skillUpdated, time.Now()); err != nil {
		slog.Warn("emit ready event", "err", err)
	}
}

// registerAssetRoutes mounts the static client/font/CSS routes shared by repo
// mode and external (proxy) mode. The catch-all "/" (the livetemplate SPA) is
// registered by the caller, since its wrapper differs between modes.
func registerAssetRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/livetemplate-client.js", serveBytes("application/javascript", assets.ClientJS()))
	mux.HandleFunc("/mermaid.min.js", serveBytes("application/javascript", assets.MermaidJS()))
	mux.HandleFunc("/mermaid-init.js", serveBytes("application/javascript", assets.MermaidInitJS()))
	mux.HandleFunc("/pico.min.css", serveBytes("text/css", assets.PicoCSS()))
	mux.HandleFunc("/livetemplate.css", serveBytes("text/css", assets.ClientCSS()))
	mux.HandleFunc("/syntax.css", serveBytes("text/css", []byte(gitdiff.HighlightCSS)))
	mux.HandleFunc("/prereview.css", serveBytes("text/css", assets.PrereviewCSS()))
	mux.HandleFunc("/fonts/jetbrains-mono-regular.woff2", serveBytes("font/woff2", assets.FontRegular()))
	mux.HandleFunc("/fonts/jetbrains-mono-bold.woff2", serveBytes("font/woff2", assets.FontBold()))
	mux.HandleFunc("/fonts/geist-regular.woff2", serveBytes("font/woff2", assets.GeistRegular()))
	mux.HandleFunc("/fonts/geist-medium.woff2", serveBytes("font/woff2", assets.GeistMedium()))
	mux.HandleFunc("/fonts/geist-semibold.woff2", serveBytes("font/woff2", assets.GeistSemiBold()))
	// The in-app usage guide. An exact pattern outranks the "/" SPA catch-all,
	// and the extension-less path is never claimed by staticFallback, so a repo
	// file named usage/usage.md can't shadow it (issue #75).
	mux.HandleFunc("/_usage", serveBytes("text/html; charset=utf-8", usagePage))
}

// serveAndWait runs the UI server (and an optional secondary server, e.g. the
// external-mode reverse proxy) until an OS signal, a UI Quit, or a serve error
// arrives, then shuts both down gracefully. extra may be nil.
func serveAndWait(srv *http.Server, ln net.Listener, extra *http.Server, shutdownReq <-chan struct{}) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serveErr := make(chan error, 1)
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
		}
		close(serveErr)
	}()

	select {
	case <-ctx.Done():
		slog.Info("shutdown signal received")
	case <-shutdownReq:
		slog.Info("quit requested from UI")
	case err := <-serveErr:
		if err != nil {
			return fmt.Errorf("server: %w", err)
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if extra != nil {
		_ = extra.Shutdown(shutdownCtx)
	}
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	return nil
}

// runExternal serves `prereview --external <url>`: a reverse proxy fronting the
// live local site on its own port (a separate origin so the app's root-relative
// URLs forward cleanly — see proxy.go) plus the prereview UI that frames it and
// overlays the region-annotation overlay. Annotations save to <out>/comments.csv.
func runExternal(externalURL, outDir, host string, explicitHost bool, port int, agentMode, skillUpdated, replace bool) error {
	target, err := url.Parse(externalURL)
	if err != nil || (target.Scheme != "http" && target.Scheme != "https") || target.Host == "" {
		return fmt.Errorf("invalid --external URL %q: expected e.g. http://localhost:8080", externalURL)
	}
	if outDir == "" {
		return fmt.Errorf("--external requires --out <dir> (where annotations are saved)")
	}
	absOut, err := resolveStoreRoot(outDir, "")
	if err != nil {
		return err
	}

	// Claim the per-store server lock BEFORE openStore, same as repo mode: a
	// non-replacing second launch must error out before openStore wipes a live
	// server's events.jsonl.
	release, err := claimServerLock(filepath.Join(absOut, ".prereview"), replace)
	if err != nil {
		return err
	}
	defer release()

	// Same .prereview/ store layout as repo mode (so the agent locates the
	// store identically), just rooted at --out.
	startedAt := time.Now()
	csvPath, csvWriter, err := openStore(absOut)
	if err != nil {
		return err
	}
	emitter := newStreamEmitter(agentMode, csvPath)
	// Fail fast on a corrupt store; Mount reloads the rows on every connect.
	if _, err := csv.Read(csvPath); err != nil {
		return fmt.Errorf("read existing csv: %w", err)
	}

	tmplFiles, cleanup, err := stageTemplates(templatesFS)
	if err != nil {
		return fmt.Errorf("stage templates: %w", err)
	}
	defer cleanup()
	tmpl, err := livetemplate.New("prereview",
		livetemplate.WithParseFiles(tmplFiles...),
		livetemplate.WithWebSocketCompression(),
	)
	if err != nil {
		return fmt.Errorf("livetemplate.New: %w", err)
	}

	tsIP, magicDNS := netaddr.TailscaleIPv4()
	bindHost, warn := netaddr.ResolveBindHost(explicitHost, host, netaddr.IsRemoteBox(), tsIP)
	if warn != "" {
		fmt.Fprintf(os.Stderr, "prereview: %s\n", warn)
	}

	// The proxy gets its OWN origin (a random port on the same bind host) so
	// the framed app's root-relative URLs resolve against the proxy root. Bind
	// it first to learn its port before rendering the UI that iframes it.
	proxyLn, err := net.Listen("tcp", net.JoinHostPort(bindHost, "0"))
	if err != nil {
		return fmt.Errorf("listen proxy: %w", err)
	}
	proxyPort := proxyLn.Addr().(*net.TCPAddr).Port
	proxyBaseURL := fmt.Sprintf("http://%s:%d/", bindHost, proxyPort)
	proxySrv := &http.Server{Handler: proxy.NewExternalProxy(target), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := proxySrv.Serve(proxyLn); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("proxy server", "err", err)
		}
	}()

	shutdownReq := make(chan struct{}, 1)
	controller := &review.PrereviewController{
		ExternalMode: true,
		ProxyBaseURL: proxyBaseURL,
		TargetURL:    externalURL,
		CSVPath:      csvPath,
		UIPrefsPath:  uiPrefsPath(),
		Version:      version,
		CSVWriter:    csvWriter,
		AgentMode:    agentMode,
		Emitter:      emitter,
		ShutdownReq:  shutdownReq,
	}
	initial := &review.PrereviewState{
		ExternalMode: true,
		ProxyBaseURL: proxyBaseURL,
		TargetURL:    externalURL,
		StartedAt:    startedAt.Format("2006-01-02 15:04:05"),
		CSVPath:      csvPath,
		AgentMode:    agentMode,
	}

	mux := http.NewServeMux()
	// No staticFallback: there is no repo to serve assets from. The live
	// site's own assets flow through the proxy origin, not this one.
	mux.Handle("/", tmpl.Handle(controller, livetemplate.AsState(initial)))
	registerAssetRoutes(mux)

	uiLn, err := net.Listen("tcp", net.JoinHostPort(bindHost, fmt.Sprintf("%d", port)))
	if err != nil {
		return fmt.Errorf("listen ui: %w", err)
	}
	uiPort := uiLn.Addr().(*net.TCPAddr).Port
	uiSrv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}

	uiURL := fmt.Sprintf("http://%s:%d", bindHost, uiPort)
	fmt.Printf("READY %s\n", uiURL)
	for _, alt := range netaddr.AltURLs(bindHost, tsIP, magicDNS, uiPort) {
		fmt.Printf("ALT %s\n", alt)
	}
	fmt.Printf("PROXY %s\n", proxyBaseURL)
	// REPO points at the annotation store so the agent can locate <out>/.prereview/.
	fmt.Printf("REPO %s\n", absOut)
	slog.Info("prereview started (external)", "url", uiURL, "proxy", proxyBaseURL, "target", externalURL, "out", absOut, "bindHost", bindHost)

	emitReady(emitter, absOut, csvPath, skillUpdated)

	// Watch the agent's inbound status file and push changes to every open tab
	// (agent mode only). Stops on shutdown.
	if agentMode {
		stopWatch := make(chan struct{})
		defer close(stopWatch)
		go controller.WatchLLMStatus(stopWatch, review.LLMStatusPollInterval)
	}

	return serveAndWait(uiSrv, uiLn, proxySrv, shutdownReq)
}

func serveBytes(contentType string, body []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(body)
	}
}

// staticAllowedExt is the closed set of extensions staticFallback will
// serve from disk. Excludes .md / .txt — those must keep routing to the
// SPA so the LiveHandler can render markdown reviews and future SPA
// routes don't accidentally hit the filesystem. .html / .htm ARE on the
// list: the HTML preview iframe (`<iframe src="/foo.html">`) needs the
// file served from disk; the SPA entry is `/`, never `/index.html`, so
// the fall-through path is unaffected.
var staticAllowedExt = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true,
	".svg": true, ".webp": true, ".ico": true,
	".pdf": true,
	".css": true, ".js": true,
	".html": true, ".htm": true,
	".woff": true, ".woff2": true, ".ttf": true,
	".mp4": true, ".webm": true, ".mp3": true, ".wav": true,
}

// staticFallback serves files from root for GET/HEAD requests whose
// URL path has an allowlisted extension and resolves (after symlink
// eval + traversal checks) to an existing regular file under root.
// Every other request — wrong method, dot-component path
// (.git/.prereview/.env), non-allowlisted extension, WebSocket
// upgrade on `/` — is delegated to next, which is the LiveHandler.
//
// Two independent traversal defenses: reject any path segment that
// begins with `.`, AND verify EvalSymlinks(resolved) stays under
// EvalSymlinks(root). Either alone is enough; both is belt-and-braces.
func staticFallback(root string, next http.Handler) http.Handler {
	rootResolved, err := filepath.EvalSymlinks(root)
	if err != nil || rootResolved == "" {
		rootResolved = root
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			next.ServeHTTP(w, r)
			return
		}
		// URL semantics: path.Clean (not filepath.Clean) collapses ".."
		// and "." in slash-separated paths regardless of OS.
		cleaned := path.Clean("/" + strings.TrimPrefix(r.URL.Path, "/"))
		if cleaned == "/" || hasDotComponent(cleaned) {
			next.ServeHTTP(w, r)
			return
		}
		if !staticAllowedExt[strings.ToLower(filepath.Ext(cleaned))] {
			next.ServeHTTP(w, r)
			return
		}
		full := filepath.Join(rootResolved, filepath.FromSlash(cleaned))
		resolved, err := filepath.EvalSymlinks(full)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		// EvalSymlinks-then-prefix-check defeats symlinks that escape the
		// repo. Append the separator on both sides so "/repo" doesn't
		// accept "/repo-evil/foo.png".
		if !strings.HasPrefix(resolved+string(filepath.Separator), rootResolved+string(filepath.Separator)) {
			http.NotFound(w, r)
			return
		}
		info, err := os.Stat(resolved)
		if err != nil || info.IsDir() {
			http.NotFound(w, r)
			return
		}
		f, err := os.Open(resolved)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer f.Close()
		w.Header().Set("Cache-Control", "no-cache")
		// Force inline rendering — without this, Chrome respects the
		// user's "Download PDFs" setting and shows the compact embed
		// stub (PDF icon + Open button) instead of the inline viewer.
		// `inline` is the safe default for every allowlisted format
		// here (images, PDFs, media) — none of these are intended as
		// downloads in a code-review context.
		w.Header().Set("Content-Disposition", "inline")
		// http.ServeContent sets Content-Type via mime.TypeByExtension,
		// honours Range, and handles If-Modified-Since for 304s.
		http.ServeContent(w, r, resolved, info.ModTime(), f)
	})
}

// hasDotComponent returns true if any segment of cleaned (a slash-rooted
// path) begins with "." — guards against /.git/, /.prereview/, /.env etc.
func hasDotComponent(cleaned string) bool {
	for seg := range strings.SplitSeq(strings.TrimPrefix(cleaned, "/"), "/") {
		if strings.HasPrefix(seg, ".") {
			return true
		}
	}
	return false
}
