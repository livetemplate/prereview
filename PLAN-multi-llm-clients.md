# Plan: prereview multi-LLM-client support — wire prereview into any coding agent, not just Claude

prereview's hand-off mechanism is an open file/stream protocol (`comments.csv`
+ `events.jsonl` + `DONE` marker), so it is already client-agnostic at its
core. Today the only documented/turnkey on-ramp is the Claude Code skill, and a
few user-facing strings + the hand-off button hard-code "Claude". This plan adds
first-class support for **OpenAI Codex CLI, Gemini CLI, aider, opencode, and
cursor-agent** alongside Claude.

## Scope (locked by user)

1. **Docs + protocol** — README repositioning ("works with any LLM CLI; you
   point, the LLM fixes") folded into the hero; a new `docs/integrations.md`
   documenting the open protocol + per-client recipes with honest
   verified/unverified labels.
2. **Per-client installer code** — `prereview --install-skill --client=<id>`
   writes the right command/skill/rules file for each CLI. **No `--client`
   passed → interactive selector** lists the clients to install for (multi-pick).
3. **UI neutrality (chrome only)** — rename the **"Hand off → Claude"** button
   to vendor-neutral **"Hand off →"**. Keep `hero.gif` and the flagship demo
   showing Claude Code as the turnkey path; **no full asset regeneration.**

### Accepted risk (user overrode the recommendation)
Per-client installer paths are unstable for 2 of 5: Codex's skills dir is
unresolved upstream (`~/.codex/skills` vs `~/.agents/skills`); cursor's CLI
honoring of `.cursor/commands` is unconfirmed. **De-risk, don't guess:** install
to the *verified* mechanism per CLI (cursor → `.cursor/rules/*.mdc` + AGENTS.md,
which the CLI definitely reads; Codex → both candidate dirs), unit-test every
write against a temp `$HOME`, and label uncertainty in `docs/integrations.md`.

## Key architectural finding (drives the per-client content)

Only Claude Code robustly supports prereview's **blocking-stream** loop (agent
backgrounds the server and block-reads `events.jsonl` across rounds). Codex,
Gemini, opencode, and cursor-agent either can't background a process or hang on
a blocking read (verified against open issues in each repo); aider has no stream
mode at all. **The portable pattern is one-shot-per-batch:** the agent (or the
human, turn-by-turn) re-reads `.prereview/comments.csv`, applies the unresolved
rows (`resolved=false && anchor_status!=outdated` — a complete snapshot,
verified), and the loop is driven outside the single blocking read.

Therefore: **Claude keeps the streaming SKILL.md; every other client ships a
one-shot/poll instruction** built from one shared generic body + a thin
per-client wrapper (format + path + invocation).

## Client registry (the source of truth for the installer + docs)

| id | mechanism (verified) | install target | filename | invocation |
|---|---|---|---|---|
| `claude` | Claude Code skill (streaming) | `~/.claude/skills/prereview/` | `SKILL.md`+`reference.md` | `/prereview` |
| `codex` | skill (path unresolved → write BOTH) | `~/.codex/skills/prereview/` **and** `~/.agents/skills/prereview/` | `SKILL.md` | `$prereview` / `/skills` |
| `gemini` | TOML custom command | `~/.gemini/commands/` | `prereview.toml` | `/prereview` |
| `aider` | executable `--message` wrapper (no command registry) | `~/.config/prereview/aider/` | `prereview-aider.sh` | `prereview-aider.sh <files>` |
| `opencode` | markdown command | `~/.config/opencode/commands/` | `prereview.md` | `/prereview` or `opencode run --command prereview` |
| `cursor` | rules (commands unconfirmed) | `~/.cursor/rules/` (best-effort) | `prereview.mdc` | `cursor-agent -p --force "use prereview"` |

## Architecture

```
embed:  skill/SKILL.md, skill/reference.md           (claude, streaming — unchanged)
        skill/clients/_body.md                       (shared generic one-shot workflow)
        skill/clients/{codex,gemini,aider,opencode,cursor}.* (thin per-client wrappers)

skill.go:
  type client struct { id, label string; targets []target; render func() []file }
  clients() []client                       // the registry above
  installClient(home, id) ([]string, error)// writes files, returns paths
  selectClients(in, out) ([]string, error) // interactive multi-pick when no --client

main.go:
  --client <id>        // repeatable / comma-list; omitted → selector
  --install-skill      // now drives the registry (claude stays the default single-pick)
  syncInstalledSkill   // unchanged: only ever syncs the claude skill it finds installed

internal/review: button label "Hand off → Claude" → "Hand off →"  (+ e2e)
```

## Implementation phases — progress tracker

Per-phase: `/simplify` first, then unit tests, then (where UI) chromedp e2e,
then run the FULL suite before commit. Full-capture (console + server + WS +
HTML) is used for *behavioral* UI tests; a pure rendered-attribute assertion
(e.g. the vendor-neutral label/tooltip check) piggybacks on an existing
handoff test that already captures server stderr. Pre-commit hook must pass —
never `--no-verify`.

