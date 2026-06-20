package main

import (
	"context"
	"errors"
	"flag"
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

	_ "embed"

	"github.com/livetemplate/livetemplate"
	"github.com/livetemplate/prereview/csv"
	"github.com/livetemplate/prereview/gitdiff"
	"github.com/livetemplate/prereview/internal/assets"
	"github.com/livetemplate/prereview/internal/netaddr"
	"github.com/livetemplate/prereview/internal/proxy"
	"github.com/livetemplate/prereview/internal/update"
)

//go:embed prereview.tmpl
var prereviewTemplate string

// Skill files embedded so `prereview --install-skill` can drop them
// into ~/.claude/skills/prereview/ without the user hand-copying (and
// fat-fingering the case-sensitive SKILL.md filename).
//
//go:embed skill/SKILL.md
var skillMD string

//go:embed skill/reference.md
var skillReferenceMD string

// Version set via -ldflags at build time.
var version = "dev"

// reviewPath is the path to review: the first positional argument, or "."
// (current directory) when none is given. It's a git repo, a non-git
// directory, or a single file — resolveTarget classifies which.
func reviewPath(args []string) string {
	if len(args) > 0 {
		return args[0]
	}
	return "."
}

func main() {
	flag.Usage = func() {
		fmt.Fprint(flag.CommandLine.Output(),
			"Usage: prereview [flags] [path]\n\n"+
				"  path   git repo, non-git directory, or single file to review (default: current dir).\n"+
				"         Flags must come before the path, e.g. `prereview --skill ./docs`.\n\n"+
				"Flags:\n")
		flag.PrintDefaults()
	}
	base := flag.String("base", "HEAD", "git base for comparison (default HEAD = working tree vs last commit); ignored for a non-git dir or single file")
	external := flag.String("external", "", "annotate a live local website instead of files: reverse-proxies this http(s):// URL and overlays the region-annotation UI. Requires --out. Ignores [path]/--base.")
	out := flag.String("out", "", "directory whose .prereview/ holds the saved annotations (comments.csv + DONE). Defaults to the review path; required with --external (which has no review path).")
	port := flag.Int("port", 0, "TCP port to listen on (0 = random free port)")
	host := flag.String("host", "127.0.0.1", "host/IP to bind on. Unset on a remote (SSH) box, prereview auto-binds to this host's Tailscale IP so a phone can reach it without exposing it publicly; locally it stays 127.0.0.1. Pass an explicit value to override.")
	skill := flag.Bool("skill", false, "running under the Claude skill: show 'Hand off → Claude' button that writes .prereview/DONE; default UI shows 'Quit' instead")
	stream := flag.Bool("stream", false, "emit a continuous JSON event stream (stdout + .prereview/events.jsonl) for an LLM: each 'Hand off' emits a handoff snapshot, the new 'End session' button emits a terminating session_end. Implies --skill.")
	showVersion := flag.Bool("version", false, "print version and exit")
	doInstallSkill := flag.Bool("install-skill", false, "install the Claude Code skill into ~/.claude/skills/prereview/ and exit")
	doUpdate := flag.Bool("update", false, "download and install the latest prereview release from GitHub, then exit")
	doUninstall := flag.Bool("uninstall", false, "remove the prereview binary from disk, then exit (your review comments in each repo's .prereview/ are left untouched)")
	noUpdate := flag.Bool("no-update", false, "skip the on-run update check (also honoured via PREREVIEW_NO_UPDATE=1)")
	flag.Parse()

	// flag can't tell "user passed --host 127.0.0.1" from "default
	// 127.0.0.1" by value alone, and that distinction is load-bearing:
	// an explicit --host is an absolute operator override (we never
	// auto-rebind over it), the default is just a starting point we may
	// replace with the Tailscale IP on a remote box. flag.Visit only
	// reports flags actually set on the command line.
	explicitHost := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "host" {
			explicitHost = true
		}
	})

	if *showVersion {
		fmt.Println(version)
		return
	}

	if *doUpdate {
		exe, err := update.ResolveExecutablePath()
		if err != nil {
			fmt.Println(err)
			return
		}
		if pm, ok := update.DetectPackageManager(exe); ok {
			fmt.Printf("prereview was installed via %s, which manages upgrades.\nUpgrade with:\n  %s\n", pm.Name, pm.Upgrade)
			return
		}
		cacheDir, _ := os.UserCacheDir()
		newTag, err := update.SelfUpdate(context.Background(), version, exe,
			update.GithubAPIBase, &http.Client{Timeout: 120 * time.Second}, cacheDir, true)
		switch {
		case err == nil:
			fmt.Printf("Updated prereview %s → %s. Restart prereview to use the new version.\n", version, newTag)
		case errors.Is(err, update.ErrAlreadyCurrent):
			fmt.Printf("prereview %s is already the latest version.\n", version)
		case errors.Is(err, update.ErrDevBuild), errors.Is(err, update.ErrGoBuildCache), errors.Is(err, update.ErrUnwritable):
			fmt.Println(err)
		default:
			slog.Error("update failed", "err", err)
			os.Exit(1)
		}
		return
	}

	if *doUninstall {
		exe, err := update.ResolveExecutablePath()
		if err != nil {
			fmt.Println(err)
			return
		}
		// brew/scoop own the binary they placed; deleting it underneath
		// them leaves dangling package metadata. Defer to their uninstaller.
		if pm, ok := update.DetectPackageManager(exe); ok {
			fmt.Printf("prereview was installed via %s, which manages removal.\nUninstall with:\n  %s\n", pm.Name, pm.Uninstall)
			return
		}
		// Scope is the binary only: review comments live in each repo's
		// .prereview/ and are never touched by uninstall.
		fmt.Printf("Removing prereview binary: %s\n", exe)
		if err := os.Remove(exe); err != nil {
			// A running binary can't delete itself on Windows ("access is
			// denied"); on Unix the unlink succeeds while still executing.
			fmt.Printf("Could not remove %s automatically: %v\n", exe, err)
			fmt.Println("Delete that file manually to finish uninstalling.")
			os.Exit(1)
		}
		fmt.Println("Uninstalled. Your review comments in each repo's .prereview/ are left untouched.")
		return
	}

	if *doInstallSkill {
		home, err := os.UserHomeDir()
		if err != nil {
			slog.Error("install skill: resolve home", "err", err)
			os.Exit(1)
		}
		path, err := installSkill(home)
		if err != nil {
			slog.Error("install skill", "err", err)
			os.Exit(1)
		}
		fmt.Printf("Installed prereview skill → %s\n", path)
		fmt.Println("Invoke it in Claude Code with /prereview (or just \"review my changes\").")
		fmt.Println("If Claude reports it as unknown, run /reload or restart the session.")
		return
	}

	if update.ShouldAutoUpdate(version, *noUpdate) {
		maybeAutoUpdate()
	}

	if *external != "" {
		if err := runExternal(*external, *out, *host, explicitHost, *port, *skill, *stream); err != nil {
			slog.Error("fatal", "err", err)
			os.Exit(1)
		}
		return
	}

	if err := run(reviewPath(flag.Args()), *base, *host, explicitHost, *port, *skill, *out, *stream); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

