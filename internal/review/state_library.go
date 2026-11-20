package review

import (
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/livetemplate/prereview/csv"
	"github.com/livetemplate/prereview/internal/library"
)

type learnStatus struct{ status, label string }

// learnStatuses are the Learnings tab's status chips, in order.
var learnStatuses = []learnStatus{
	{library.StatusProposed, "Proposed"}, {library.StatusAccepted, "Accepted"},
	{library.StatusSuperseded, "Superseded"}, {library.StatusRejected, "Rejected"},
}

// LearnPage is the Library's Learnings tab.
type LearnPage struct {
	Chips  []CheckChip // the status filter
	Scopes []LearnScope
	Scope  string
	Cards  []LearnCard
	Empty  string // shown when no card matches
}

// LearnScope is one option of the scope picker.
type LearnScope struct {
	Name     string
	Selected bool
}

// LearnCard is one learning on the tab.
type LearnCard struct {
	ID, Text, Scope, Status string
	Pill                    string // the status pill's color class suffix
	Meta                    string // the header's right side: evidence count, or when / by what
	Evidence                []LearnEvidence
	Produces                string         // "instruction · checker"
	Checker, Judge          string         // the drafted checker / judge it adds (shown before you accept)
	Shortcut                *LearnShortcut // the comment-box chip it adds (#204), nil when none
	Conflict                string         // the learning a proposal would supersede
	Editing                 bool
	Actions                 []LearnAction // the card's buttons, in order
}

// LearnAction is one of a card's buttons: the action it submits (Name) and, for
// learningAction, which (Act).
type LearnAction struct {
	Name, Act, Label, Class string
}

var (
	actAccept  = LearnAction{"learningAction", library.ActAccept, "Accept", "accept"}
	actEdit    = LearnAction{"editLearning", "", "Edit", "outline"}
	actReject  = LearnAction{"learningAction", library.ActReject, "Reject", "outline reject"}
	actRestore = LearnAction{"learningAction", library.ActRestore, "Restore", "outline"}
	actDelete  = LearnAction{"learningAction", library.ActDelete, "Delete", "outline secondary"}
)

// LearnShortcut is the chip a learning adds to the comment box.
type LearnShortcut struct {
	Label, Body string
}

// LearnEvidence is one cited record on a card.
type LearnEvidence struct {
	Where, Quote string
}

// libraryTab is the open Library tab, defaulting to Learnings.
func (s PrereviewState) libraryTab() string {
	if s.LibraryTab == "" {
		return libTabLearnings
	}
	return s.LibraryTab
}

// LibraryOnLearnings / LibraryOnChecks pick the tab (zero-arg for the template).
func (s PrereviewState) LibraryOnLearnings() bool { return s.libraryTab() == libTabLearnings }
func (s PrereviewState) LibraryOnChecks() bool    { return s.libraryTab() == libTabChecks }
func (s PrereviewState) LibraryOnSkills() bool    { return s.libraryTab() == libTabSkills }
func (s PrereviewState) LibraryOnShortcuts() bool { return s.libraryTab() == libTabShortcuts }
func (s PrereviewState) LibraryOnBenchmarks() bool {
	return s.libraryTab() == libTabBenchmarks
}

// learnStatusFor is the status chip the Learnings tab shows: the one you
// picked, else Proposed while something waits for you, else Accepted (with
// auto-accept on, where what's new is).
func learnStatusFor(picked string, ls []library.Learning) string {
	if picked != "" {
		return picked
	}
	if slices.ContainsFunc(ls, func(l library.Learning) bool { return l.Status == library.StatusProposed }) {
		return library.StatusProposed
	}
	return library.StatusAccepted
}

