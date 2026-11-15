package review

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/livetemplate/livetemplate"
	"github.com/livetemplate/prereview/gitdiff"
	"github.com/livetemplate/prereview/internal/checks"
	"github.com/livetemplate/prereview/internal/library"
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

// ReviewChecks is the Checks page's data: every check target's results
// (applyReviewChecks). The Vale notice is per repo, so it lives here once.
type ReviewChecks struct {
	Files      []PathChecks `json:"files"`
	Total      int          `json:"total"` // targets before the maxCheckFiles cap
	ValeNotice string       `json:"vale_notice,omitempty"`
	ValeTrust  bool         `json:"vale_trust,omitempty"`
}

// PathChecks is one file's results on the Checks page.
type PathChecks struct {
	Path    string          `json:"path"`
	Results []checks.Result `json:"results"`
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

// maxCheckFiles caps how many files the Checks page runs checks on; a review of
// a large folder shows the first ones and says how many it skipped.
const maxCheckFiles = 300

// maxCheckBytes skips files too large to be prose worth checking.
const maxCheckBytes = 1 << 20

// checkCacheSize bounds the result cache (entries are per file × content × library);
// it holds a whole capped review a few times over, so a review-wide pass hits.
const checkCacheSize = 4 * maxCheckFiles

// checkWorkers bounds the command / vale processes running at once.
const checkWorkers = 4

// checksBroadcastDelay coalesces async results landing close together into one
// ChecksUpdated, so a review-wide run doesn't re-render once per file.
const checksBroadcastDelay = 150 * time.Millisecond

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
	// diskKeys memoises a file's content key by (mtime, size), so a review-wide
	// pass stats files instead of re-reading and re-hashing them.
	diskKeys map[string]diskKey
	sem      chan struct{} // checkWorkers slots for async runs
	pending  atomic.Bool   // a coalesced ChecksUpdated broadcast is scheduled
}

type diskKey struct {
	mtime  time.Time
	size   int64
	key    string // checkKey, ending with the library hash it was computed under
	binary bool   // the file isn't text: skipped until it changes
}

type checkEntry struct {
	results []checks.Result
}

