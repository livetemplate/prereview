package review

import (
	"fmt"
	"path/filepath"
	"slices"

	"github.com/livetemplate/livetemplate"

	"github.com/livetemplate/prereview/csv"
	"github.com/livetemplate/prereview/internal/library"
	"github.com/livetemplate/prereview/internal/skills"
)

// Library tabs. "" is Learnings, the first tab.
const (
	libTabLearnings = "learnings"
	libTabChecks    = "checks"
)

// learnings is the folded learnings log, re-read only when the file changes
// (its size/mtime fingerprint). Shared read-only by every tab; another server's
// append shows up on the next watcher tick.
func (c *PrereviewController) learnings() []library.Learning {
	if c.LibraryDir == "" {
		return nil
	}
	fp := statusFingerprint(filepath.Join(c.LibraryDir, library.LearningsFile))
	c.learnMu.Lock()
	defer c.learnMu.Unlock()
	if fp != c.learnFP || c.learnCache == nil {
		c.learnCache, c.learnFP = library.LoadLearnings(c.LibraryDir), fp
	}
	return c.learnCache
}

// applyLibrary refreshes what the Library shows: the toolbar badge (always) and,
// while the Learnings tab is open, the learnings themselves (a closed Library
// carries none, so every action's state clone stays small).
func (c *PrereviewController) applyLibrary(state *PrereviewState) {
	ls := c.learnings()
	state.LearnNew = 0
	for _, l := range ls {
		if l.Status == library.StatusProposed {
			state.LearnNew++
		}
	}
	state.Learnings = nil
	if state.ShowLibrary && state.libraryTab() == libTabLearnings {
		state.Learnings = ls
		state.LearnStore = filepath.Dir(c.CSVPath)
	}
	c.applySkillsTab(state)
	c.applyBenchTab(state)
	state.Bootstrapped = c.LibraryDir == "" || library.Bootstrapped(c.LibraryDir)
	state.BootStores, state.BootScanned = nil, false
	if !state.Bootstrapped || state.BootOpen {
		c.bootMu.Lock()
		state.BootStores, state.BootScanned = c.bootStores, c.bootScanned
		c.bootMu.Unlock()
	}
}

// libraryFingerprint is what the watcher compares: the learnings log, the
// first-launch marker (answering the offer in one review hides it in the
// others) and the skills' inputs (so another server's Create skill or Import,
// or your edit to a skill, syncs here too).
func (c *PrereviewController) libraryFingerprint() string {
	fp := statusFingerprint(filepath.Join(c.LibraryDir, library.LearningsFile)) + "|" +
		statusFingerprint(filepath.Join(c.LibraryDir, library.BootstrapFile))
	// Every other input of a skills sync: created skills, imports, and the skill
	// sources you might edit in your editor.
	inputs, _ := filepath.Glob(filepath.Join(c.LibraryDir, "skills", "*.json"))
	srcs, _ := filepath.Glob(skills.Env{LibDir: c.LibraryDir}.SourcePath("*"))
	for _, p := range append(inputs, srcs...) {
		if filepath.Base(p) != skills.OwnedFile { // written by every sync: not an input
			fp += "|" + p + statusFingerprint(p)
		}
	}
	return fp
}

// ScanPastReviews looks for review stores on this machine (the first-launch
// offer) and tells every tab when it's done. Read-only; run in the background —
// a walk of a real home takes a while.
func (c *PrereviewController) ScanPastReviews() {
	if !c.bootScanning.CompareAndSwap(false, true) {
		return // one walk at a time; its result reaches every tab
	}
	defer c.bootScanning.Store(false)
	stores := library.PastReviews(c.LibraryDir, library.ScanRoot(), library.ScanMaxDepth)
	c.bootMu.Lock()
	c.bootStores, c.bootScanned = stores, true
	c.bootMu.Unlock()
	c.broadcast("LibraryUpdated")
}

// OpenPastReviews reopens the offer from the Learnings tab, rescanning.
func (c *PrereviewController) OpenPastReviews(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	state.BootOpen = !state.BootOpen
	if state.BootOpen {
		c.bootMu.Lock()
		c.bootScanned = false
		c.bootMu.Unlock()
		go c.ScanPastReviews()
	}
	c.applyLibrary(&state)
	return state, nil
}

