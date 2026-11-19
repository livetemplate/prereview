package review

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"slices"
	"time"

	"github.com/livetemplate/livetemplate"

	"github.com/livetemplate/prereview/internal/library"
	"github.com/livetemplate/prereview/internal/skills"
)

// Library tabs. "" is Learnings, the first tab.
const (
	libTabLearnings = "learnings"
	libTabChecks    = "checks"
	libTabShortcuts = "shortcuts"
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

// applyLibrary refreshes what the Library shows: the toolbar badge and the
// added-learnings notice (always) and, while the Learnings tab is open, the
// learnings themselves (a closed Library carries none, so every action's state
// clone stays small). It only reads: the notice is marked seen by its buttons
// and by the actions that show the added learnings (seeIfShown).
func (c *PrereviewController) applyLibrary(state *PrereviewState) {
	ls := c.learnings()
	onLearnings := state.ShowLibrary && state.libraryTab() == libTabLearnings
	state.LearnAdded = nil
	if !onLearnings && c.LibraryDir != "" { // hidden while Learnings shows, back after
		state.LearnAdded = addedSince(ls, library.LearnSeenAt(c.LibraryDir))
	}
	state.LearnNew = len(state.LearnAdded)
	for _, l := range ls {
		if l.Status == library.StatusProposed {
			state.LearnNew++
		}
	}
	state.LearnReading, state.LearnStalled = c.learnJob()
	state.Learnings = nil
	if onLearnings {
		state.Learnings = ls
		state.LearnStore = filepath.Dir(c.CSVPath)
		pinLearnStatus(state, ls) // a proposal arriving while you look
	}
	c.applySkillsTab(state)
	c.applyShortcuts(state) // the chips and the tab: a learning can add a chip
	c.applyBenchTab(state)
	state.Bootstrapped = c.LibraryDir == "" || library.Bootstrapped(c.LibraryDir)
	state.BootStores, state.BootScanned = nil, false
	if !state.Bootstrapped || state.BootOpen {
		c.bootMu.Lock()
		state.BootStores, state.BootScanned = c.bootStores, c.bootScanned
		c.bootMu.Unlock()
	}
}

// addedSince is the learnings accepted on arrival after seen, newest first.
func addedSince(ls []library.Learning, seen string) []LearnedRef {
	var out []LearnedRef
	for _, l := range ls {
		if l.Auto && l.Status == library.StatusAccepted && l.CreatedAt > seen {
			out = append(out, LearnedRef{ID: l.ID, Text: l.Text, At: l.CreatedAt})
		}
	}
	return out
}

// seeLearnings records that you've seen the learnings added for you (in every
// review on this machine), when there are any.
func (c *PrereviewController) seeLearnings() {
	if c.LibraryDir == "" || len(addedSince(c.learnings(), library.LearnSeenAt(c.LibraryDir))) == 0 {
		return
	}
	if _, err := library.MarkLearnSeen(c.LibraryDir); err != nil {
		slog.Warn("learn seen", "err", err)
	}
	c.learnViewChanged() // this server's tabs are told now, not again on the tick
	go c.broadcast("LibraryUpdated")
}

// openLearnings resets the Learnings tab to its default chip when it opens:
// Accepted until something waits for you, then Proposed (pinLearnStatus).
func (c *PrereviewController) openLearnings(state *PrereviewState) {
	if state.ShowLibrary && state.libraryTab() == libTabLearnings {
		state.LearnStatus = ""
		pinLearnStatus(state, c.learnings())
		c.seeIfShown(*state)
	}
}

// pinLearnStatus fixes the default chip on Proposed once something waits, so
// accepting the last proposal leaves you on Proposed ("Nothing waiting")
// instead of jumping to Accepted under your finger.
func pinLearnStatus(state *PrereviewState, ls []library.Learning) {
	if state.LearnStatus == "" && learnStatusFor("", ls) == library.StatusProposed {
		state.LearnStatus = library.StatusProposed
	}
}

// seeIfShown marks the added learnings seen when the Learnings tab now shows
// them: the Accepted chip, every scope. It lands there when nothing is
// proposed; while something is, it opens on Proposed and the notice stays.
func (c *PrereviewController) seeIfShown(state PrereviewState) {
	if state.ShowLibrary && state.libraryTab() == libTabLearnings && state.LearnScope == "" &&
		learnStatusFor(state.LearnStatus, c.learnings()) == library.StatusAccepted {
		c.seeLearnings()
	}
}

// DismissLearned closes the added-learnings notice (in every tab).
func (c *PrereviewController) DismissLearned(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	c.seeLearnings()
	c.applyLibrary(&state)
	return state, nil
}

// ReviewLearned opens Library → Learnings on the accepted ones.
func (c *PrereviewController) ReviewLearned(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	closeFullViews(&state)
	state.ShowLibrary, state.LibraryTab, state.LearnStatus, state.LearnScope = true, libTabLearnings, library.StatusAccepted, ""
	c.seeIfShown(state)
	c.applyLibrary(&state)
	return state, nil
}

// UndoLearned rejects every learning the notice names, in one write: each
// leaves your skills (anything one replaced comes back), and a learning job
// won't propose it again (the prompt lists rejected ones).
func (c *PrereviewController) UndoLearned(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	if c.LibraryDir == "" {
		return state, fmt.Errorf("no library folder")
	}
	added := addedSince(c.learnings(), library.LearnSeenAt(c.LibraryDir))
	ids := make([]string, len(added))
	for i, l := range added {
		ids[i] = l.ID
	}
	if err := library.Undo(c.LibraryDir, ids); err != nil {
		return state, err
	}
	c.seeLearnings()
	state.Flash = fmt.Sprintf("Undid %d learning%s", len(added), plural(len(added)))
	return c.libraryWritten(state, "Undid "+learnedLabel(added)), nil
}

// autoChangelog names the learnings accepted on arrival since the watcher last
// looked ("" on its first look, which only takes the baseline), for the library
// version their sync records: the learner is another process, so no action of
// this server's names them.
func (c *PrereviewController) autoChangelog() string {
	ls := c.learnings()
	c.learnMu.Lock()
	defer c.learnMu.Unlock()
	fresh := addedSince(ls, c.autoMark)
	for _, l := range fresh {
		c.autoMark = max(c.autoMark, l.At)
	}
	if first := !c.autoMarked; first || len(fresh) == 0 {
		c.autoMarked = true
		return ""
	}
	return learnedLabel(fresh)
}

// ToggleAutoAccept turns "Accept automatically" on or off (a per-user
// pref, read when a learning job is queued).
func (c *PrereviewController) ToggleAutoAccept(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	state.AutoAcceptOff = !state.AutoAcceptOff
	c.savePrefs(state)
	return state, nil
}

// libraryFingerprint is what the watcher compares before a skills sync: the
// learnings log, the first-launch marker (answering the offer in one review
// hides it in the others) and the skills' inputs (so another server's Create
// skill or Import, or your edit to a skill, syncs here too).
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

// learnViewChanged reports whether what the tabs show from the library but no
// skills sync depends on moved since the watcher last looked: the learning jobs
// (a job being read), one starting to wait with no learner (no file changes for
// that: the learner's heartbeat moves every second), and the notice being seen
// in any review.
func (c *PrereviewController) learnViewChanged() bool {
	if c.LibraryDir == "" {
		return false
	}
	_, stalled := c.learnJob()
	key := fmt.Sprint(statusFingerprint(filepath.Join(c.LibraryDir, library.LearnJobsFile)), "|",
		statusFingerprint(filepath.Join(c.LibraryDir, library.LearnSeenFile)), "|", stalled)
	c.learnMu.Lock()
	defer c.learnMu.Unlock()
	if key == c.learnViewKey {
		return false
	}
	c.learnViewKey = key
	return true
}

// openJobs is the open learning jobs, re-read only when the log changes.
func (c *PrereviewController) openJobs() []library.Job {
	fp := statusFingerprint(filepath.Join(c.LibraryDir, library.LearnJobsFile))
	c.learnMu.Lock()
	defer c.learnMu.Unlock()
	if fp != c.jobsFP || !c.jobsLoaded {
		c.jobsCache, c.jobsFP, c.jobsLoaded = library.OpenJobs(c.LibraryDir), fp, true
	}
	return c.jobsCache
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

// ReadPastReviews queues one learning job naming exactly the ticked stores for
// the background learner (no file needs to be open: the job never lands on one),
// records that they were read (with their signal counts, for the auto-learn
// gate) and answers the first-launch offer.
func (c *PrereviewController) ReadPastReviews(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	if c.LibraryDir == "" {
		return state, fmt.Errorf("read past reviews: no library folder")
	}
	picked := state.pickedBootStores()
	if len(picked) == 0 {
		return state, fmt.Errorf("read past reviews: no review ticked")
	}
	signals := map[string]int{}
	for _, st := range picked {
		signals[st] = library.ReviewerSignals(st)
	}
	if _, err := c.queueLearnJob(picked, signals, !state.AutoAcceptOff, false); err != nil {
		return state, err
	}
	if err := library.MarkBootstrapped(c.LibraryDir, "read"); err != nil {
		return state, err
	}
	state.BootOpen = false
	state.Flash = fmt.Sprintf("Learning from %d past review%s in the background", len(picked), plural(len(picked)))
	return c.libraryWritten(state, ""), nil
}

// queueLearnJob hands stores to the background learner as one job, recording
// each store's signal count (signals) so auto-learn waits for
// AutoLearnMinSignals more. With onlyIfIdle, nothing is queued (false) while an
// open job covers them.
func (c *PrereviewController) queueLearnJob(stores []string, signals map[string]int, autoAccept, onlyIfIdle bool) (bool, error) {
	_, ok, err := library.QueueJob(c.LibraryDir, library.Job{Stores: stores, AutoAccept: autoAccept,
		Server: filepath.Dir(c.CSVPath)}, onlyIfIdle)
	if err != nil || !ok {
		return false, err
	}
	for _, s := range stores {
		if err := library.RecordExtraction(c.LibraryDir, s, signals[s]); err != nil {
			return true, err
		}
	}
	return true, nil
}

// learnJob reports this review's learning job: one is open (queued or being
// read), and whether it has waited past LearnerStale with no learner running
// (the agent never started one, or it stopped).
func (c *PrereviewController) learnJob() (open, stalled bool) {
	if c.LibraryDir == "" || c.CSVPath == "" {
		return false, false
	}
	store := filepath.Dir(c.CSVPath)
	for _, j := range c.openJobs() {
		if j.Server != store {
			continue
		}
		open = true
		if at, err := time.Parse(time.RFC3339Nano, j.At); j.Op == library.JobQueued && err == nil && time.Since(at) > library.LearnerStale {
			stalled = !library.LearnerAlive(c.LibraryDir)
		}
	}
	return open, stalled
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
	if !slices.Contains([]string{libTabLearnings, libTabShortcuts, libTabSkills, libTabChecks, libTabBenchmarks}, tab) {
		return state, fmt.Errorf("library: unknown tab %q", tab)
	}
	state.LibraryTab = tab
	if tab == libTabLearnings {
		c.openLearnings(&state)
	}
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
	c.seeIfShown(state)
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
	c.learnViewChanged()
	go c.broadcast("LibraryUpdated")
	return state
}

// AutoLearnMinSignals is how many new reviewer signals (comments, replies,
// decisions) since the last extraction make an idle agent worth asking.
const AutoLearnMinSignals = 5

// maybeAutoLearn runs when the agent goes idle (attached and not working): with
// "Learn after each review" on and enough new reviewer signals in this review
// since the last extraction, it queues a learning job for the background
// learner. A job is not a comment: it never touches comments.csv, the open file
// or the agent's queue, so no tab is needed.
func (c *PrereviewController) maybeAutoLearn() {
	if c.LibraryDir == "" || c.CSVPath == "" {
		return
	}
	prefs := loadUIPrefs(c.UIPrefsPath)
	if prefs.AutoLearnOff {
		return
	}
	store := filepath.Dir(c.CSVPath)
	if slices.ContainsFunc(c.openJobs(), func(j library.Job) bool { return slices.Contains(j.Stores, store) }) {
		return // an open job already reads this review
	}
	n := library.ReviewerSignals(store)
	if n-library.LastExtractions(c.LibraryDir)[store].Signals < AutoLearnMinSignals {
		return
	}
	queued, err := c.queueLearnJob([]string{store}, map[string]int{store: n}, !prefs.AutoAcceptOff, true)
	if err != nil {
		slog.Warn("auto-learn", "err", err)
	}
	if queued {
		c.learnViewChanged() // this server's tabs are told now, not again on the tick
		c.broadcast("LibraryUpdated")
	}
}

// ToggleAutoLearn turns "Learn after each review" on or off (a per-user pref).
func (c *PrereviewController) ToggleAutoLearn(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	state.AutoLearnOff = !state.AutoLearnOff
	c.savePrefs(state)
	return state, nil
}
