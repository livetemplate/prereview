package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	_ "embed"

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

	// Keep an already-installed skill in lockstep with this binary. Runs
	// after the (possibly re-exec'd) update so the *new* binary's embedded
	// skill text is what lands on disk. Orthogonal to the binary lifecycle,
	// so it sits outside the update/package-manager gates and covers brew,
	// Scoop, and `go install` upgrades too. Best-effort: a review must start
	// regardless.
	if home, err := os.UserHomeDir(); err != nil {
		slog.Debug("skill sync: resolve home", "err", err)
	} else if changed, serr := syncInstalledSkill(home); serr != nil {
		slog.Debug("skill sync failed", "err", serr)
	} else if changed {
		fmt.Fprintln(os.Stderr, "prereview: refreshed the Claude skill at ~/.claude/skills/prereview/ to match this version.")
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
