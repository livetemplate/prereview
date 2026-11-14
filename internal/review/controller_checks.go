package review

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/livetemplate/livetemplate"
	"github.com/livetemplate/prereview/gitdiff"
	"github.com/livetemplate/prereview/internal/checks"
)

// FileChecks is the selected file's check outcome, attached to state by
// applyChecks. Plain data (state is JSON-cloned). Results follow the library's
// order; an async check (a command, vale) shows StatusRunning until its result
// arrives and ChecksUpdated re-applies.
type FileChecks struct {
	Results []checks.Result `json:"results"`
	// ValeNotice is set when the repo has a Vale config that isn't being applied:
	// you haven't trusted it yet (ValeTrust offers the button), or vale isn't
	// installed.
	ValeNotice string `json:"vale_notice,omitempty"`
	ValeTrust  bool   `json:"vale_trust,omitempty"`
}

// Vale modes for a repo with a Vale config. A config can make vale start other
// tools (an XML Transform runs xsltproc on a stylesheet from the repo), so it
// runs only after you trust the repo: opening a review never runs what the repo
// configured on its own.
const (
	valeOff       = ""          // no config
	valeUntrusted = "untrusted" // config, not trusted: a notice with the trust button
	valeNoBinary  = "nobin"     // trusted, vale not installed: a notice
	valeRun       = "run"       // trusted and installed
)

// ChecksPollInterval is how often the rule folder is polled for edits.
const ChecksPollInterval = time.Second

// checkCacheSize bounds the result cache (entries are per file × content × library).
const checkCacheSize = 64

// checkRunner owns the rule library and a result cache keyed by (path, displayed
// text, library hash), so the render path pays for a check once per content.
type checkRunner struct {
	mu    sync.Mutex
	lib   *checks.Library
	stamp string
	// valeCfg / valeBin: the repo's Vale config and the vale binary, resolved on
	// load and on each watcher tick rather than stat'd per diff.
	valeCfg bool
	valeBin string
	cache   map[string]*checkEntry
	order   []string // cache keys, oldest first
	// keys memoises a diff's content key: cached FileDiffs are shared pointers, so
	// a file re-shown (or a fan-out to every tab) doesn't re-hash its text.
	keys map[*gitdiff.FileDiff]string
	// inflight cancels a path's background run when newer content supersedes it.
	inflight map[string]context.CancelFunc
}

type checkEntry struct {
	results    []checks.Result
	valeNotice string
}

func (c *PrereviewController) runner() *checkRunner {
	c.checksOnce.Do(func() {
		r := &checkRunner{cache: map[string]*checkEntry{}, keys: map[*gitdiff.FileDiff]string{},
			inflight: map[string]context.CancelFunc{}}
		r.lib = checks.Load(checks.Dir{Path: c.ChecksDir, Source: checks.SourceUser})
		r.stamp = checks.Stamp(c.ChecksDir)
		r.valeCfg, r.valeBin = c.valeState()
		c.checks = r
	})
	return c.checks
}

// valeState resolves whether the repo has a Vale config and where vale is.
func (c *PrereviewController) valeState() (bool, string) {
	if c.RepoPath == "" || checks.ValeConfig(c.RepoPath) == "" {
		return false, ""
	}
	return true, checks.ValeBinary()
}

// reloadChecksIfChanged reloads the library when the rule folder's files moved,
// and refreshes the Vale lookup. Returns true when anything changed, so the
// caller fans it out.
func (c *PrereviewController) reloadChecksIfChanged() bool {
	r := c.runner()
	stamp := checks.Stamp(c.ChecksDir)
	cfg, bin := c.valeState()
	r.mu.Lock()
	defer r.mu.Unlock()
	if stamp == r.stamp && cfg == r.valeCfg && bin == r.valeBin {
		return false
	}
	if stamp != r.stamp {
		r.stamp = stamp
		r.lib = checks.Load(checks.Dir{Path: c.ChecksDir, Source: checks.SourceUser})
	}
	if cfg != r.valeCfg || bin != r.valeBin {
		r.valeCfg, r.valeBin = cfg, bin
		clear(r.cache) // the key has no Vale part; results with the old setup are stale
		r.order = nil
	}
	return true
}

// WatchChecks polls the rule folder and re-applies checks in every open tab when
// a rule file is added, edited or removed, so an edit in your editor shows up
// live. Runs in every mode (unlike WatchLLMStatus, which is agent-only).
func (c *PrereviewController) WatchChecks(stop <-chan struct{}, poll time.Duration) {
	if c.ExternalMode {
		return
	}
	c.runner()
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			if c.reloadChecksIfChanged() {
				c.broadcast("ChecksUpdated")
			}
		}
	}
}

