package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	_ "embed"

	"github.com/livetemplate/livetemplate"
	"github.com/livetemplate/prereview/csv"
	"github.com/livetemplate/prereview/gitdiff"
	"github.com/livetemplate/prereview/internal/assets"
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

func main() {
	repo := flag.String("repo", ".", "absolute path to the git repository to review")
	base := flag.String("base", "HEAD", "git base for comparison (default HEAD = working tree vs last commit)")
	port := flag.Int("port", 0, "TCP port to listen on (0 = random free port)")
	host := flag.String("host", "127.0.0.1", "host/IP to bind on (default 127.0.0.1, localhost-only)")
	skill := flag.Bool("skill", false, "running under the Claude skill: show 'Hand off → Claude' button that writes .prereview/DONE; default UI shows 'Quit' instead")
	showVersion := flag.Bool("version", false, "print version and exit")
	doInstallSkill := flag.Bool("install-skill", false, "install the Claude Code skill into ~/.claude/skills/prereview/ and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
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

	if err := run(*repo, *base, *host, *port, *skill); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(repo, base, host string, port int, skillMode bool) error {
	absRepo, err := filepath.Abs(repo)
	if err != nil {
		return fmt.Errorf("resolve repo path: %w", err)
	}
	if err := assertGitRepo(absRepo); err != nil {
		return err
	}

	// .prereview/ holds the CSV and the DONE marker. Create it eagerly so
	// the skill's polling loop has a stable directory to watch.
	prereviewDir := filepath.Join(absRepo, ".prereview")
	if err := os.MkdirAll(prereviewDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", prereviewDir, err)
	}
	startedAt := time.Now()
	// Fixed CSV filename — survives server restarts so users can resume
	// editing where they left off. (Earlier versions timestamped the
	// filename per session, which orphaned previous comments on restart.)
	csvPath := filepath.Join(prereviewDir, "comments.csv")
	donePath := filepath.Join(prereviewDir, "DONE")
	// Wipe any stale DONE marker from a previous session so the skill
	// doesn't read it and exit before the user has done anything.
	_ = os.Remove(donePath)
	csvWriter := csv.NewWriter(csvPath)

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
		CSVPath:     csvPath,
		DonePath:    donePath,
		CSVWriter:   csvWriter,
		SkillMode:   skillMode,
		ShutdownReq: shutdownReq,
	}
	initial := &PrereviewState{
		RepoPath:  absRepo,
		Base:      base,
		StartedAt: startedAt.Format("2006-01-02 15:04:05"),
		CSVPath:   csvPath,
		Comments:  initialComments,
		SkillMode: skillMode,
	}

	mux := http.NewServeMux()
	mux.Handle("/", tmpl.Handle(controller, livetemplate.AsState(initial)))
	mux.HandleFunc("/livetemplate-client.js", serveBytes("application/javascript", assets.ClientJS()))
	mux.HandleFunc("/livetemplate.css", serveBytes("text/css", assets.ClientCSS()))
	mux.HandleFunc("/syntax.css", serveBytes("text/css", []byte(gitdiff.HighlightCSS)))

	addr := net.JoinHostPort(host, fmt.Sprintf("%d", port))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", addr, err)
	}
	actual := ln.Addr().(*net.TCPAddr)

	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	url := fmt.Sprintf("http://%s:%d", host, actual.Port)
	fmt.Printf("READY %s\n", url)
	slog.Info("prereview started", "url", url, "repo", absRepo, "base", base)

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
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	return nil
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

// assertGitRepo verifies path looks like a git repo. We don't bail on
// non-existent .git/ — git diff itself will produce a clear error and the
// user can pass --repo correctly.
func assertGitRepo(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("repo %q: %w", path, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("repo %q is not a directory", path)
	}
	dotGit := filepath.Join(path, ".git")
	if _, err := os.Stat(dotGit); err != nil {
		// Allow worktrees where .git is a file, not a directory.
		if errors.Is(err, os.ErrNotExist) || !strings.Contains(err.Error(), "is a directory") {
			return fmt.Errorf("repo %q does not contain a .git entry (pass --repo /path/to/repo)", path)
		}
	}
	return nil
}