// ToggleBootStore ticks or unticks one past review in the offer. A server-side
// set, not checkbox values: a lone checkbox posts a boolean, not its value.
func (c *PrereviewController) ToggleBootStore(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	store := ctx.GetString("store")
	if i := slices.Index(state.BootSkip, store); i >= 0 {
		state.BootSkip = slices.Delete(slices.Clone(state.BootSkip), i, i+1)
	} else {
		state.BootSkip = append(slices.Clone(state.BootSkip), store)
	}
	return state, nil
}

// ReadPastReviews queues one extraction request naming exactly the ticked
// stores, records that they were read (with their signal counts, for the
// auto-extraction gate) and answers the first-launch offer.
func (c *PrereviewController) ReadPastReviews(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	if !state.AgentMode {
		return state, fmt.Errorf("read past reviews: no agent attached (start one with prereview --agent)")
	}
	if state.SelectedFile == "" {
		return state, fmt.Errorf("read past reviews: open a file first (the request is queued on it)")
	}
	picked := state.pickedBootStores()
	if len(picked) == 0 {
		return state, fmt.Errorf("read past reviews: no review ticked")
	}
	ls := c.learnings()
	state, err := c.queueRequest(state, csv.RequestExtraction, library.ExtractionPrompt(picked, ls))
	if err != nil {
		return state, err
	}
	for _, s := range picked {
		if err := library.RecordExtraction(c.LibraryDir, s, library.ReviewerSignals(s)); err != nil {
			return state, err
		}
	}
	if err := library.MarkBootstrapped(c.LibraryDir, "read"); err != nil {
		return state, err
	}
	state.BootOpen = false
	state.Flash = fmt.Sprintf("Asked your agent to read %d past review%s", len(picked), plural(len(picked)))
	return c.libraryWritten(state, ""), nil
}

// DismissPastReviews answers the offer with "Not now" (on every review of this
// machine; the Learnings tab can reopen it).
func (c *PrereviewController) DismissPastReviews(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	if !state.Bootstrapped {
		if err := library.MarkBootstrapped(c.LibraryDir, "dismissed"); err != nil {
			return state, err
		}
	}
	state.BootOpen = false
	return c.libraryWritten(state, ""), nil
}

// learningsChanged reports whether the log moved since the watcher last looked
// (any server's write), so the watcher fans it out once.
func (c *PrereviewController) learningsChanged() bool {
	if c.LibraryDir == "" {
		return false
	}
	fp := c.libraryFingerprint()
	c.learnMu.Lock()
	defer c.learnMu.Unlock()
	if fp == c.learnWatchFP {
		return false
	}
	c.learnWatchFP = fp
	return true
}

// LibraryUpdated re-reads the learnings after a write (this server's or
// another's). Dispatched by the watcher to every tab.
func (c *PrereviewController) LibraryUpdated(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	c.applyLibrary(&state)
	return state, nil
}

// SetLibraryTab switches the Library's tab.
func (c *PrereviewController) SetLibraryTab(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	tab := ctx.GetString("tab")
	if !slices.Contains([]string{libTabLearnings, libTabSkills, libTabChecks, libTabBenchmarks}, tab) {
		return state, fmt.Errorf("library: unknown tab %q", tab)
	}
	state.LibraryTab = tab
	c.applyLibrary(&state)
	c.applyReviewChecks(&state)
	return state, nil
}

// SetLearnFilter sets the Learnings tab's status chip or scope picker.
func (c *PrereviewController) SetLearnFilter(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	if ctx.Has("scope") {
		state.LearnScope = ctx.GetString("scope")
		return state, nil
	}
	v := ctx.GetString("status")
	if !slices.ContainsFunc(learnStatuses, func(s learnStatus) bool { return s.status == v }) {
		return state, fmt.Errorf("learnings filter: unknown status %q", v)
	}
	state.LearnStatus = v
	return state, nil
}

// LearningAction is a card's Accept / Reject / Delete / Restore.
func (c *PrereviewController) LearningAction(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	if c.LibraryDir == "" {
		return state, fmt.Errorf("no library folder")
	}
	id, act := ctx.GetString("id"), ctx.GetString("act")
	if err := library.Act(c.LibraryDir, id, act); err != nil {
		return state, err
	}
	return c.libraryWritten(state, learningChangelog(c.learnings(), id, act)), nil
}

