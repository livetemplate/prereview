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

### Session 2 — rendering + navigation (no comments) ✅
- [x] `internal/assets/assets.go` with `//go:embed client/*` (JS + CSS)
- [x] `make sync-client` succeeds and writes JS+CSS into `internal/assets/client/`
- [x] `main.go` — flag parsing, bind 127.0.0.1, random port (port=0), `READY <url>` on stdout, graceful shutdown on SIGINT/SIGTERM
- [x] `state.go` compiles
- [x] `controller.go` `Mount` + `SelectFile`
- [x] `prereview.tmpl` two-pane layout, diff line coloring via classes (`line.add`, `line.del`, `line.ctx`)
- [x] Chromedp test (`e2e_test.go`, build tag `browser`): opens UI, clicks edited file, asserts add+del+ctx; clicks untracked file, asserts all-add
- [x] No console errors in chromedp run

### Session 3 — comments + selection + CSV ✅
- [x] Selection driven by `PrereviewState.SelectedLines() map[int]bool` zero-arg method (the livetemplate framework only pre-computes zero-arg methods, so `SelectionContains(n int)` would NOT be callable from the template; using `{{index $.SelectedLines $ln}}` works instead)
- [x] `controller.go` adds `SelectLine` (two-click range), `ClearSelection`, `SaveDraft`, `AddComment`, `EditComment` (delete-and-reseed), `DeleteComment`, `Done`
- [x] `csv/schema.go` column constants — load-bearing for skill contract
- [x] `csv/writer.go` atomic write: `sync.Mutex` + `tmp` + `fsync` + `rename` + parent-dir `fsync`
- [x] `csv/writer_test.go`: header, RFC-4180 multi-line bodies, rewrite-replaces, empty-list, no-tmp-leak, concurrent stress (6 tests)
- [x] `Done` action writes `.prereview/DONE` AFTER the CSV is fsynced; DONE contains the CSV path
- [x] `lvt-form:preserve` on draft textarea so unsubmitted edits survive re-renders
- [x] Native `<dialog command="show-modal" commandfor=...>` for delete confirm
- [x] Chromedp E2E covers: file pick → two-click range → type → save → CSV row verified → Edit → re-save → CSV updated → open delete dialog → confirm → CSV emptied → Done → DONE marker contains valid CSV path

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
