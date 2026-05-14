# prereview — implementation tracker

Authoritative plan: `~/.claude/plans/prereview-webapp-to-add-elegant-gosling.md`. Mirrored here so the next session can pick up state directly from the repo.

## Progress

### Session 1 — skeleton + git diff parsing (no UI) ✅
- [x] Create `prereview/` directory
- [x] `prereview/go.mod` (`module github.com/livetemplate/prereview`); add `./prereview` to `go.work`
- [x] `Makefile` with `sync-client`, `build`, `test`
- [x] `.gitignore` for `internal/assets/client/*.js`
- [x] `gitdiff/gitdiff.go` — `ListFiles(repo, base) ([]FileEntry, error)` (also surfaces untracked files as 'A')
- [x] `gitdiff/parser.go` — `LoadDiff(repo, base, path) (*FileDiff, error)` (handles A/M/D/R + untracked + binary)
- [x] `gitdiff/parser_test.go` — 8 golden tests against an in-test fixture repo
- [x] `go test ./gitdiff/...` green (8/8)

### Session 2 — rendering + navigation (no comments)
- [ ] `internal/assets/assets.go` with `//go:embed client/*`
- [ ] `make sync-client` succeeds and writes JS into `internal/assets/client/`
- [ ] `main.go` — flag parsing, bind 127.0.0.1, random port if not set, `READY <url>` on stdout, graceful shutdown on SIGINT/SIGTERM
- [ ] `state.go` compiles
- [ ] `controller.go` `Mount` + `SelectFile`
- [ ] `prereview.tmpl` two-pane layout, diff line coloring via classes
- [ ] Chromedp test: opens UI, clicks first file, asserts diff lines render with `line-add`/`line-del`/`line-ctx` classes
- [ ] No console errors in chromedp

### Session 3 — comments + selection + CSV
- [ ] `helpers.go` `selectionContains` template func registered
- [ ] `controller.go` adds `SelectLine`, `ClearSelection`, `SaveDraft`, `AddComment`, `EditComment`, `DeleteComment`
- [ ] Selection highlight emits per-line `["u",…]` ops (verify via WS-message capture in E2E)
- [ ] `csv/schema.go` column constants
- [ ] `csv/writer.go` atomic write with `sync.Mutex` + fsync + rename + parent-dir fsync
- [ ] `csv/writer_test.go` covers crash-mid-write safety
- [ ] `Done` action writes `.prereview/DONE` AFTER CSV fsync
- [ ] `lvt-form:preserve` on draft textarea
- [ ] Native `<dialog>` for delete-confirm (clones `examples/dialog-patterns/`)
- [ ] Chromedp E2E: select range (two clicks) → type comment → submit → assert CSV row → edit → delete → Done → assert DONE marker content

### Session 4 — skill + polish + manual test
- [ ] `skill/SKILL.md` with triggers + usage
- [ ] `skill/reference.md` with CSV schema + flag reference
- [ ] `README.md` install/flags/CSV schema
- [ ] Edge cases: empty diff (banner), binary files (skip with note), files >1MB (warn)
- [ ] `lvt-fx:animate="fade"` + `lvt-fx:highlight="flash"` on comment list
- [ ] Manual test on a real repo with 20+ changed files
- [ ] User signoff before push/PR per `feedback_pr_signoff_gate`

### v2 polish (deferred)
- [ ] Drag-select via separate embedded `prereview-shiftclick.js` reading `event.shiftKey`
- [ ] Tree view when file list > 50
- [ ] Word-level intraline diff
- [ ] Multi-base presets (`--base origin/main`, `--base HEAD~3`)
