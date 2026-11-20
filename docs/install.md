# Installing prereview

prereview is one Go binary with everything embedded. It needs `git` on your
`$PATH`. Install it one of these ways, then install the integration for your agent.

```bash
# Quick install (macOS / Linux): downloads the latest release, checksum-verified
curl -fsSL https://raw.githubusercontent.com/livetemplate/prereview/main/install.sh | sh
```

```bash
# Homebrew (macOS / Linux)
brew tap livetemplate/prereview https://github.com/livetemplate/prereview
brew trust --formula livetemplate/prereview/prereview   # once: brew asks you to trust third-party taps
brew install livetemplate/prereview/prereview
```

```powershell
# Windows (Scoop)
scoop bucket add prereview https://github.com/livetemplate/prereview
scoop install prereview/prereview
```

```bash
# Go toolchain
go install github.com/livetemplate/prereview@latest
```

The quick-install script takes two settings: `PREREVIEW_INSTALL_DIR=/path` and
`PREREVIEW_VERSION=vX.Y.Z` (pin a release; leave it out for the latest).

For what prereview reads, writes, connects to and runs, see
[security.md](security.md).

## Your agent's integration

The binary carries the integration for every supported agent:

```bash
prereview --install-skill                         # pick agents from a menu
prereview --install-skill --client=claude         # Claude Code → ~/.claude/skills/prereview/
prereview --install-skill --client=codex,gemini   # several at once
```

The menu offers Claude Code, Codex, Gemini, opencode, aider and cursor-agent.
Then start a review from your agent: in Claude Code, `/prereview` or "review my
changes" (if it reports an unknown skill, run `/reload`). Each agent's files are
refreshed to match the binary on the next run after an upgrade, so you never run
`--install-skill` again to keep them current. A file you edited is left alone,
and prereview says so. Per-agent paths and caveats are in
[integrations.md](integrations.md#keeping-the-integration-current).

To check the integration into a project instead, copy it from a clone:

```bash
mkdir -p .claude/skills/prereview
cp skill/SKILL.md .claude/skills/prereview/SKILL.md
```

The file name must be `SKILL.md`, in capitals: a lowercase `skill.md` is ignored.
`skill/reference.md` (the CSV schema and file layout) is worth copying alongside.

## Behind a corporate proxy

If the Go module proxy is blocked, try these in order:

1. the default (`proxy.golang.org`);
2. `GOPROXY=direct GOSUMDB=off go install …@latest`, which still needs every
   dependency's host;
3. an internal `GOPROXY=…`;
4. fully offline: the quick-install script or Homebrew, which download one
   prebuilt binary and fetch no modules.

## Upgrading

`brew upgrade prereview`, `scoop update prereview`, re-run the install script, or
`prereview --update`. prereview makes no network calls unless you ask. Set
`PREREVIEW_AUTO_UPDATE=1` to have curl and `go install` binaries check for a new
release when they start (at most hourly).

## Uninstalling

`brew uninstall prereview`, `scoop uninstall prereview`, `prereview --uninstall`
(it hands over to brew or scoop when one of them installed it), or
`rm "$(go env GOPATH)/bin/prereview"`. Your reviews' `.prereview/` folders are
never touched.

Uninstalling removes only the binary. These stay until you delete them:

- each agent's integration, such as `~/.claude/skills/prereview/`;
- the skills prereview generated from your learnings: `learned-*` folders in each
  agent's skills directory;
- the block between `<!-- prereview:learned-skills -->` markers in
  `~/.claude/CLAUDE.md`, `~/.codex/AGENTS.md`, `~/.gemini/GEMINI.md` and
  opencode's `~/.config/opencode/AGENTS.md`;
- the `prereview check --hook` entry under `hooks.PostToolUse` in
  `~/.claude/settings.json` (the file before prereview changed it is
  `settings.json.prereview-bak`);
- the library folder: your learnings, checks, shortcuts and prompts, in
  `~/.config/prereview` on Linux and `~/Library/Application Support/prereview` on
  macOS;
- the update-check cache.

To remove the agent blocks and the hook while keeping prereview, turn off **Tell
agents to use these skills** on Library → Skills.
