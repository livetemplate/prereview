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
	"github.com/livetemplate/prereview/internal/library"
	"github.com/livetemplate/prereview/internal/netaddr"
	"github.com/livetemplate/prereview/internal/proxy"
	"github.com/livetemplate/prereview/internal/review"
	"github.com/livetemplate/prereview/internal/skills"
)

// externalURL, when set, adds the live site to the review (#114): the session
// fronts it through the reverse proxy as runExternal does, alongside the files.
func run(repo, base, host string, explicitHost, explicitBase bool, port int, publicURL string, agentMode bool, skillSyncs []skills.ClientSync, out string, replace bool, externalURL string) error {
	var target *url.URL
	if externalURL != "" {
		var err error
		if target, err = parseExternalURL(externalURL); err != nil {
			return err
		}
	}
	absRepo, err := filepath.Abs(repo)
	if err != nil {
		return fmt.Errorf("resolve repo path: %w", err)
	}
	catchSIGPIPE()
	tgt, err := resolveTarget(absRepo)
	if err != nil {
		return err
	}
	// A single-file review scopes every list to that file, and a live-site
	// annotation has no file, so it would vanish from the page and the agent's
	// snapshot. Refuse rather than lose it.
	if target != nil && tgt.SingleFile != "" {
		return fmt.Errorf("--external can't review a single file alongside the live site: pass its directory (%s) instead", tgt.RepoPath)
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

	// The store defaults to the review root; --out redirects it (e.g. to keep a
	// read-only checkout pristine). storeRoot is what we print as REPO; the store
	// resolved under it is printed as STORE, which is what the agent polls.
	storeRoot, err := resolveStoreRoot(out, absRepo)
	if err != nil {
		return err
	}

	// A single-file review gets its OWN store, keyed by the target (#199): its
	// storeRoot is the file's parent directory, so sibling files would otherwise
	// share one store — and one server.pid lock, which made launching a review of
	// a second file evict the first. The lock lives inside the store, so keying
	// the directory keys the lock with it.
	absTarget := ""
	if tgt.SingleFile != "" {
		absTarget = filepath.Join(absRepo, tgt.SingleFile)
	}
	store := storeDirFor(storeRoot, absTarget)

	// Claim the per-store server lock BEFORE openStore: a non-replacing second
	// launch must error out before openStore wipes a live server's events.jsonl.
	release, err := claimServerLock(store, replace)
	if err != nil {
		return err
	}
	defer release()

	startedAt := time.Now()
	csvPath, csvWriter, err := openStore(store)
	if err != nil {
		return err
	}
	// Carry over a previous, pre-#199 review's comments on this file: they were
	// written to the shared parent-directory store this session no longer reads.
	if err := seedFromLegacyStore(csvPath, storeRoot, tgt.SingleFile); err != nil {
		return err
	}
	// Record this session's review scope in the store (#171) so the agent subcommands
	// — which only ever see `--out <dir>` — list the file under review rather than
	// every file ever reviewed from this directory. openStore just cleared the
	// previous session's; a directory review writes none (it scopes to nothing).
	if err := review.WriteSessionScope(csvPath, tgt.SingleFile); err != nil {
		return fmt.Errorf("record session scope: %w", err)
	}
	emitter, closeLive := newStreamEmitter(agentMode, csvPath)
	defer closeLive()

	// Load any existing comments from disk so a restart resumes the session.
	existing, err := csv.Read(csvPath)
	if err != nil {
		return fmt.Errorf("read existing csv: %w", err)
	}
	initialComments := review.CommentsFromRows(existing)

	// livetemplate.New requires templates as files on disk. Stage the embedded
	// split set to a temp dir for the lifetime of the process. Same workaround
	// used by tinkerdown — see tinkerdown/internal/server/websocket.go:465.
	tmplFiles, cleanup, err := stageTemplates(templatesFS)
	if err != nil {
		return fmt.Errorf("stage templates: %w", err)
	}
	defer cleanup()

	// Decide what to actually bind to. On a remote (SSH) box with no
	// explicit --host, prefer this host's Tailscale IP: reachable from
	// the user's phone over the tailnet, never exposed to the public
	// internet the way --host 0.0.0.0 would. Locally, unchanged: the
	// historical 127.0.0.1 default. Resolved before the template so a
	// --public-url origin list (#65) can name the bind URL.
	tsIP, magicDNS := netaddr.TailscaleIPv4()
	bindHost, warn := netaddr.ResolveBindHost(explicitHost, host, netaddr.IsRemoteBox(), tsIP)
	switch {
	case publicURL != "" && !explicitHost:
		// The tailnet hint doesn't apply behind a proxy; this one does.
		fmt.Fprintf(os.Stderr, "prereview: bound %s for --public-url; if the proxy can't reach it there, pass --host (e.g. 0.0.0.0 behind a private proxy)\n", bindHost)
	case warn != "" && publicURL == "":
		fmt.Fprintf(os.Stderr, "prereview: %s\n", warn)
	}

	tmpl, err := newTemplate(tmplFiles, netaddr.AllowedOrigins(publicURL, bindHost, tsIP, magicDNS, port))
	if err != nil {
		return fmt.Errorf("livetemplate.New: %w", err)
	}

	// The launch token, and the files key the HTML preview loads repo files
	// with, exist before the controller: a diff loaded during startup already
	// carries the files base.
	token := newLaunchToken()
	if err := privateOutput(os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "prereview: %v\n", err)
	}
	gitdiff.SetPreviewFilesBase(token.filesBase())

	// Quit action signals here so the HTTP server can shut down gracefully
	// AFTER the framework has rendered the "stopping…" state back to the client.
	shutdownReq := make(chan struct{}, 1)

	var proxySrv *http.Server
	proxyBaseURL := ""
	if target != nil {
		if proxySrv, proxyBaseURL, err = startProxy(target, bindHost, token); err != nil {
			return err
		}
	}

	controller := &review.PrereviewController{
		ProxyBaseURL: proxyBaseURL,
		TargetURL:    externalURL,
		RepoPath:     absRepo,
		Base:         base,
		NoGit:        tgt.NoGit,
		SingleFile:   tgt.SingleFile,
		CSVPath:      csvPath,
		UIPrefsPath:  uiPrefsPath(),
		PromptsDir:   userPromptsDir(),
		QuizzesDir:   userQuizzesDir(),
		ExplainDir:   userExplainDir(),
		ShortcutsDir: userShortcutsDir(),
		ChecksDir:    userChecksDir(),
		LibraryDir:   library.Dir(),
		AgentHome:    agentHome(),
		Version:      version,
		CSVWriter:    csvWriter,
		AgentMode:    agentMode,
		SkillSyncs:   skillSyncs,
		Emitter:      emitter,
		ShutdownReq:  shutdownReq,
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

	// A comment whose file no longer exists is work nobody can do (#171). Flag those
	// outdated once, up front, so the store is true from the first moment: the CLI readers
	// (`prereview comments` / `done`) read the CSV directly, and without this they keep
	// handing the agent work on deleted files until something happens to trigger a
	// relocate. The comments stay visible to the reviewer to resolve or delete.
	controller.SweepGoneFiles()
	// Remote comments imported while no server ran (#209); after the sweep, so an
	// import is anchored to the text as it is now. Live imports drain in the watcher.
	if n, err := controller.DrainImports(); err != nil {
		slog.Warn("import", "err", err)
	} else if n > 0 {
		slog.Info("imported remote comments", "count", n)
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
	mux.Handle("/", staticFallback(absRepo, review.WithRequestHost(tmpl.Handle(controller, livetemplate.AsState(initial)))))
	registerUsageRoute(mux)
	if controller.LibraryDir != "" {
		mux.HandleFunc("/_library-review", guardLibraryRoute(libraryReviewHandler(controller.LibraryDir)))
		mux.HandleFunc("/_library-export", guardLibraryRoute(libraryExportHandler(controller.LibraryDir)))
		mux.HandleFunc("/_library-import", guardLibraryRoute(libraryImportHandler(controller.LibraryDir)))
		mux.HandleFunc("/_library-skill-patch", guardLibraryRoute(librarySkillPatchHandler(controller.LibraryDir)))
	}

	addr := net.JoinHostPort(bindHost, fmt.Sprintf("%d", port))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", addr, err)
	}
	actual := ln.Addr().(*net.TCPAddr)
	if err := recordServerAddr(store, absRepo, actual); err != nil {
		ln.Close()
		return err
	}

	srv := &http.Server{
		Handler:           guardedHandler(token, mux, staticFallback(absRepo, http.NotFoundHandler())),
		ReadHeaderTimeout: 10 * time.Second,
	}

	url := fmt.Sprintf("http://%s:%d", bindHost, actual.Port)
	// READY is the canonical, machine-parsed line: the skill and the e2e
	// harness read the first `READY ` line and nothing else. It now
	// already points at a reachable address — loopback locally, the
	// Tailscale IP on a remote box — so the skill only has to render it
	// as a clickable link.
	// With --public-url (#65) READY is that URL, the one a browser outside the
	// box can open, and the local bind URL drops to the first ALT line.
	if publicURL != "" {
		fmt.Printf("READY %s\n", token.url(publicURL))
		fmt.Printf("ALT %s\n", token.url(url))
	} else {
		fmt.Printf("READY %s\n", token.url(url))
	}
	// ALT lines are additive, human-facing alternatives the harness
	// ignores by contract: chiefly the MagicDNS hostname, far nicer to
	// tap on a phone than a 100.x octet string. Same format, one per line.
	for _, alt := range netaddr.AltURLs(bindHost, tsIP, magicDNS, actual.Port) {
		fmt.Printf("ALT %s\n", token.url(alt))
	}
	// A combined session (#114) names the live site's proxy origin, as
	// runExternal does.
	if proxyBaseURL != "" {
		fmt.Printf("PROXY %s\n", proxyBaseURL)
	}
	// Print the resolved review directory so the agent can locate the store
	// under <dir>/.prereview/ even when the path was a single file (RepoPath
	// is normalized to the file's parent). For a git repo this equals the
	// path argument, so the existing contract is unchanged.
	fmt.Printf("REPO %s\n", storeRoot)
	// STORE is where the annotations actually live, and the value the agent
	// subcommands take as --out. It is <REPO>/.prereview for a repo or directory
	// review, and a per-target subdirectory for a single-file one (#199) — so it
	// is printed unconditionally and REPO never has to mean two things.
	fmt.Printf("STORE %s\n", store)
	slog.Info("prereview started", "url", url, "repo", absRepo, "store", store, "base", base, "noGit", tgt.NoGit, "bindHost", bindHost)

	// Emit the `ready` event AFTER the plaintext preamble so the agent's
	// READY/REPO parse is never interleaved with JSON. No-op when not in agent mode.
	emitReady(emitter, storeRoot, csvPath, skills.AnyRefreshed(skillSyncs, "claude"))

	// Watch the agent's inbound status file (.prereview/llm-status.json) and
	// push each change to every open tab. Agent mode only — that's when an
	// agent is running and writing status. Stops on shutdown.
	if agentMode {
		stopWatch := make(chan struct{})
		defer close(stopWatch)
		go controller.WatchLLMStatus(stopWatch, review.LLMStatusPollInterval)
	}
	// Watch the checks rule folder in every mode, so editing a rule in your editor
	// re-runs the checks in the open tabs.
	stopChecks := make(chan struct{})
	defer close(stopChecks)
	go controller.WatchChecks(stopChecks, review.ChecksPollInterval)
	// The per-user library (checks M3): note this review so extraction can find
	// it later, and on a machine that hasn't answered the first-launch offer, look
	// for past reviews in the background (a home-directory walk takes a while).
	if lib := controller.LibraryDir; lib != "" {
		if err := library.RecordSession(lib, library.Session{Store: filepath.Dir(csvPath), Repo: absRepo, Target: tgt.SingleFile}); err != nil {
			slog.Warn("library: recording the session failed", "err", err)
		}
		if !library.Bootstrapped(lib) {
			go controller.ScanPastReviews()
		}
		go func() { // drop superseded log lines once there are enough of them
			if err := library.Compact(lib); err != nil {
				slog.Warn("library: compacting learnings failed", "err", err)
			}
		}()
	}

	return serveAndWait(srv, ln, proxySrv, shutdownReq)
}

// liveQueueDepth bounds the stdout live channel's queue (#215): worst-case
// memory is this many lines × the largest snapshot. Snapshots supersede each
// other, so a deep queue buys nothing.
const liveQueueDepth = 8

// liveFlushTimeout bounds the shutdown flush of the live channel: ample for a
// draining reader to receive the final `end`, and short so an undrained stdout
// adds little to a shutdown --replace waits on (stopGrace counts it).
const liveFlushTimeout = 500 * time.Millisecond

// newStreamEmitter builds the agent-mode event emitter targeting
// <store>/.prereview/events.jsonl (durable mirror) and stdout (live channel),
// or returns nil outside agent mode so callers attach it unconditionally.
// Stdout goes through a LossyWriter so an undrained pipe can never block
// emission (#215). The returned func flushes it; callers defer it so whatever
// is queued — notably End session's `end` — reaches stdout on every exit path.
func newStreamEmitter(agentMode bool, csvPath string) (*review.EventStream, func()) {
	if !agentMode {
		return nil, func() {}
	}
	live := review.NewLossyWriter(os.Stdout, liveQueueDepth)
	return review.NewEventStream(live, review.EventsPath(csvPath)), func() { live.Close(liveFlushTimeout) }
}

// catchSIGPIPE turns a write to a closed stdout/stderr into an EPIPE error
// instead of process death (#215). By default the Go runtime re-raises SIGPIPE
// for a broken fd 1/2, which kills the server with no log — the reviewer's tab
// just goes dead. Notify (not signal.Ignore) because SIG_IGN is inherited across
// exec, and the git children prereview runs should keep dying on a closed pipe.
func catchSIGPIPE() {
	signal.Notify(make(chan os.Signal, 1), syscall.SIGPIPE)
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

// guardedHandler is a review server's handler. The embedded assets and
// serverPIDRoute are open to anyone: the assets are public bytes from the
// binary, and --replace asks serverPIDRoute before it holds any token. The HTML
// preview reads repo files under their own key (files is nil when there is no
// repo). Everything else, page, needs the launch token.
func guardedHandler(token launchToken, page, files http.Handler) http.Handler {
	mux := http.NewServeMux()
	registerAssetRoutes(mux)
	if files != nil {
		mux.Handle(filesRoute, token.filesHandler(files))
	}
	mux.Handle("/", token.guard(page))
	return noReferrer(mux)
}

// registerAssetRoutes mounts the static client/font/CSS routes and
// serverPIDRoute, shared by repo mode and external (proxy) mode.
func registerAssetRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/livetemplate-client.js", serveBytes("application/javascript", assets.ClientJS()))
	mux.HandleFunc("/prereview-handlers.js", serveBytes("application/javascript", assets.HandlersJS()))
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
	mux.HandleFunc(serverPIDRoute, serveServerPID)
}

// registerUsageRoute mounts the in-app usage guide. An exact pattern outranks
// the "/" SPA catch-all, and the extension-less path is never claimed by
// staticFallback, so a repo file named usage/usage.md can't shadow it (issue #75).
func registerUsageRoute(mux *http.ServeMux) {
	mux.HandleFunc("/_usage", serveBytes("text/html; charset=utf-8", usagePage()))
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

	started := time.Now()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if extra != nil {
		_ = extra.Shutdown(shutdownCtx)
	}
	err := srv.Shutdown(shutdownCtx)
	slog.Info("servers stopped", "took", time.Since(started).Round(time.Millisecond))
	if err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	return nil
}

// shutdownTimeout bounds a server's graceful HTTP shutdown. --replace waits for
// it (stopGrace), so the two can't drift apart.
const shutdownTimeout = 5 * time.Second

// runExternal serves `prereview --external <url>`: a reverse proxy fronting the
// live local site on its own port (a separate origin so the app's root-relative
// URLs forward cleanly — see proxy.go) plus the prereview UI that frames it and
// overlays the region-annotation overlay. Annotations save to <out>/comments.csv.
func runExternal(externalURL, outDir, host string, explicitHost bool, port int, agentMode bool, skillSyncs []skills.ClientSync, replace bool) error {
	target, err := parseExternalURL(externalURL)
	if err != nil {
		return err
	}
	catchSIGPIPE()
	if outDir == "" {
		return fmt.Errorf("--external requires --out <dir> (where annotations are saved)")
	}
	absOut, err := resolveStoreRoot(outDir, "")
	if err != nil {
		return err
	}

	// External mode annotates a live site, not files, so there is no single-file
	// target to key on: the store is always <out>/.prereview (storeDirFor's
	// no-target form).
	store := storeDirFor(absOut, "")

	// Claim the per-store server lock BEFORE openStore, same as repo mode: a
	// non-replacing second launch must error out before openStore wipes a live
	// server's events.jsonl.
	release, err := claimServerLock(store, replace)
	if err != nil {
		return err
	}
	defer release()

	// Same store layout as repo mode (so the agent locates the store
	// identically), just rooted at --out.
	startedAt := time.Now()
	csvPath, csvWriter, err := openStore(store)
	if err != nil {
		return err
	}
	emitter, closeLive := newStreamEmitter(agentMode, csvPath)
	defer closeLive()
	// Fail fast on a corrupt store; Mount reloads the rows on every connect.
	if _, err := csv.Read(csvPath); err != nil {
		return fmt.Errorf("read existing csv: %w", err)
	}

	tmplFiles, cleanup, err := stageTemplates(templatesFS)
	if err != nil {
		return fmt.Errorf("stage templates: %w", err)
	}
	defer cleanup()
	tmpl, err := newTemplate(tmplFiles, nil)
	if err != nil {
		return fmt.Errorf("livetemplate.New: %w", err)
	}

	tsIP, magicDNS := netaddr.TailscaleIPv4()
	bindHost, warn := netaddr.ResolveBindHost(explicitHost, host, netaddr.IsRemoteBox(), tsIP)
	if warn != "" {
		fmt.Fprintf(os.Stderr, "prereview: %s\n", warn)
	}

	token := newLaunchToken()
	if err := privateOutput(os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "prereview: %v\n", err)
	}

	proxySrv, proxyBaseURL, err := startProxy(target, bindHost, token)
	if err != nil {
		return err
	}

	shutdownReq := make(chan struct{}, 1)
	controller := &review.PrereviewController{
		ExternalMode: true,
		ProxyBaseURL: proxyBaseURL,
		TargetURL:    externalURL,
		CSVPath:      csvPath,
		UIPrefsPath:  uiPrefsPath(),
		SkillsLibDir: library.Dir(), // only for the snapshot's write_with
		Version:      version,
		CSVWriter:    csvWriter,
		AgentMode:    agentMode,
		SkillSyncs:   skillSyncs,
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
	mux.Handle("/", review.WithRequestHost(tmpl.Handle(controller, livetemplate.AsState(initial))))
	registerUsageRoute(mux)

	uiLn, err := net.Listen("tcp", net.JoinHostPort(bindHost, fmt.Sprintf("%d", port)))
	if err != nil {
		return fmt.Errorf("listen ui: %w", err)
	}
	uiPort := uiLn.Addr().(*net.TCPAddr).Port
	if err := recordServerAddr(store, "", uiLn.Addr()); err != nil {
		uiLn.Close()
		return err
	}
	uiSrv := &http.Server{Handler: guardedHandler(token, mux, nil), ReadHeaderTimeout: 10 * time.Second}

	uiURL := fmt.Sprintf("http://%s:%d", bindHost, uiPort)
	fmt.Printf("READY %s\n", token.url(uiURL))
	for _, alt := range netaddr.AltURLs(bindHost, tsIP, magicDNS, uiPort) {
		fmt.Printf("ALT %s\n", token.url(alt))
	}
	fmt.Printf("PROXY %s\n", proxyBaseURL)
	// REPO points at the annotation store root so the agent can locate <out>/.prereview/;
	// STORE names the store itself — the --out for the subcommands, same as repo mode.
	fmt.Printf("REPO %s\n", absOut)
	fmt.Printf("STORE %s\n", store)
	slog.Info("prereview started (external)", "url", uiURL, "proxy", proxyBaseURL, "target", externalURL, "out", absOut, "store", store, "bindHost", bindHost)

	emitReady(emitter, absOut, csvPath, skills.AnyRefreshed(skillSyncs, "claude"))

	// Watch the agent's inbound status file and push changes to every open tab
	// (agent mode only). Stops on shutdown.
	if agentMode {
		stopWatch := make(chan struct{})
		defer close(stopWatch)
		go controller.WatchLLMStatus(stopWatch, review.LLMStatusPollInterval)
	}

	return serveAndWait(uiSrv, uiLn, proxySrv, shutdownReq)
}

// parseExternalURL validates an --external target: an absolute http(s) URL.
func parseExternalURL(externalURL string) (*url.URL, error) {
	target, err := url.Parse(externalURL)
	if err != nil || (target.Scheme != "http" && target.Scheme != "https") || target.Host == "" {
		return nil, fmt.Errorf("invalid --external URL %q: expected e.g. http://localhost:8080", externalURL)
	}
	return target, nil
}

// startProxy serves the reverse proxy for an --external target and returns it
// with its base URL. The proxy gets its OWN origin (a random port on the same
// bind host) so the framed app's root-relative URLs resolve against the proxy
// root. It is bound before the UI renders, which iframes it.
func startProxy(target *url.URL, bindHost string, token launchToken) (*http.Server, string, error) {
	proxyLn, err := net.Listen("tcp", net.JoinHostPort(bindHost, "0"))
	if err != nil {
		return nil, "", fmt.Errorf("listen proxy: %w", err)
	}
	proxyBaseURL := fmt.Sprintf("http://%s:%d/", bindHost, proxyLn.Addr().(*net.TCPAddr).Port)
	// The proxy forwards the browser's cookies and auth headers to the target,
	// so it takes the same token: the UI page's cookie covers this port too.
	proxySrv := &http.Server{Handler: token.guard(proxy.NewExternalProxy(target)), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := proxySrv.Serve(proxyLn); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("proxy server", "err", err)
		}
	}()
	return proxySrv, proxyBaseURL, nil
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
// The dot-segment and extension rules apply to both the URL path and the
// file it resolves to (servableTarget), and EvalSymlinks(resolved) must stay under
// EvalSymlinks(root). A link inside the repo passes the URL checks by its own
// name, so checking the URL alone serves whatever the link points at.
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
		if !servablePath(cleaned) {
			next.ServeHTTP(w, r)
			return
		}
		// ResolveWithin refuses a path, or a symlink on it, that leaves the
		// repo; a missing file is a 404 too.
		resolved, err := gitdiff.ResolveWithin(rootResolved, strings.TrimPrefix(cleaned, "/"))
		if err != nil || !servableTarget(rootResolved, resolved) {
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
		// Files from the reviewed tree are content under review, not part of
		// the app: run them in a sandbox (an opaque origin, no access to
		// prereview's cookies or socket) and never let a browser guess a type.
		w.Header().Set("Content-Security-Policy", "sandbox")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		// http.ServeContent sets Content-Type via mime.TypeByExtension,
		// honours Range, and handles If-Modified-Since for 304s.
		http.ServeContent(w, r, resolved, info.ModTime(), f)
	})
}