// ChecksUpdated re-applies checks after a rule edit or an async result. Dispatched
// by the watcher / runner to every tab; each re-reads against its own view.
func (c *PrereviewController) ChecksUpdated(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	c.applyChecks(&state)
	return state, nil
}

// showDiff is the one way the live view's diff changes: it sets CurrentDiff and
// re-applies checks, so findings can never lag the text on screen. A historical
// version view sets it through showHistoricalDiff, which clears checks.
// TestShowDiffIsTheOnlyLiveAssignment guards this.
func (c *PrereviewController) showDiff(state *PrereviewState, diff *gitdiff.FileDiff) {
	state.CurrentDiff = diff
	c.applyChecks(state)
}

// showHistoricalDiff shows a read-only past version. Checks don't run there: the
// text isn't the file on disk, so a finding couldn't be queued against it.
func (c *PrereviewController) showHistoricalDiff(state *PrereviewState, diff *gitdiff.FileDiff) {
	state.CurrentDiff = diff
	state.Checks = nil
}

// applyChecks attaches the displayed file's check results to state. Data rules run
// inline (RE2, linear time); commands and vale run once per content in the
// background and land via ChecksUpdated.
func (c *PrereviewController) applyChecks(state *PrereviewState) {
	state.Checks = nil
	d := state.CurrentDiff
	if c.ExternalMode || d == nil || d.IsBinary || state.ViewingVersion || len(d.Lines) == 0 {
		return
	}
	r := c.runner()
	r.mu.Lock()
	lib, valeCfg, valeBin := r.lib, r.valeCfg, r.valeBin
	key, keyed := r.keys[d]
	r.mu.Unlock()

	var rules []*checks.Rule
	for _, rule := range lib.Rules {
		if rule.Applies(d.Path) {
			rules = append(rules, rule)
		}
	}
	mode := valeOff
	switch {
	case !valeCfg:
	case !slices.Contains(state.TrustedVale, c.RepoPath):
		mode = valeUntrusted
	case valeBin == "":
		mode = valeNoBinary
	default:
		mode = valeRun
	}
	if len(rules) == 0 && mode == valeOff {
		return
	}
	lines := sideContent(d, "new")
	if len(lines) == 0 {
		return // a deleted file: nothing to check
	}
	if !keyed || !strings.HasSuffix(key, lib.Hash) {
		key = checkKey(d.Path, lines, lib.Hash)
		r.mu.Lock()
		if len(r.keys) >= checkCacheSize {
			clear(r.keys)
		}
		r.keys[d] = key
		r.mu.Unlock()
	}
	key += "\x00vale=" + mode // trusting a repo's Vale config is a different result
	r.mu.Lock()
	e := r.cache[key]
	r.mu.Unlock()
	if e == nil {
		e = c.startChecks(key, d.Path, lines, rules, mode, valeBin)
	}
	r.mu.Lock()
	state.Checks = &FileChecks{Results: slices.Clone(e.results), ValeNotice: e.valeNotice, ValeTrust: mode == valeUntrusted}
	r.mu.Unlock()
}

