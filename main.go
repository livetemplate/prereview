package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"embed"

	"github.com/livetemplate/prereview/internal/update"
)

// topUsage is the top-level help: the review-launch synopsis plus the agent-
// facing subcommands, so `prereview help` (or a bad flag) surfaces every verb
// instead of only the launch flags — a bare `prereview` launches a review.
const topUsage = `Usage: prereview [flags] [path]

  path   git repo, non-git directory, or single file to review (default: current dir).
         Flags must come before the path, e.g. ` + "`prereview --agent ./docs`" + `.

Subcommands (for the coding agent; each takes --out <REPO>):
  comments   list the review's comments (--json for the queue-snapshot shape; --all for resolved too)
  done       mark comments worked on (validated against comments.csv; --file -/--all-open)
  reply      post a thread reply on a comment or suggestion so the reviewer sees what you did (--body/--file)
  status     echo the agent's status to the review UI: status <working|done> [message]
  suggest    submit proposed edits as inline suggestion boxes (--file/stdin)
  watch      deliver the next batch of queue events after --since <seq> (blocks when caught up), until the terminating end event
  help       show this message

  Run a subcommand with -h for its own flags, e.g. ` + "`prereview done -h`" + `.
`

// templatesFS holds the split template set: page.tmpl (the page shell — the
// entry template) plus partials.tmpl (reusable comment/region render partials)
// and icons.tmpl (SVG icon {{define}}s). They are staged to a temp dir at
// startup because livetemplate.New requires template files on disk
// (see stageTemplates). The output-equivalence guard concatenates them in
// templateOrder and stays byte-identical to the pre-split monolith.
//
//go:embed templates/*.tmpl
var templatesFS embed.FS