// maybeAutoUpdate runs the throttled on-run update check. On a newer
// release it replaces the binary and re-execs into it (this never
// returns on success). Every non-update outcome is non-fatal: the
// review server must always start regardless of network state. Expected
// "no update" sentinels are silent; a checksum mismatch is the one
// signal worth surfacing (corrupt CDN or tampering); transport errors
// are debug-only noise.
func maybeAutoUpdate() {
	exe, err := update.ResolveExecutablePath()
	if err != nil {
		slog.Debug("auto-update: resolve executable", "err", err)
		return
	}
	// A brew/scoop-installed binary must not self-replace — the package
	// manager owns upgrades. Skip silently; `--update` surfaces a hint.
	if _, ok := update.DetectPackageManager(exe); ok {
		return
	}
	cacheDir, _ := os.UserCacheDir()
	ctx, cancel := context.WithTimeout(context.Background(), 130*time.Second)
	defer cancel()

	newTag, err := update.SelfUpdate(ctx, version, exe, update.GithubAPIBase,
		&http.Client{Timeout: 120 * time.Second}, cacheDir, false)
	switch {
	case err == nil && newTag != "":
		fmt.Fprintf(os.Stderr, "prereview: updated %s → %s, restarting…\n", version, newTag)
		if rerr := update.Reexec(exe, newTag); rerr != nil {
			slog.Warn("re-exec after update failed; continuing on current version", "err", rerr)
		}
	case errors.Is(err, update.ErrDevBuild), errors.Is(err, update.ErrGoBuildCache),
		errors.Is(err, update.ErrAlreadyCurrent), errors.Is(err, update.ErrThrottled),
		errors.Is(err, update.ErrUnwritable):
		// Expected steady-state outcomes — stay silent.
	case errors.Is(err, update.ErrChecksumMismatch):
		slog.Warn("auto-update aborted: release checksum mismatch", "err", err)
	default:
		slog.Debug("auto-update check failed", "err", err)
	}
}