// startChecks computes the inline results, records running placeholders for the
// async ones, caches the entry, and starts the background runs — cancelling the
// path's previous run, whose content is now stale.
func (c *PrereviewController) startChecks(key, path string, lines []string, rules []*checks.Rule, mode, valeBin string) *checkEntry {
	e := &checkEntry{}
	type job struct {
		idx  int
		rule *checks.Rule
	}
	var jobs []job
	doc := checks.NewDoc(path, lines)
	for _, rule := range rules {
		if rule.Async() {
			jobs = append(jobs, job{len(e.results), rule})
			e.results = append(e.results, checks.RunningResult(rule))
			continue
		}
		e.results = append(e.results, checks.RunInline(rule, doc))
	}
	switch mode {
	case valeUntrusted:
		e.valeNotice = "This repo has a Vale config. Running it can start tools the config names, so it stays off until you trust this repo."
	case valeNoBinary:
		e.valeNotice = "This repo has a Vale config, but vale isn't installed, so its rules aren't applied."
	case valeRun:
		placeholder := checks.ValeSummary(c.RepoPath)
		placeholder.Status = checks.StatusRunning
		e.results = append(e.results, placeholder)
	}
	if mode != valeRun {
		valeBin = ""
	}

	r := c.runner()
	async := len(jobs) > 0 || valeBin != ""
	var ctx context.Context
	r.mu.Lock()
	if prev := r.cache[key]; prev != nil { // another tab got here first
		r.mu.Unlock()
		return prev
	}
	r.cache[key] = e
	r.order = append(r.order, key)
	for len(r.order) > checkCacheSize {
		delete(r.cache, r.order[0])
		r.order = r.order[1:]
	}
	if async {
		if cancel := r.inflight[path]; cancel != nil {
			cancel()
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithCancel(context.Background())
		r.inflight[path] = cancel
	}
	r.mu.Unlock()
	if !async {
		return e
	}

	go func() {
		var wg sync.WaitGroup
		if len(jobs) > 0 {
			abs := filepath.Join(c.RepoPath, path)
			content := []byte(strings.Join(lines, "\n") + "\n")
			for _, j := range jobs {
				wg.Add(1)
				go func() {
					defer wg.Done()
					res := checks.RunCommand(ctx, j.rule, abs, content)
					r.mu.Lock()
					e.results[j.idx] = res
					r.mu.Unlock()
				}()
			}
		}
		if valeBin != "" {
			wg.Add(1)
			go func() {
				defer wg.Done()
				vres := checks.RunVale(ctx, valeBin, c.RepoPath, path)
				r.mu.Lock()
				// Replace the running placeholder (always last) with Vale's rows.
				e.results = append(e.results[:len(e.results)-1], vres...)
				r.mu.Unlock()
			}()
		}
		wg.Wait()
		r.mu.Lock()
		superseded := ctx.Err() != nil
		if superseded {
			delete(r.cache, key) // its runs were killed; rerun if this content returns
		} else {
			delete(r.inflight, path)
		}
		r.mu.Unlock()
		if !superseded {
			c.broadcast("ChecksUpdated")
		}
	}()
	return e
}

// checkKey identifies one file's content under one library; it ends with the
// library hash so a memoised key can be checked against a reloaded library.
func checkKey(path string, lines []string, libHash string) string {
	h := sha256.New()
	for _, l := range lines {
		h.Write([]byte(l))
		h.Write([]byte{'\n'})
	}
	return path + "\x00" + hex.EncodeToString(h.Sum(nil)) + "\x00" + libHash
}

// ToggleChecksPanel opens or closes the file's checks panel.
func (c *PrereviewController) ToggleChecksPanel(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	state.ChecksOpen = !state.ChecksOpen
	return state, nil
}

// SelectCheck shows one check's findings inline (selecting it again clears it).
// Only the selected check's findings unfold rows, so a noisy rule can't bury the
// file. A rendered Markdown/HTML file shows its source while a check with findings
// is selected (ShowRenderedMarkdown/HTML read checkShowsSource), never touching
// the durable Preview/Raw preference.
func (c *PrereviewController) SelectCheck(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	id := ctx.GetString("id")
	if state.SelectedCheck == id {
		id = ""
	}
	state.SelectedCheck = id
	return state, nil
}

// TrustVale lets this repo's Vale config run, for this user (ui-prefs.json,
// keyed by the repo's absolute path). It's the only way vale ever runs.
func (c *PrereviewController) TrustVale(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	if c.RepoPath == "" || slices.Contains(state.TrustedVale, c.RepoPath) {
		return state, nil
	}
	state.TrustedVale = append(slices.Clone(state.TrustedVale), c.RepoPath)
	c.savePrefs(state)
	c.applyChecks(&state)
	return state, nil
}

// ToggleCheckEnabled disables or re-enables a check for this user, in every repo
// (ui-prefs.json). A disabled check leaves the count and draws nothing.
func (c *PrereviewController) ToggleCheckEnabled(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	id := ctx.GetString("id")
	if id == "" {
		return state, fmt.Errorf("toggle check: missing id")
	}
	if i := slices.Index(state.DisabledChecks, id); i >= 0 {
		state.DisabledChecks = slices.Delete(slices.Clone(state.DisabledChecks), i, i+1)
	} else {
		state.DisabledChecks = append(slices.Clone(state.DisabledChecks), id)
		slices.Sort(state.DisabledChecks)
		if state.SelectedCheck == id {
			state.SelectedCheck = ""
		}
	}
	c.savePrefs(state)
	return state, nil
}

// QueueFinding turns a finding into an ordinary queued text comment anchored to
// its span, so the agent fixes it from the queue it already works. It builds the
// comment directly rather than through the composer, so a half-typed draft or an
// open edit survives.
func (c *PrereviewController) QueueFinding(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	res, f, ok := state.finding(ctx.GetString("id"), ctx.GetInt("index"))
	if !ok {
		return state, fmt.Errorf("queue finding: %s #%d not found", ctx.GetString("id"), ctx.GetInt("index"))
	}
	anchor := captureAnchor(state.CurrentDiff, f.Line, f.EndLine, "new")
	anchor.Snippet = findingSnippet(state.CurrentDiff, f)
	if anchor.Snippet == "" {
		anchor.Snippet = f.Message // a zero-width span still needs a snippet
	}
	cm := newTextComment(state.SelectedFile, f.Line, f.EndLine, f.FromCol, f.ToCol, "new",
		fmt.Sprintf("%s (check: %s)", f.Message, res.Name), anchor)
	state.Comments = append(state.Comments, cm)
	if err := c.persist(state.Comments); err != nil {
		state.Comments = state.Comments[:len(state.Comments)-1]
		return state, fmt.Errorf("persist finding comment: %w", err)
	}
	noteEnqueue(&state)
	state.LastSaved = time.Now().Format("15:04:05")
	state.Files = annotateCommentCounts(state.Files, state.Comments)
	return state, nil
}

// findingSnippet is the text a finding covers, joined across lines.
func findingSnippet(d *gitdiff.FileDiff, f checks.Finding) string {
	if d == nil {
		return ""
	}
	lines := sideContent(d, "new")
	if f.Line < 1 || f.EndLine > len(lines) {
		return ""
	}
	var parts []string
	for ln := f.Line; ln <= f.EndLine; ln++ {
		rs := []rune(lines[ln-1])
		from, to, _ := spanOnLine(ln, f.Line, f.FromCol, f.EndLine, f.ToCol)
		from, to = min(from, len(rs)), min(to, len(rs))
		parts = append(parts, string(rs[from:max(from, to)]))
	}
	return strings.Join(parts, "\n")
}

// CopyCheckToUserFolder copies a built-in rule into the user's checks folder so it
// can be edited (the copy replaces the built-in by ID). The watcher picks it up.
func (c *PrereviewController) CopyCheckToUserFolder(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	id := ctx.GetString("id")
	data, ok := checks.Builtin(id)
	if !ok || c.ChecksDir == "" {
		return state, fmt.Errorf("copy check: %q is not a built-in", id)
	}
	dst := filepath.Join(c.ChecksDir, id+".yml")
	if _, err := os.Stat(dst); err == nil {
		return state, nil // already copied: the row already points at it
	}
	if err := os.MkdirAll(c.ChecksDir, 0o755); err != nil {
		return state, fmt.Errorf("copy check: %w", err)
	}
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		return state, fmt.Errorf("copy check: %w", err)
	}
	if c.reloadChecksIfChanged() {
		c.applyChecks(&state)
		go c.broadcast("ChecksUpdated") // other tabs; the watcher won't see it again
	}
	return state, nil
}