// servablePath reports whether a slash-rooted repo path may be served: not the
// root, no dot segment, and an allowlisted extension.
func servablePath(p string) bool {
	return p != "/" && !hasDotComponent(p) && staticAllowedExt[strings.ToLower(filepath.Ext(p))]
}

// servableTarget applies servablePath to the file a request resolved to: a
// link inside the repo (logo.png → .env) passes the URL check by its own name,
// so the target must pass it too.
func servableTarget(rootResolved, resolved string) bool {
	rel, err := filepath.Rel(rootResolved, resolved)
	return err == nil && servablePath("/"+filepath.ToSlash(rel))
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

// agentHome is where generated skills are installed ("" disables them).
func agentHome() string {
	h, err := skills.AgentHome()
	if err != nil {
		slog.Warn("no home folder for agent skills; generated skills are off", "err", err)
		return ""
	}
	return h
}

// newTemplate builds the page template for both modes. Diff payloads are
// large, highly repetitive HTML (1000+ `<div class="line-row"><button…`
// rows); permessage-deflate compresses them ~10x on the wire, the dominant
// win on the phone-over-Tailscale path. origins, when set, is the WebSocket
// origin allow-list (#65); nil keeps livetemplate's same-origin check. Never
// DevMode or a permissive check: the origin check is the CSWSH guard.
func newTemplate(files []string, origins []string) (*livetemplate.Template, error) {
	opts := []livetemplate.Option{livetemplate.WithParseFiles(files...), livetemplate.WithWebSocketCompression()}
	if len(origins) > 0 {
		opts = append(opts, livetemplate.WithAllowedOrigins(origins))
	}
	return livetemplate.New("prereview", opts...)
}
