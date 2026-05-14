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
	"github.com/livetemplate/prereview/internal/assets"
)

//go:embed prereview.tmpl
var prereviewTemplate string

// Version set via -ldflags at build time.
var version = "dev"

func main() {
	repo := flag.String("repo", ".", "absolute path to the git repository to review")
	base := flag.String("base", "HEAD", "git base for comparison (default HEAD = working tree vs last commit)")
	port := flag.Int("port", 0, "TCP port to listen on (0 = random free port)")
	host := flag.String("host", "127.0.0.1", "host/IP to bind on (default 127.0.0.1, localhost-only)")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}

	if err := run(*repo, *base, *host, *port); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(repo, base, host string, port int) error {
	absRepo, err := filepath.Abs(repo)
	if err != nil {
		return fmt.Errorf("resolve repo path: %w", err)
	}
	if err := assertGitRepo(absRepo); err != nil {
		return err
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
	)
	if err != nil {
		return fmt.Errorf("livetemplate.New: %w", err)
	}

	controller := &PrereviewController{RepoPath: absRepo, Base: base}
	initial := &PrereviewState{
		RepoPath:  absRepo,
		Base:      base,
		StartedAt: time.Now().Format("2006-01-02 15:04:05"),
	}

	mux := http.NewServeMux()
	mux.Handle("/", tmpl.Handle(controller, livetemplate.AsState(initial)))
	mux.HandleFunc("/livetemplate-client.js", serveBytes("application/javascript", assets.ClientJS()))
	mux.HandleFunc("/livetemplate.css", serveBytes("text/css", assets.ClientCSS()))

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