// SetEditor saves the editor used by "Open in editor" links.
func (c *PrereviewController) SetEditor(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	ed := ctx.GetString("editor")
	if _, _, ok := editorScheme(ed); !ok && ed != EditorCustom && ed != "" {
		return state, fmt.Errorf("unknown editor %q", ed)
	}
	state.Editor = ed
	if ctx.Has("template") {
		state.EditorTemplate = strings.TrimSpace(ctx.GetString("template"))
	}
	if ctx.Has("ssh_host") {
		state.EditorSSHHost = strings.TrimSpace(ctx.GetString("ssh_host"))
	}
	c.savePrefs(state)
	return state, nil
}

// JumpToFinding moves the line cursor (and scroll) to a finding of the selected
// check — the panel's way to the inline card. The row is unfolded because the
// selected check's lines stay out of folds.
func (c *PrereviewController) JumpToFinding(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	id := ctx.GetString("id")
	_, f, ok := state.finding(id, ctx.GetInt("index"))
	if !ok || state.CurrentDiff == nil {
		return state, fmt.Errorf("jump to finding: %s #%d not found", id, ctx.GetInt("index"))
	}
	state.SelectedCheck = id
	for _, l := range state.CurrentDiff.Lines {
		if l.Kind != "del" && l.NewNum == f.EndLine {
			state.CursorKey = lineCursorKey(l)
			break
		}
	}
	return state, nil
}