// templateOrder is the canonical parse order: page.tmpl MUST be first — it is
// livetemplate's main template (its top-level markup becomes "prereview"); the
// other files contribute only {{define}} partials and are parsed into the same
// set. Single source of truth shared by stageTemplates and the signature guard.
var templateOrder = []string{"page.tmpl", "partials.tmpl", "icons.tmpl"}

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
	// `prereview done [--out <dir>] <id>...` — the coding agent marks
	// comments it has addressed so the live review UI badges them "worked on".
	// A bare positional verb, so intercept it before flag parsing (which would
	// otherwise treat "done" as the review path).
	if len(os.Args) > 1 && os.Args[1] == "done" {
		if err := runDone(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "prereview done:", err)
			os.Exit(1)
		}
		return
	}

	// `prereview suggest [--out <dir>] [--file <f>]` — the coding agent submits
	// proposed edits (from a JSON payload on stdin/--file) that the live review UI
	// renders as inline suggestion boxes (#98). A bare positional verb like
	// `done`, so intercept it before flag parsing.
	if len(os.Args) > 1 && os.Args[1] == "suggest" {
		if err := runSuggest(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "prereview suggest:", err)
			os.Exit(1)
		}
		return
	}

	// `prereview reply [--out <dir>] (--body "…"|--file <f>) <id>` — the coding
	// agent posts a thread reply on a comment or suggestion so the reviewer sees
	// what it did (#149). A bare positional verb like `done`/`suggest`, so intercept
	// it before flag parsing.
	if len(os.Args) > 1 && os.Args[1] == "reply" {
		if err := runReply(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "prereview reply:", err)
			os.Exit(1)
		}
		return
	}

	// `prereview watch [--out <dir>] [--since <seq>]` — the coding agent consumes
	// the review's JSON event stream (the durable events.jsonl the server writes in
	// --agent mode), resuming from a seq cursor. A bare positional verb like
	// `done`/`suggest`, so intercept it before flag parsing.
	if len(os.Args) > 1 && os.Args[1] == "watch" {
		if err := runWatch(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "prereview watch:", err)
			os.Exit(1)
		}
		return
	}

	// `prereview comments [--out <dir>] [--json] [--all]` — enumerate the review's
	// comments from a stable interface (feeds `prereview done`). A bare
	// positional verb like the others, so intercept it before flag parsing.
	if len(os.Args) > 1 && os.Args[1] == "comments" {
		if err := runComments(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "prereview comments:", err)
			os.Exit(1)
		}
		return
	}

	// `prereview status <working|done> [message] [--out <dir>]` — the coding agent
	// echoes what it's doing so the live review UI shows a status pill. A bare
	// positional verb like the others, so intercept it before flag parsing.
	if len(os.Args) > 1 && os.Args[1] == "status" {
		if err := runStatus(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "prereview status:", err)
			os.Exit(1)
		}
		return
	}

	// `prereview help` (or -h/--help with no other args) — list the subcommands so
	// an agent doesn't accidentally launch a server when it meant to query. Bare
	// `prereview` still launches a review of the current directory (the default).
	if len(os.Args) > 1 && (os.Args[1] == "help" || os.Args[1] == "-h" || os.Args[1] == "--help") {
		fmt.Print(topUsage)
		return
	}

	flag.Usage = func() {
		fmt.Fprint(flag.CommandLine.Output(), topUsage+"\nFlags:\n")
		flag.PrintDefaults()
	}
	base := flag.String("base", "HEAD", "git base for comparison (default HEAD = working tree vs last commit); ignored for a non-git dir or single file")
	external := flag.String("external", "", "annotate a live local website instead of files: reverse-proxies this http(s):// URL and overlays the region-annotation UI. Requires --out. Ignores [path]/--base.")
	out := flag.String("out", "", "directory whose .prereview/ holds the saved annotations (comments.csv). Defaults to the review path; required with --external (which has no review path).")
	port := flag.Int("port", 0, "TCP port to listen on (0 = random free port)")
	host := flag.String("host", "127.0.0.1", "host/IP to bind on. Unset on a remote (SSH) box, prereview auto-binds to this host's Tailscale IP so a phone can reach it without exposing it publicly; locally it stays 127.0.0.1. Pass an explicit value to override.")
	agent := flag.Bool("agent", false, "run under a coding agent: stream the review queue as JSON events (consume with `prereview watch`); shows the Queue (Pause/Resume) + End session UI")
	skill := flag.Bool("skill", false, "deprecated alias for --agent")
	stream := flag.Bool("stream", false, "deprecated alias for --agent")
	showVersion := flag.Bool("version", false, "print version and exit")
	doInstallSkill := flag.Bool("install-skill", false, "install the prereview integration for one or more coding agents and exit (choose with --client; omit it to pick from a menu)")
	clientFlag := flag.String("client", "", "agent(s) to install the integration for: a comma-separated list of claude,codex,gemini,opencode,aider,cursor (with --install-skill; empty shows an interactive menu)")
	doUpdate := flag.Bool("update", false, "download and install the latest prereview release from GitHub, then exit")
	doUninstall := flag.Bool("uninstall", false, "remove the prereview binary from disk, then exit (your review comments in each repo's .prereview/ are left untouched)")
	noUpdate := flag.Bool("no-update", false, "skip the on-run update check (also honoured via PREREVIEW_NO_UPDATE=1)")
	replace := flag.Bool("replace", false, "if a prereview server is already running for this repo's store, stop it and take over (otherwise launch errors)")
	flag.Parse()

	// --agent is the single agent-mode flag. --skill/--stream are kept as
	// deprecated aliases so existing skills/scripts keep launching agent mode;
	// warn once on stderr when only a legacy alias was passed.
	agentMode := *agent || *skill || *stream
	if (*skill || *stream) && !*agent {
		fmt.Fprintln(os.Stderr, "prereview: --skill/--stream are deprecated; use --agent")
	}

	// flag can't tell "user passed --host 127.0.0.1" from "default
	// 127.0.0.1" by value alone, and that distinction is load-bearing:
	// an explicit --host is an absolute operator override (we never
	// auto-rebind over it), the default is just a starting point we may
	// replace with the Tailscale IP on a remote box. flag.Visit only
	// reports flags actually set on the command line.
	// explicitBase is the same distinction for --base: an explicit base
	// (--base main, HEAD~3, a tag) is honored as-is, while the default "HEAD"
	// is a starting point we may swap for the empty tree on a clean working
	// tree (so every line is commentable). See run().
	explicitHost := false
	explicitBase := false
	flag.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "host":
			explicitHost = true
		case "base":
			explicitBase = true
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
		ids, err := resolveClients(*clientFlag)
		if err != nil {
			slog.Error("install skill", "err", err)
			os.Exit(1)
		}
		if len(ids) == 0 {
			fmt.Println("No agent selected; nothing installed.")
			return
		}
		for _, id := range ids {
			paths, err := installClient(home, id)
			if err != nil {
				slog.Error("install skill", "err", err)
				os.Exit(1)
			}
			t, _ := clientByID(id)
			fmt.Printf("Installed prereview integration for %s → %s\n", t.label, strings.Join(paths, ", "))
			fmt.Printf("  %s\n", t.hint)
		}
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
	skillUpdated := false
	if home, err := os.UserHomeDir(); err != nil {
		slog.Debug("skill sync: resolve home", "err", err)
	} else if changed, serr := syncInstalledSkill(home); serr != nil {
		slog.Debug("skill sync failed", "err", serr)
	} else {
		skillUpdated = changed
	}
	if skillUpdated {
		// The refreshed skill on disk does NOT reload into an already-running
		// agent's context, so its loaded copy is now stale. Make the drift
		// actionable for both audiences: the agent must re-read the skill, and
		// the human must reload it in their agent. In agent mode the `ready`
		// event also carries skill_updated:true (the reliable machine channel —
		// stderr may be detached on a backgrounded launch).
		fmt.Fprintln(os.Stderr, "prereview: refreshed the prereview skill at ~/.claude/skills/prereview/ to match this binary. Re-read that SKILL.md before continuing, and reload the skill in your agent.")
	}

	if *external != "" {
		if err := runExternal(*external, *out, *host, explicitHost, *port, agentMode, skillUpdated, *replace); err != nil {
			slog.Error("fatal", "err", err)
			os.Exit(1)
		}
		return
	}

	if err := run(reviewPath(flag.Args()), *base, *host, explicitHost, explicitBase, *port, agentMode, skillUpdated, *out, *replace); err != nil {
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