// LearnPage builds the Learnings tab, or nil when it isn't showing.
func (s PrereviewState) LearnPage() *LearnPage {
	if !s.ShowLibrary || !s.LibraryOnLearnings() {
		return nil
	}
	status := learnStatusFor(s.LearnStatus, s.Learnings)
	p := &LearnPage{Scope: s.LearnScope}
	counts := map[string]int{}
	scopes := map[string]bool{}
	byID := map[string]library.Learning{}
	for _, l := range s.Learnings {
		counts[l.Status]++
		scopes[l.Scope] = true
		byID[l.ID] = l
	}
	for _, st := range learnStatuses {
		if st.status == library.StatusProposed && counts[st.status] == 0 && status != st.status {
			continue // nothing waiting: with auto-accept on, the usual case
		}
		p.Chips = append(p.Chips, CheckChip{Value: st.status, Label: st.label, Count: counts[st.status], On: st.status == status})
	}
	names := make([]string, 0, len(scopes))
	for n := range scopes {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		p.Scopes = append(p.Scopes, LearnScope{Name: n, Selected: n == s.LearnScope})
	}
	for _, l := range s.Learnings {
		if l.Status != status || (s.LearnScope != "" && l.Scope != s.LearnScope) {
			continue
		}
		p.Cards = append(p.Cards, s.learnCard(l, byID))
	}
	if len(p.Cards) == 0 {
		switch {
		case len(s.Learnings) == 0:
			p.Empty = "No learnings yet. When your agent reads your reviews, " + map[bool]string{
				true:  "it proposes what it learned here for you to accept.",
				false: "what it learns shows here, already in your skills.",
			}[s.AutoAcceptOff]
		case status == library.StatusProposed:
			p.Empty = "Nothing waiting: every proposed learning has been accepted or rejected."
		default:
			p.Empty = "No " + status + " learnings" + map[bool]string{true: " in this scope", false: ""}[s.LearnScope != ""] + "."
		}
	}
	return p
}

func (s PrereviewState) learnCard(l library.Learning, byID map[string]library.Learning) LearnCard {
	c := LearnCard{ID: l.ID, Text: l.Text, Scope: l.Scope, Status: l.Status, Editing: s.LearnEdit == l.ID}
	switch l.Status {
	case library.StatusProposed:
		c.Pill, c.Actions = "new", []LearnAction{actAccept, actEdit, actReject, actDelete}
		c.Meta = fmt.Sprintf("%d piece%s of evidence", len(l.Evidence), plural(len(l.Evidence)))
		if n := l.NewestEvidence(); n != "" {
			c.Meta += " · newest " + shortDate(n)
		}
	case library.StatusAccepted:
		c.Pill, c.Actions = "pass", []LearnAction{actEdit, actDelete}
		c.Meta = "accepted " + shortDate(l.UpdatedAt)
		if l.Auto {
			c.Meta = "added for you " + shortDate(l.CreatedAt)
		}
	case library.StatusRejected:
		c.Pill, c.Actions = "muted", []LearnAction{actAccept, actDelete}
		c.Meta = "rejected " + shortDate(l.UpdatedAt)
	case library.StatusSuperseded:
		c.Pill, c.Actions = "muted", []LearnAction{actRestore, actDelete}
		c.Meta = "superseded " + shortDate(l.UpdatedAt)
		if by, ok := byID[l.SupersededBy]; ok {
			c.Meta = "by “" + clip(by.Text, 48) + "” · " + shortDate(l.UpdatedAt)
		}
	}
	if old, ok := byID[l.Replaces]; ok && l.Status == library.StatusProposed {
		c.Conflict = "“" + clip(old.Text, 80) + "” (" + old.Status + " " + shortDate(old.UpdatedAt) + ")"
	}
	c.Produces = strings.Join(l.Produces, " · ")
	c.Checker, c.Judge = l.Checker, l.Judge
	if l.ShortcutBody != "" {
		c.Shortcut = &LearnShortcut{Label: l.ShortcutLabel, Body: l.ShortcutBody}
	}
	for _, e := range l.Evidence {
		c.Evidence = append(c.Evidence, LearnEvidence{Where: s.evidenceWhere(e), Quote: e.Quote})
	}
	return c
}

// evidenceWhere is an evidence row's location line: "comment · README.md L5 ·
// this review · 2 Nov".
func (s PrereviewState) evidenceWhere(e library.Evidence) string {
	w := e.Kind
	if e.Kind == library.EvidenceReply {
		w = "thread reply"
	}
	if e.File != "" {
		w += " · " + e.File
		if e.Line > 0 {
			w += fmt.Sprintf(" L%d", e.Line)
		}
	}
	if e.Store == s.LearnStore {
		w += " · this review"
	} else {
		w += " · " + storeLabel(e.Store)
	}
	if e.At != "" {
		w += " · " + shortDate(e.At)
	}
	return w
}

// storeLabel names a store by what it reviewed: the folder above .prereview
// (plus the per-target subfolder, if any).
func storeLabel(store string) string {
	dir, base := filepath.Split(filepath.Clean(store))
	if base == library.StoreDir {
		return filepath.Base(filepath.Clean(dir))
	}
	parent := filepath.Dir(filepath.Clean(dir))
	return filepath.Base(parent) + "/" + base
}

// shortDate renders an RFC 3339 timestamp as "2 Nov 2026"; "" when it doesn't
// parse. Always with the year: deterministic (goldens), and evidence spans years.
func shortDate(ts string) string {
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return ""
	}
	return t.Format("2 Jan 2006")
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// LearnedRef is one learning the notice names.
type LearnedRef struct{ ID, Text, At string }