// learningChangelog names what a card action did, for the library version it
// causes: `Accepted "Cut filler words."`.
func learningChangelog(ls []library.Learning, id, act string) string {
	verb := map[string]string{library.ActAccept: "Accepted", library.ActReject: "Rejected",
		library.ActDelete: "Deleted", library.ActRestore: "Restored", "edit": "Edited"}[act]
	for _, l := range ls {
		if l.ID == id {
			return verb + " “" + clip(l.Text, 80) + "”"
		}
	}
	return verb + " a learning"
}

// EditLearning opens a card's editor (again closes it).
func (c *PrereviewController) EditLearning(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	id := ctx.GetString("id")
	if state.LearnEdit == id {
		id = ""
	}
	state.LearnEdit = id
	return state, nil
}

// SaveLearning saves a card's edited text and scope.
func (c *PrereviewController) SaveLearning(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	if c.LibraryDir == "" {
		return state, fmt.Errorf("no library folder")
	}
	if err := library.Edit(c.LibraryDir, ctx.GetString("id"), ctx.GetString("text"), ctx.GetString("scope")); err != nil {
		return state, err
	}
	state.LearnEdit = ""
	return c.libraryWritten(state, learningChangelog(c.learnings(), ctx.GetString("id"), "edit")), nil
}

// libraryWritten refreshes this tab after its own write and fans it out to the
// others now (the watcher would, a tick later; other servers learn it that way).
func (c *PrereviewController) libraryWritten(state PrereviewState, changelog string) PrereviewState {
	c.syncSkills(changelog)
	c.applyLibrary(&state)
	c.learningsChanged() // this server's tabs are told now, not again on the tick
	go c.broadcast("LibraryUpdated")
	return state
}

// AutoLearnMinSignals is how many new reviewer signals (comments, replies,
// decisions) since the last extraction make an idle agent worth asking.
const AutoLearnMinSignals = 5

// maybeAutoLearn runs when the agent goes idle (attached and not working): with
// "Learn after each review" on and enough new reviewer signals in this review
// since the last extraction, it asks the open tabs to queue one extraction
// request. The watcher never writes comments.csv itself — a tab's next persist
// would erase a row it didn't know about (persist rewrites from state.Comments).
func (c *PrereviewController) maybeAutoLearn() {
	if c.LibraryDir == "" || c.CSVPath == "" || loadUIPrefs(c.UIPrefsPath).AutoLearnOff {
		return
	}
	store := filepath.Dir(c.CSVPath)
	n := library.ReviewerSignals(store)
	if n-library.LastExtractions(c.LibraryDir)[store].Signals < AutoLearnMinSignals {
		return
	}
	c.autoLearn.Store(int64(n))
	c.broadcast("AutoLearn")
}

// AutoLearn queues the extraction request maybeAutoLearn asked for, in the
// first tab that can (an agent attached and a file open: the request is queued
// on it). autoLearn holds the signal count it was asked at; swapping it to 0
// claims it, so it's queued once however many tabs are open, and recording that
// count resets the gate until AutoLearnMinSignals more arrive.
func (c *PrereviewController) AutoLearn(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	if !state.AgentMode || state.SelectedFile == "" || state.extractionOpen() {
		return state, nil
	}
	n := c.autoLearn.Swap(0)
	if n == 0 {
		return state, nil
	}
	store := filepath.Dir(c.CSVPath)
	state, err := c.queueRequest(state, csv.RequestExtraction, library.ExtractionPrompt([]string{store}, c.learnings()))
	if err != nil {
		return state, err
	}
	if err := library.RecordExtraction(c.LibraryDir, store, int(n)); err != nil {
		return state, err
	}
	state.Flash = "Asked your agent to learn from this review"
	return state, nil
}

// ToggleAutoLearn turns "Learn after each review" on or off (a per-user pref).
func (c *PrereviewController) ToggleAutoLearn(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	state.AutoLearnOff = !state.AutoLearnOff
	c.savePrefs(state)
	return state, nil
}