func (c *PrereviewController) runner() *checkRunner {
	c.checksOnce.Do(func() {
		r := &checkRunner{cache: map[string]*checkEntry{}, keys: map[*gitdiff.FileDiff]string{},
			inflight: map[string]context.CancelFunc{}, diskKeys: map[string]diskKey{},
			sem: make(chan struct{}, checkWorkers)}
		r.lib = checks.Load(checks.UserDirs(c.ChecksDir, c.learnedDir())...)
		r.stamp = checks.StampDirs(checks.UserDirs(c.ChecksDir, c.learnedDir()))
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
	stamp := checks.StampDirs(checks.UserDirs(c.ChecksDir, c.learnedDir()))
	cfg, bin := c.valeState()
	r.mu.Lock()
	defer r.mu.Unlock()
	if stamp == r.stamp && cfg == r.valeCfg && bin == r.valeBin {
		return false
	}
	if stamp != r.stamp {
		r.stamp = stamp
		r.lib = checks.Load(checks.UserDirs(c.ChecksDir, c.learnedDir())...)
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
	c.calibrationChanged()                          // the baseline: only later answers fan out
	c.checkpointLibrary(VersionTriggerFileEdit, "") // edits made while no server ran
	c.syncSkills("")                                   // bring generated skills in line after an upgrade
	c.learningsChanged()                        // the baseline: only later writes fan out
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			if c.reloadChecksIfChanged() {
				c.checkpointLibrary(VersionTriggerFileEdit, "")
				c.broadcast("ChecksUpdated")
			}
			if c.calibrationChanged() { // an answer in any review re-counts every tab
				c.broadcast("ChecksUpdated")
			}
			if c.learningsChanged() { // any server's write to the shared library
				c.syncSkills("") // idempotent: a no-op when the writer already synced
				c.broadcast("LibraryUpdated")
			}
		}
	}
}

// ChecksUpdated re-applies checks after a rule edit or an async result. Dispatched
// by the watcher / runner to every tab; each re-reads against its own view.
func (c *PrereviewController) ChecksUpdated(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	c.reapplyChecks(&state)
	return state, nil
}

// reapplyChecks recomputes both views of the results after the library or the
// Vale setup changed: the open file's and (while it's open) the Checks page's.
func (c *PrereviewController) reapplyChecks(state *PrereviewState) {
	c.applyChecks(state)
	c.applyReviewChecks(state)
}

// broadcastChecksSoon schedules one ChecksUpdated for every tab, folding the
// results that land within checksBroadcastDelay into it.
func (c *PrereviewController) broadcastChecksSoon() {
	r := c.runner()
	if !r.pending.CompareAndSwap(false, true) {
		return
	}
	time.AfterFunc(checksBroadcastDelay, func() {
		r.pending.Store(false)
		c.broadcast("ChecksUpdated")
	})
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

// checkSetup is the library and Vale setup one pass checks against: a snapshot,
// so a reload mid-pass can't mix two libraries.
type checkSetup struct {
	lib     *checks.Library
	mode    string // valeOff | valeUntrusted | valeNoBinary | valeRun
	valeBin string
}

func (c *PrereviewController) checkSetup(trusted []string) checkSetup {
	r := c.runner()
	r.mu.Lock()
	lib, cfg, bin := r.lib, r.valeCfg, r.valeBin
	r.mu.Unlock()
	mode := valeOff
	switch {
	case !cfg:
	case !slices.Contains(trusted, c.RepoPath):
		mode = valeUntrusted
	case bin == "":
		mode = valeNoBinary
	default:
		mode = valeRun
	}
	return checkSetup{lib: lib, mode: mode, valeBin: bin}
}

// rulesFor is the library rules whose files globs match path.
func (cs checkSetup) rulesFor(path string) (checkers, judges []*checks.Rule) {
	for _, rule := range cs.lib.Rules {
		switch {
		case !rule.Applies(path):
		case rule.IsJudge():
			judges = append(judges, rule)
		default:
			checkers = append(checkers, rule)
		}
	}
	return checkers, judges
}

// judgeResults appends each judge's latest verdict on path as a result.
// contentHash identifies the text the review shows; lines loads it only to
// locate a current verdict's quote.
func judgeResults(res []checks.Result, path string, judges []*checks.Rule, verdicts map[string]Judgment, contentHash string, lines func() []string) []checks.Result {
	for _, jr := range judges {
		j, ok := verdicts[judgmentKey(path, jr.ID)]
		res = append(res, judgeResult(jr, j, ok, contentHash, lines))
	}
	return res
}

// keyContentHash is a checkKey's content part — checks.ContentHash of the lines —
// so the page needn't re-read a file to tell whether a verdict is current.
func keyContentHash(key string) string {
	_, rest, _ := strings.Cut(key, "\x00")
	h, _, _ := strings.Cut(rest, "\x00")
	return h
}

// verdicts is the agent's judge verdicts, latest per (file, judge), re-read only
// when judgments.jsonl changes. Shared by every tab: read-only.
func (c *PrereviewController) verdicts() map[string]Judgment {
	if c.CSVPath == "" {
		return nil
	}
	p := JudgmentsPath(c.CSVPath)
	fp := statusFingerprint(p)
	c.judgeMu.Lock()
	defer c.judgeMu.Unlock()
	if c.judgeCache == nil || fp != c.judgeFP {
		c.judgeCache, c.judgeFP = loadJudgments(p), fp
	}
	return c.judgeCache
}

// judgeLabels is the reviewer's Agree/Disagree answers, re-read only when
// judge-labels.jsonl changes. Shared by every tab: read-only.
func (c *PrereviewController) judgeLabels() map[string]string {
	if c.CSVPath == "" {
		return nil
	}
	p := JudgeLabelsPath(c.CSVPath)
	fp := statusFingerprint(p)
	c.judgeMu.Lock()
	defer c.judgeMu.Unlock()
	if c.labelCache == nil || fp != c.labelFP {
		c.labelCache, c.labelFP = loadJudgeLabels(p), fp
	}
	return c.labelCache
}

// results returns path's results for one content (key = checkKey) from the
// cache, starting the runs on a miss. lines loads the text only when needed.
func (c *PrereviewController) results(cs checkSetup, path, key string, rules []*checks.Rule, lines func() []string) ([]checks.Result, bool) {
	key += "\x00vale=" + cs.mode // trusting a repo's Vale config is a different result
	r := c.runner()
	r.mu.Lock()
	e := r.cache[key]
	r.mu.Unlock()
	if e == nil {
		text := lines()
		if text == nil {
			return nil, false
		}
		e = c.startChecks(key, path, text, rules, cs)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(e.results), true
}

// valeNotice explains a Vale config that isn't being applied.
func valeNotice(mode string) string {
	switch mode {
	case valeUntrusted:
		return "This repo has a Vale config. Running it can start tools the config names, so it stays off until you trust this repo."
	case valeNoBinary:
		return "This repo has a Vale config, but vale isn't installed, so its rules aren't applied."
	}
	return ""
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
	cs := c.checkSetup(state.TrustedVale)
	rules, judges := cs.rulesFor(d.Path)
	if len(rules) == 0 && len(judges) == 0 && cs.mode == valeOff {
		return
	}
	if len(judges) > 0 {
		state.JudgeLabels = c.judgeLabels()
		state.JudgeCal = c.calibration()
	}
	lines := sideContent(d, "new")
	if len(lines) == 0 {
		return // a deleted file: nothing to check
	}
	r := c.runner()
	r.mu.Lock()
	key, keyed := r.keys[d]
	r.mu.Unlock()
	if !keyed || !strings.HasSuffix(key, cs.lib.Hash) {
		key = checkKey(d.Path, lines, cs.lib.Hash)
		r.mu.Lock()
		if len(r.keys) >= checkCacheSize {
			clear(r.keys)
		}
		r.keys[d] = key
		r.mu.Unlock()
	}
	res, _ := c.results(cs, d.Path, key, rules, func() []string { return lines })
	if len(judges) > 0 {
		res = judgeResults(res, d.Path, judges, c.verdicts(), keyContentHash(key), func() []string { return lines })
	}
	state.Checks = &FileChecks{Results: res, ValeNotice: valeNotice(cs.mode), ValeTrust: cs.mode == valeUntrusted}
}

// applyReviewChecks attaches every check target's results for the Checks page,
// only while the page is open (it's the only reader, and a large review would
// otherwise pay for it on every Mount and broadcast): the changed files (all
// files when none changed, scopeFiles), text only, capped at maxCheckFiles. Files
// are read from disk; a pass over unchanged files costs a stat each (diskKeys).
// The open file is skipped: the page takes its displayed text (pageFiles), so the
// page and the inline marks agree, and its two runs can't cancel each other.
func (c *PrereviewController) applyReviewChecks(state *PrereviewState) {
	state.JudgeCal = c.calibration() // the page shows every judge's standing
	defer c.applyRuleHistory(state) // the expanded rule's versions, on every path out
	state.ReviewChecks = nil
	if !state.ShowLibrary || !state.LibraryOnChecks() || c.ExternalMode || c.RepoPath == "" {
		return
	}
	cs := c.checkSetup(state.TrustedVale)
	targets := checkTargets(state.Files)
	verdicts := c.verdicts()
	rc := &ReviewChecks{Total: len(targets), ValeNotice: valeNotice(cs.mode), ValeTrust: cs.mode == valeUntrusted}
	if len(targets) > maxCheckFiles {
		targets = targets[:maxCheckFiles]
	}
	for _, f := range targets {
		if state.Checks != nil && f.Path == state.SelectedFile {
			continue
		}
		rules, judges := cs.rulesFor(f.Path)
		if len(rules) == 0 && len(judges) == 0 && cs.mode == valeOff {
			continue
		}
		key, lines, ok := c.diskKey(f.Path, cs.lib.Hash)
		if !ok {
			continue
		}
		load := func() []string {
			if lines == nil {
				lines, _ = checks.ReadLines(filepath.Join(c.RepoPath, f.Path))
			}
			return lines
		}
		if res, ok := c.results(cs, f.Path, key, rules, load); ok {
			res = judgeResults(res, f.Path, judges, verdicts, keyContentHash(key), load)
			rc.Files = append(rc.Files, PathChecks{Path: f.Path, Results: res})
		}
	}
	state.ReviewChecks = rc
}

// checkTargets is the files the Checks page covers: the review's changed files
// (scopeFiles, ignoring the drawer's show-all toggle), minus the binaries git
// already flagged. An unflagged binary is caught by checks.ReadLines.
func checkTargets(files []gitdiff.FileEntry) []gitdiff.FileEntry {
	var out []gitdiff.FileEntry
	for _, f := range scopeFiles(files, false) {
		if f.Added >= 0 {
			out = append(out, f)
		}
	}
	return out
}

// diskKey returns a file's content key, from the (mtime, size) memo when the
// file hasn't changed, else by reading it (and then returns the lines too).
// ok is false for a missing, empty, oversized or binary file (a binary verdict is
// memoised too, so it isn't re-read every pass).
func (c *PrereviewController) diskKey(path, libHash string) (key string, lines []string, ok bool) {
	abs := filepath.Join(c.RepoPath, path)
	fi, err := os.Stat(abs)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() == 0 || fi.Size() > maxCheckBytes {
		return "", nil, false
	}
	r := c.runner()
	r.mu.Lock()
	dk, hit := r.diskKeys[path]
	r.mu.Unlock()
	unchanged := hit && dk.mtime.Equal(fi.ModTime()) && dk.size == fi.Size()
	if unchanged && dk.binary {
		return "", nil, false
	}
	if unchanged && strings.HasSuffix(dk.key, libHash) {
		return dk.key, nil, true
	}
	lines, err = checks.ReadLines(abs)
	binary := errors.Is(err, checks.ErrBinary)
	if err != nil && !binary {
		return "", nil, false
	}
	if !binary {
		key = checkKey(path, lines, libHash)
	}
	r.mu.Lock()
	if len(r.diskKeys) >= checkCacheSize {
		clear(r.diskKeys)
	}
	r.diskKeys[path] = diskKey{mtime: fi.ModTime(), size: fi.Size(), key: key, binary: binary}
	r.mu.Unlock()
	return key, lines, !binary
}

// startChecks computes the inline results, records running placeholders for the
// async ones, caches the entry, and starts the background runs — cancelling the
// path's previous run, whose content is now stale.
func (c *PrereviewController) startChecks(key, path string, lines []string, rules []*checks.Rule, cs checkSetup) *checkEntry {
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
	runVale := cs.mode == valeRun
	if runVale {
		placeholder := checks.ValeSummary(c.RepoPath)
		placeholder.Status = checks.StatusRunning
		e.results = append(e.results, placeholder)
	}

	r := c.runner()
	async := len(jobs) > 0 || runVale
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
			for _, j := range jobs {
				wg.Add(1)
				go func() {
					defer wg.Done()
					if !r.acquire(ctx) {
						return
					}
					defer r.release()
					// Built once a slot is free, so queued runs don't each hold a copy.
					content := []byte(strings.Join(lines, "\n") + "\n")
					res := checks.RunCommand(ctx, j.rule, abs, content)
					r.mu.Lock()
					e.results[j.idx] = res
					r.mu.Unlock()
				}()
			}
		}
		if runVale {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if !r.acquire(ctx) {
					return
				}
				defer r.release()
				vres := checks.RunVale(ctx, cs.valeBin, c.RepoPath, path)
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
			c.broadcastChecksSoon()
		}
	}()
	return e
}

// acquire takes a worker slot, or gives up when the run is superseded first.
func (r *checkRunner) acquire(ctx context.Context) bool {
	select {
	case r.sem <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

func (r *checkRunner) release() { <-r.sem }

// checkKey identifies one file's content under one library; it ends with the
// library hash so a memoised key can be checked against a reloaded library.
func checkKey(path string, lines []string, libHash string) string {
	return path + "\x00" + checks.ContentHash(lines) + "\x00" + libHash
}

// ToggleLibrary opens or closes the Library (the toolbar button), on the Checks
// tab across every file.
func (c *PrereviewController) ToggleLibrary(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	toggleFullView(&state, &state.ShowLibrary)
	state.MoreMenuOpen = false
	if state.ShowLibrary {
		state.ChecksFile = ""
	}
	c.applyLibrary(&state)
	c.applyReviewChecks(&state) // runs while open on the Checks tab; clears otherwise
	return state, nil
}

// OpenChecks opens the Checks page filtered to the open file: the file header's
// Checks button and the bar's "All checks".
func (c *PrereviewController) OpenChecks(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	closeFullViews(&state)
	state.ShowLibrary, state.LibraryTab = true, libTabChecks
	state.ChecksFile = state.SelectedFile
	c.applyLibrary(&state)
	c.applyReviewChecks(&state)
	return state, nil
}

// SetChecksQuery updates the Checks page search (live-debounced, like the file
// filter).
func (c *PrereviewController) SetChecksQuery(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	state.ChecksQuery = ctx.GetString("q")
	return state, nil
}

// SetChecksFilter sets one Checks page filter from a chip: field is status,
// category or file; an empty value clears it.
func (c *PrereviewController) SetChecksFilter(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	v := ctx.GetString("value")
	switch field := ctx.GetString("field"); field {
	case "status":
		if !slices.Contains([]string{"", checks.StatusFail, checks.StatusError, checks.StatusPass, checkStatusOff}, v) {
			return state, fmt.Errorf("checks filter: unknown status %q", v)
		}
		state.ChecksStatus = v
	case "category":
		state.ChecksCategory = v
	case "file":
		state.ChecksFile = v
	default:
		return state, fmt.Errorf("checks filter: unknown field %q", field)
	}
	return state, nil
}

// SetChecksSource sets the Checks page's source picker. Its own action: a
// select's lvt-on:change sends only the select's value, not its form's other
// fields.
func (c *PrereviewController) SetChecksSource(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	v := ctx.GetString("source")
	if !slices.Contains([]string{"", checks.SourceBuiltin, checks.SourceUser, checks.SourceLearned, checks.SourceVale}, v) {
		return state, fmt.Errorf("checks source: unknown %q", v)
	}
	state.ChecksSource = v
	return state, nil
}

// ExpandCheck opens one check's row on the Checks page (again closes it). One at
// a time: the open row carries the findings and the rule's actions.
func (c *PrereviewController) ExpandCheck(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	id := ctx.GetString("id")
	if state.ExpandedCheck == id {
		id = ""
	}
	state.ExpandedCheck = id
	c.applyRuleHistory(&state)
	return state, nil
}

// TogglePassingGroups shows or folds the categories where every check passes.
func (c *PrereviewController) TogglePassingGroups(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	state.ShowPassingGroups = !state.ShowPassingGroups
	return state, nil
}

// SelectCheck shows one check's findings inline (selecting it again clears it) and
// moves to its first finding. Only the selected check's findings unfold rows, so a
// noisy rule can't bury the file. A rendered Markdown/HTML file shows its source
// while a check with findings is selected (ShowRenderedMarkdown/HTML read
// checkShowsSource), never touching the durable Preview/Raw preference.
func (c *PrereviewController) SelectCheck(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	id := ctx.GetString("id")
	if state.SelectedCheck == id {
		id = ""
	}
	state.SelectedCheck = id
	state.FindingCursor = 0
	if res, ok := state.selectedFindings(); ok {
		moveCursorTo(&state, res.Findings[0])
	}
	return state, nil
}

// StepFinding moves to the selected check's next (dir=1) or previous (dir=-1)
// finding in this file, wrapping around: the bar's ‹ ›. It never changes file.
func (c *PrereviewController) StepFinding(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	res, ok := state.selectedFindings()
	if !ok {
		return state, nil
	}
	n := len(res.Findings)
	step := 1
	if ctx.GetInt("dir") < 0 {
		step = -1
	}
	state.FindingCursor = ((state.FindingCursor+step)%n + n) % n
	moveCursorTo(&state, res.Findings[state.FindingCursor])
	return state, nil
}

// moveCursorTo parks the line cursor (and scroll) on a finding's last line. The
// row is unfolded because the selected check's lines stay out of folds.
func moveCursorTo(state *PrereviewState, f checks.Finding) {
	if l, ok := findDiffLine(state.CurrentDiff, "new", f.EndLine); ok {
		state.CursorKey = lineCursorKey(l)
	}
}

// TrustVale lets this repo's Vale config run, for this user (ui-prefs.json,
// keyed by the repo's absolute path). It's the only way vale ever runs.
func (c *PrereviewController) TrustVale(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	if c.RepoPath == "" || slices.Contains(state.TrustedVale, c.RepoPath) {
		return state, nil
	}
	state.TrustedVale = append(slices.Clone(state.TrustedVale), c.RepoPath)
	c.savePrefs(state)
	c.reapplyChecks(&state)
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
// open edit survives. path names the finding's file (the Checks page queues
// findings in files that aren't open); "" means the open file.
func (c *PrereviewController) QueueFinding(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	return c.queueFinding(state, ctx.GetString("path"), ctx.GetString("id"), ctx.GetInt("index"))
}

// queueFinding queues finding idx of check id in path ("" = the open file) as a
// text comment on its span, labelled by kind ("(check: …)" / "(judge: …)").
func (c *PrereviewController) queueFinding(state PrereviewState, path, id string, idx int) (PrereviewState, error) {
	if path == "" {
		path = state.SelectedFile
	}
	res, f, ok := state.findingIn(path, id, idx)
	if !ok {
		return state, fmt.Errorf("queue finding: %s %s #%d not found", path, id, idx)
	}
	d := state.CurrentDiff
	if path != state.SelectedFile || d == nil || state.ViewingVersion { // the anchor needs the live text
		var err error
		if d, err = c.loadDiffCached(state.Base, path); err != nil {
			return state, fmt.Errorf("queue finding: load %s: %w", path, err)
		}
	}
	anchor := captureAnchor(d, f.Line, f.EndLine, "new")
	anchor.Snippet = findingSnippet(d, f)
	if anchor.Snippet == "" {
		anchor.Snippet = f.Message // a zero-width span still needs a snippet
	}
	kind := "check"
	if res.Type == checks.TypeJudge {
		kind = "judge"
	}
	return c.appendQueued(state, newTextComment(path, f.Line, f.EndLine, f.FromCol, f.ToCol, "new",
		fmt.Sprintf("%s (%s: %s)", f.Message, kind, res.Name), anchor))
}

// appendQueued adds a comment the server built (a finding, a request to the
// agent) to the queue and persists it, leaving the composer — and any half-typed
// draft or comment being edited — exactly as it was.
func (c *PrereviewController) appendQueued(state PrereviewState, cm Comment) (PrereviewState, error) {
	state.Comments = append(state.Comments, cm)
	if err := c.persist(state.Comments); err != nil {
		state.Comments = state.Comments[:len(state.Comments)-1]
		return state, fmt.Errorf("persist queued comment: %w", err)
	}
	noteEnqueue(&state)
	state.LastSaved = time.Now().Format("15:04:05")
	state.Files = annotateCommentCounts(state.Files, state.Comments)
	return state, nil
}

// queueRequest queues body as a file-level request to the agent on the open file
// (an extraction), without touching the composer.
func (c *PrereviewController) queueRequest(state PrereviewState, body string) (PrereviewState, error) {
	return c.appendQueued(state, Comment{ID: newCommentID(), File: state.SelectedFile, Body: body,
		Created: time.Now().UTC(), Kind: commentKindFile})
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
		c.reapplyChecks(&state)
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

// JumpToFinding shows a finding in its file: the Checks page's "Show in file" and
// each finding's location link. It opens the file (path; "" = the open one),
// selects the check so its findings draw inline, closes the Library and moves the
// cursor to the finding.
func (c *PrereviewController) JumpToFinding(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	path, id, idx := ctx.GetString("path"), ctx.GetString("id"), ctx.GetInt("index")
	if path != "" && path != state.SelectedFile {
		var err error
		if state, err = c.selectFileForJump(state, path); err != nil {
			return state, err
		}
	}
	closeFullViews(&state)
	_, f, ok := state.finding(id, idx)
	if !ok {
		return state, fmt.Errorf("jump to finding: %s #%d not found in %s", id, idx, state.SelectedFile)
	}
	state.SelectedCheck = id
	state.FindingCursor = idx
	moveCursorTo(&state, f)
	return state, nil
}

// SetChecksKind sets the Checks page's kind chip: "" (checkers and judges),
// "checker" or "judge".
func (c *PrereviewController) SetChecksKind(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	v := ctx.GetString("kind")
	if v != "" && v != "checker" && v != checks.TypeJudge {
		return state, fmt.Errorf("checks kind: unknown %q", v)
	}
	state.ChecksKind = v
	return state, nil
}

// RunJudges asks the agent to judge the open file: ONE file-level comment whose
// body lists every enabled judge's rubric and the exact `prereview judge`
// command per judge (the RequestQuiz pattern — the agent picks it up from the
// queue it already works). prereview never calls an LLM itself.
func (c *PrereviewController) RunJudges(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	if !state.AgentMode {
		return state, fmt.Errorf("run judges: no agent attached (start one with prereview --agent)")
	}
	d := state.CurrentDiff
	if d == nil || state.SelectedFile == "" {
		return state, fmt.Errorf("run judges: no file selected")
	}
	cs := c.checkSetup(state.TrustedVale)
	_, judges := cs.rulesFor(d.Path)
	var run []*checks.Rule
	for _, j := range judges {
		if j.Err == "" && !state.checkDisabled(j.ID) {
			run = append(run, j)
		}
	}
	if len(run) == 0 {
		return state, nil
	}
	prompt := checks.JudgePrompt(state.SelectedFile, filepath.Dir(c.CSVPath), checks.ContentHash(sideContent(d, "new")), run)
	state, err := c.queueFileComment(state, prompt)
	if err != nil {
		return state, err
	}
	state.Flash = fmt.Sprintf("Asked your agent to run %d judge%s", len(run), plural(len(run)))
	return state, nil
}

// LabelJudgment records your Agree / Disagree on a judge's current verdict for
// a file (path; "" = the open file) — the judge's calibration label. Choosing
// the same answer again takes it back.
func (c *PrereviewController) LabelJudgment(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	path, id, label := ctx.GetString("path"), ctx.GetString("id"), ctx.GetString("label")
	if path == "" {
		path = state.SelectedFile
	}
	if label != LabelAgree && label != LabelDisagree {
		return state, fmt.Errorf("label judgment: unknown label %q", label)
	}
	j, ok := c.verdicts()[judgmentKey(path, id)]
	if !ok {
		return state, fmt.Errorf("label judgment: no verdict from %s on %s", id, path)
	}
	at := j.At
	if state.JudgeLabels[labelKey(path, id, at)] == label {
		label = "" // the same answer again takes it back
	}
	if err := appendJSONL(JudgeLabelsPath(c.CSVPath), JudgeLabel{File: path, Judge: id, At: at, Label: label,
		When: time.Now().UTC().Format(time.RFC3339)}); err != nil {
		return state, fmt.Errorf("label judgment: %w", err)
	}
	if c.LibraryDir != "" { // the calibration label: this answer, the verdict it answers, its rubric version
		if err := library.RecordLabel(c.LibraryDir, library.Label{Judge: id, RubricHash: j.RubricHash, ContentHash: j.ContentHash,
			Verdict: j.Verdict, Critique: j.Critique, Label: label, Store: filepath.Dir(c.CSVPath), File: path, At: at}); err != nil {
			return state, fmt.Errorf("label judgment: %w", err)
		}
		c.calibrationChanged() // this server's tabs are re-counted below, not again on the tick
	}
	state.JudgeLabels = c.judgeLabels()
	state.JudgeCal = c.calibration()
	go c.broadcast("ChecksUpdated") // other tabs show the answer too
	return state, nil
}

// QueueJudgment queues a judge's critique on the open file as an ordinary
// comment, so the agent can act on it: a text comment on the quoted passage when
// the quote was found, else a file-level comment.
func (c *PrereviewController) QueueJudgment(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	id := ctx.GetString("id")
	r, ok := state.checkResult(id)
	if !ok || r.Type != checks.TypeJudge || r.Critique == "" {
		return state, fmt.Errorf("queue judgment: no verdict from %s", id)
	}
	if len(r.Findings) > 0 {
		return c.queueFinding(state, "", id, 0)
	}
	return c.queueFileComment(state, fmt.Sprintf("%s (judge: %s)", r.Critique, r.Name))
}