// LearnAddedLabel is the notice's text: the learning itself when there is one.
func (s PrereviewState) LearnAddedLabel() string { return learnedLabel(s.LearnAdded) }

// learnedLabel names learnings added for you: the notice, and the library
// version they cause.
func learnedLabel(ls []LearnedRef) string {
	if len(ls) == 1 {
		return "Learned “" + clip(ls[0].Text, 80) + "”"
	}
	return fmt.Sprintf("%d learnings added", len(ls))
}

// BootCard is the first-launch offer to learn from past reviews, or (on the
// Learnings tab) the progress once your agent is reading.
type BootCard struct {
	Reading    bool // a learning job is open (or a pre-job extraction request)
	Stalled    bool // the job waits with no learner running
	Scanning   bool // the scan hasn't finished
	Stores     []BootStore
	Picked     int
	CanRead    bool // an agent is attached
	AutoAccept bool // what's learned goes straight into your skills
	Waiting    int  // learnings proposed and waiting for you
}

// BootStore is one past review in the offer.
type BootStore struct {
	Store, Label, Counts string
	Picked               bool
}

// BootCard is the offer above the file view, until it's answered on this
// machine, and hidden while a learning job runs: the progress shows only on the
// Learnings tab (a job can run for a while; the toolbar's Library button says
// when it's stuck).
func (s PrereviewState) BootCard() *BootCard {
	if s.ShowLibrary || s.learnOpen() {
		return nil
	}
	return s.bootCard(!s.Bootstrapped)
}

// LibraryBootCard is the Learnings tab's card: a learning job's progress, else
// the offer (reopened there by "Read past reviews", or still unanswered).
func (s PrereviewState) LibraryBootCard() *BootCard {
	if !s.ShowLibrary || !s.LibraryOnLearnings() {
		return nil
	}
	if s.learnOpen() {
		return &BootCard{Reading: true, Stalled: s.LearnStalled, Waiting: s.LearnNew}
	}
	return s.bootCard(!s.Bootstrapped || s.BootOpen)
}

// learnOpen reports a learning job open for this review (or a pre-job
// extraction request).
func (s PrereviewState) learnOpen() bool { return s.LearnReading || s.extractionOpen() }

// bootCard is the first-launch offer, when offer is set.
func (s PrereviewState) bootCard(offer bool) *BootCard {
	if !offer {
		return nil
	}
	if !s.BootScanned {
		if !s.BootOpen {
			return nil // the first-launch scan is still running: don't flash a card
		}
		return &BootCard{Scanning: true}
	}
	if len(s.BootStores) == 0 && !s.BootOpen {
		return nil // nothing to learn from yet
	}
	c := &BootCard{CanRead: s.AgentMode, AutoAccept: !s.AutoAcceptOff}
	for _, st := range s.BootStores {
		b := BootStore{Store: st.Store, Label: st.Label, Counts: storeCounts(st), Picked: !slices.Contains(s.BootSkip, st.Store)}
		if b.Picked {
			c.Picked++
		}
		c.Stores = append(c.Stores, b)
	}
	return c
}

// pickedBootStores is the ticked stores' paths, in list order.
func (s PrereviewState) pickedBootStores() []string {
	var out []string
	for _, st := range s.BootStores {
		if !slices.Contains(s.BootSkip, st.Store) {
			out = append(out, st.Store)
		}
	}
	return out
}

// extractionOpen reports an extraction request comment the agent hasn't
// finished: one queued before learning moved to background jobs.
func (s PrereviewState) extractionOpen() bool {
	for _, c := range s.Comments {
		if c.Request == csv.RequestExtraction && !c.Resolved && !c.Processed {
			return true
		}
	}
	return false
}

// storeCounts is a past review's size: "16 comments · 6 suggestions · 3 versions".
func storeCounts(st library.StoreInfo) string {
	var parts []string
	for _, p := range []struct {
		n    int
		word string
	}{{st.Comments, "comment"}, {st.Suggestions, "suggestion"}, {st.Versions, "version"}} {
		if p.n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s%s", p.n, p.word, plural(p.n)))
		}
	}
	return strings.Join(parts, " · ")
}

// timeLayout is a library time as cards show it ("15 Nov 2026 10:41").
const timeLayout = "2 Jan 2006 15:04"

// shortTime renders an RFC 3339 timestamp in timeLayout (the raw text when it
// doesn't parse).
func shortTime(ts string) string {
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return ts
	}
	return t.Local().Format(timeLayout)
}