### Phase 1 (Tier 1) — README repositioning + docs/integrations.md ✅
- [x] Fold the pending Option-A hero rewrite into the README hero ("you point, the LLM fixes; works with any LLM CLI")
- [x] Neutralize Claude-only prose where it implies Claude is the *only* path (keep Claude as the turnkey/flagship)
- [x] Add a "Works with any LLM CLI" section pointing at docs/integrations.md
- [x] New `docs/integrations.md`: the open protocol (comments.csv schema, events.jsonl, READY/handoff) + one verified recipe per client + verified/unverified labels
- [x] Also updated docs/cli.md flag table (`--install-skill` reworded, `--client` added)
- [x] Acceptance: links resolve; recipes match the registry; no overclaim a reader can falsify

### Phase 2 (Tier 2) — per-client installer + interactive selector ✅
- [x] `skill/clients/body.md` embedded shared one-shot workflow; per-format wrappers in Go (clients.go)
- [x] `clients.go`: client registry, `installClient`, `selectClients`, `resolveClients`, `parseClientSelection`
- [x] `main.go`: `--client` flag (comma-list); no `--client` → selector; wired into `--install-skill`; post-install strings neutralized
- [x] `skill.go`: `installSkill` delegates to the registry's `claude` target; `syncInstalledSkill` kept claude-only (respects opt-out)
- [x] Acceptance: clients_test.go writes each client's files to a temp `$HOME` (paths+content), codex dual-dir pinned, selection parsing + selector + resolve tested; `GOWORK=off go test ./...` green; generated TOML validated with a real parser

### Phase 3 (Tier 3) — neutralize the hand-off button ✅
- [x] Button label was already "Hand off →"; neutralized the remaining "Claude" in tooltips (prereview.tmpl) and the `--skill` flag help (main.go)
- [x] Acceptance: extended chromedp e2e `TestE2E_HandOffMarker` to assert button text + tooltip are vendor-neutral (no "Claude"); full `GOWORK=off go test -tags=browser ./e2e/` green (201s); screenshots untouched

**Status: all three phases complete; full suite + full browser e2e green. Not yet committed (awaiting user sign-off).**

### Smoke-test results (2026-06-21, real CLIs installed; end-to-end tests KEYLESS)
- **Bug #1 found + fixed:** unquoted YAML `description:` containing a colon broke
  skill loading (Codex rejected it). Fixed via `yamlQuote` in codex/opencode/
  cursor frontmatter; regression tests `TestYAMLFrontmatterDescriptionQuoted` +
  `TestYAMLQuote`.
- **Bug #2 found + fixed:** aider's `--load`/`/code` is **skipped
  non-interactively** ("only supported in interactive mode") and never applies
  edits (littered a stray file). Rewrote the aider integration as an executable
  **`--message` wrapper** (`prereview-aider.sh`, mode 0755; added `mode` field to
  `clientFile`); regression test `TestAiderWrapper`.
- **opencode (1.17.9):** ✅ full end-to-end, KEYLESS (free hosted model) —
  discovered command, read CSV+DONE, fixed the file.
- **aider (0.86.2, py3.12):** ✅ full end-to-end, KEYLESS (local ollama
  qwen2.5-coder:7b, `--edit-format diff`) — wrapper applied the fix.
- **codex (0.130.0):** 🟡 skill loads from BOTH `~/.codex/skills` and
  `~/.agents/skills` (dual-write confirmed); apply needs a capable model —
  ChatGPT account had no entitlement, and a small local model (`--oss`) couldn't
  drive codex's tool harness.
- **gemini (0.47.0):** 🟡 `/prereview` discovered + expanded in headless mode;
  apply needs `GEMINI_API_KEY` (OAuth login needs an interactive terminal).
- **cursor-agent:** 🔶 install only; run needs `CURSOR_API_KEY`.
- Labels in README + docs/integrations.md updated to ✅/🟡/🔶 accordingly.
- Test env: aider needs **Python 3.12** (3.13 removed `audioop`); keyless runs
  used a user-local **ollama** (binary in `~/.local/ollama`, model in `~/.ollama`).

## Verification
- `GOWORK=off go build ./... && GOWORK=off go test ./...` (worktree: GOWORK=off so go builds worktree code, matching CI)
- `GOWORK=off go test -tags=browser ./e2e/`
- Manual: `--install-skill` selector flow + one `--client=<id>` write inspected on disk

## Risks
- **[accepted] Unstable per-client paths** — mitigated by writing verified mechanisms + both Codex dirs + honest docs labels.
- **aider/cursor have no clean user-global command registry** — handled via a `--load` script (aider) and `~/.cursor/rules/` (cursor, best-effort); docs state the exact invocation.
- **Non-Claude agents can't block-loop** — content uses the one-shot/poll pattern, not the streaming loop.