func run(repo, base, host string, explicitHost bool, port int, skillMode bool, out string, streamMode bool) error {
	// --stream implies --skill: the stream needs the "Hand off" button, which
	// is gated by skill mode.
	if streamMode {
		skillMode = true
	}
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

	// The .prereview/ store defaults to the review root; --out redirects it
	// (e.g. to keep a read-only checkout pristine). storeRoot is what we print
	// as REPO so the skill polls the right .prereview/.
	storeRoot, err := resolveStoreRoot(out, absRepo)
	if err != nil {
		return err
	}
	startedAt := time.Now()
	csvPath, donePath, csvWriter, err := openStore(storeRoot)
	if err != nil {
		return err
	}
	emitter := newStreamEmitter(streamMode, csvPath)

	// Load any existing comments from disk so a restart resumes the session.
	existing, err := csv.Read(csvPath)
	if err != nil {
		return fmt.Errorf("read existing csv: %w", err)
	}
	initialComments := make([]Comment, 0, len(existing))
	for _, r := range existing {
		initialComments = append(initialComments, Comment{
			ID:       r.ID,
			File:     r.File,
			FromLine: r.FromLine,
			ToLine:   r.ToLine,
			Side:     r.Side,
			Body:     r.Body,
			Created:  r.CreatedAt,
			Resolved: r.Resolved,
		})
	}

	// livetemplate.New requires templates as files on disk. Write the embedded
	// template to a temp file for the lifetime of the process. Same workaround
	// used by tinkerdown — see tinkerdown/internal/server/websocket.go:465.
	tmplFile, cleanup, err := writeTempTemplate(prereviewTemplate)
	if err != nil {
		return fmt.Errorf("stage template: %w", err)
	}
	defer cleanup()

	tmpl, err := livetemplate.New("prereview",
		livetemplate.WithParseFiles(tmplFile),
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

	controller := &PrereviewController{
		RepoPath:    absRepo,
		Base:        base,
		NoGit:       tgt.NoGit,
		SingleFile:  tgt.SingleFile,
		CSVPath:     csvPath,
		DonePath:    donePath,
		Version:     version,
		CSVWriter:   csvWriter,
		SkillMode:   skillMode,
		StreamMode:  streamMode,
		Emitter:     emitter,
		ShutdownReq: shutdownReq,
	}
	initial := &PrereviewState{
		RepoPath:   absRepo,
		Base:       base,
		NoGit:      tgt.NoGit,
		StartedAt:  startedAt.Format("2006-01-02 15:04:05"),
		CSVPath:    csvPath,
		Comments:   initialComments,
		SkillMode:  skillMode,
		StreamMode: streamMode,
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
	// Print the resolved review directory so the skill can poll
	// <dir>/.prereview/DONE even when the path was a single file (RepoPath
	// is normalized to the file's parent). For a git repo this equals the
	// path argument, so the existing skill contract is unchanged.
	fmt.Printf("REPO %s\n", storeRoot)
	slog.Info("prereview started", "url", url, "repo", absRepo, "store", storeRoot, "base", base, "noGit", tgt.NoGit, "bindHost", bindHost)

	// Emit the `ready` event AFTER the plaintext preamble so the skill's
	// READY/REPO parse is never interleaved with JSON. No-op when not streaming.
	emitReady(emitter, storeRoot, csvPath)

	return serveAndWait(srv, ln, nil, shutdownReq)
}

// newStreamEmitter builds the stream-mode event emitter targeting
// <store>/.prereview/events.jsonl (durable mirror) and stdout (live channel),
// or returns nil when streaming is off so callers attach it unconditionally.
func newStreamEmitter(streamMode bool, csvPath string) *eventStream {
	if !streamMode {
		return nil
	}
	eventsPath := filepath.Join(filepath.Dir(csvPath), "events.jsonl")
	return newEventStream(os.Stdout, eventsPath)
}

// emitReady emits the one-shot `ready` event when streaming; a nil emitter
// (non-stream session) is a no-op. A write failure is logged, not fatal — the
// review server must run regardless.
func emitReady(emitter *eventStream, storeRoot, csvPath string) {
	if emitter == nil {
		return
	}
	if err := emitter.EmitReady(storeRoot, csvPath, time.Now()); err != nil {
		slog.Warn("emit ready event", "err", err)
	}
}

// openStore prepares the .prereview/ store (comments.csv + DONE marker) under
// storeRoot, the directory whose .prereview/ holds annotations — the value
// printed as REPO so the skill polls the right place. Shared by repo mode and
// external mode; clears any stale DONE marker so a fresh session isn't read as
// already-handed-off, and returns the paths plus a goroutine-safe CSV writer.
func openStore(storeRoot string) (csvPath, donePath string, w *csv.Writer, err error) {
	dir := filepath.Join(storeRoot, ".prereview")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", nil, fmt.Errorf("mkdir %s: %w", dir, err)
	}
	// Fixed CSV filename — survives server restarts so users can resume editing
	// where they left off. (Earlier versions timestamped it per session, which
	// orphaned previous comments on restart.)
	csvPath = filepath.Join(dir, "comments.csv")
	donePath = filepath.Join(dir, "DONE")
	_ = os.Remove(donePath)
	// Clear any stale stream event log so a fresh session starts from seq 0
	// rather than appending onto a previous run's events (same intent as the
	// DONE reset above). Harmless when not streaming — the file won't exist.
	_ = os.Remove(filepath.Join(dir, "events.jsonl"))
	return csvPath, donePath, csv.NewWriter(csvPath), nil
}

// resolveStoreRoot picks the directory whose .prereview/ holds annotations:
// --out when set (available in every mode so it's never a silently-ignored
// flag), else the default review root.
func resolveStoreRoot(out, defaultRoot string) (string, error) {
	if out == "" {
		return defaultRoot, nil
	}
	abs, err := filepath.Abs(out)
	if err != nil {
		return "", fmt.Errorf("resolve --out: %w", err)
	}
	return abs, nil
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
	mux.HandleFunc("/fonts/jetbrains-mono-regular.woff2", serveBytes("font/woff2", assets.FontRegular()))
	mux.HandleFunc("/fonts/jetbrains-mono-bold.woff2", serveBytes("font/woff2", assets.FontBold()))
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
func runExternal(externalURL, outDir, host string, explicitHost bool, port int, skillMode bool, streamMode bool) error {
	// --stream implies --skill (the stream needs the "Hand off" button).
	if streamMode {
		skillMode = true
	}
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

	// Same .prereview/ store layout as repo mode (so the skill polls
	// <out>/.prereview/DONE identically), just rooted at --out.
	startedAt := time.Now()
	csvPath, donePath, csvWriter, err := openStore(absOut)
	if err != nil {
		return err
	}
	emitter := newStreamEmitter(streamMode, csvPath)
	// Fail fast on a corrupt store; Mount reloads the rows on every connect.
	if _, err := csv.Read(csvPath); err != nil {
		return fmt.Errorf("read existing csv: %w", err)
	}

	tmplFile, cleanup, err := writeTempTemplate(prereviewTemplate)
	if err != nil {
		return fmt.Errorf("stage template: %w", err)
	}
	defer cleanup()
	tmpl, err := livetemplate.New("prereview",
		livetemplate.WithParseFiles(tmplFile),
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
	controller := &PrereviewController{
		ExternalMode: true,
		ProxyBaseURL: proxyBaseURL,
		TargetURL:    externalURL,
		CSVPath:      csvPath,
		DonePath:     donePath,
		Version:      version,
		CSVWriter:    csvWriter,
		SkillMode:    skillMode,
		StreamMode:   streamMode,
		Emitter:      emitter,
		ShutdownReq:  shutdownReq,
	}
	initial := &PrereviewState{
		ExternalMode: true,
		ProxyBaseURL: proxyBaseURL,
		TargetURL:    externalURL,
		StartedAt:    startedAt.Format("2006-01-02 15:04:05"),
		CSVPath:      csvPath,
		SkillMode:    skillMode,
		StreamMode:   streamMode,
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
	// REPO points at the annotation store so the skill polls <out>/DONE.
	fmt.Printf("REPO %s\n", absOut)
	slog.Info("prereview started (external)", "url", uiURL, "proxy", proxyBaseURL, "target", externalURL, "out", absOut, "bindHost", bindHost)

	emitReady(emitter, absOut, csvPath)

	return serveAndWait(uiSrv, uiLn, proxySrv, shutdownReq)
}

// installSkill writes the embedded skill files into
// <home>/.claude/skills/prereview/ and returns the SKILL.md path.
// Overwrites existing files so re-running upgrades the skill. The
// filename is the case-sensitive uppercase SKILL.md on purpose — a
// lowercase skill.md is silently ignored by Claude Code, the exact
// trap this command exists to prevent users from hitting.
func installSkill(home string) (string, error) {
	dir := filepath.Join(home, ".claude", "skills", "prereview")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", dir, err)
	}
	skillPath := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(skillPath, []byte(skillMD), 0o644); err != nil {
		return "", fmt.Errorf("write %s: %w", skillPath, err)
	}
	refPath := filepath.Join(dir, "reference.md")
	if err := os.WriteFile(refPath, []byte(skillReferenceMD), 0o644); err != nil {
		return "", fmt.Errorf("write %s: %w", refPath, err)
	}
	return skillPath, nil
}

// writeTempTemplate stages the embedded template to a deterministic temp
// path tied to the PID and returns its path plus a cleanup func.
func writeTempTemplate(content string) (path string, cleanup func(), err error) {
	f, err := os.CreateTemp("", fmt.Sprintf("prereview-%d-*.tmpl", os.Getpid()))
	if err != nil {
		return "", nil, err
	}
	if _, err := f.WriteString(content); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", nil, err
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return "", nil, err
	}
	return f.Name(), func() { _ = os.Remove(f.Name()) }, nil
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

// reviewTarget is the classified path argument after normalization.
// RepoPath is ALWAYS a directory: the comment store and DONE marker live
// at RepoPath/.prereview/, and every downstream filepath.Join(RepoPath,
// relPath) stays valid. SingleFile, when non-empty, is the only
// reviewable file (its basename, relative to RepoPath). NoGit is true
// whenever the target isn't backed by a git repo — the file list and
// per-file diff are then synthesized from the filesystem instead of git.
type reviewTarget struct {
	RepoPath   string
	SingleFile string
	NoGit      bool
}

// resolveTarget classifies an absolute review path:
//
//   - a file              → no-git, review just that file
//     (RepoPath = its parent dir, SingleFile = its basename)
//   - a directory with .git  → git mode (unchanged behaviour)
//   - a directory without .git → no-git, review the whole tree
//
// It deliberately does NOT walk up to find an ancestor .git: a mistyped
// path silently resolving to some parent repo is a worse failure than a
// clear "review exactly what you pointed at" contract. A stat error
// (missing path, permission) is fatal — same as the old assertGitRepo.
func resolveTarget(absPath string) (reviewTarget, error) {
	info, err := os.Stat(absPath)
	if err != nil {
		return reviewTarget{}, fmt.Errorf("repo %q: %w", absPath, err)
	}
	if !info.IsDir() {
		return reviewTarget{
			RepoPath:   filepath.Dir(absPath),
			SingleFile: filepath.Base(absPath),
			NoGit:      true,
		}, nil
	}
	// .git may be a directory (normal repo) or a file (worktree/submodule);
	// os.Stat succeeds for both, so err == nil ⇒ git mode. Only a genuine
	// "not there" (ErrNotExist) drops to no-git; any other stat error keeps
	// git mode so git itself surfaces the real problem (old assertGitRepo
	// intent: don't pre-empt git's clearer error message).
	if _, err := os.Stat(filepath.Join(absPath, ".git")); err == nil {
		return reviewTarget{RepoPath: absPath}, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return reviewTarget{RepoPath: absPath}, nil
	}
	return reviewTarget{RepoPath: absPath, NoGit: true}, nil
}
