package review

import (
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

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
	Produces                string // "instruction · checker"
	Checker, Judge          string // the drafted checker / judge it adds (shown before you accept)
	Conflict                string // the learning a proposal would supersede
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
func (s PrereviewState) LibraryOnBenchmarks() bool {
	return s.libraryTab() == libTabBenchmarks
}

// LearnPage builds the Learnings tab, or nil when it isn't showing.
func (s PrereviewState) LearnPage() *LearnPage {
	if !s.ShowLibrary || !s.LibraryOnLearnings() {
		return nil
	}
	status := s.LearnStatus
	if status == "" {
		status = library.StatusProposed
	}
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
			p.Empty = "No learnings yet. When your agent reads your reviews, it proposes what it learned here for you to accept."
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

// BootCard is the first-launch offer to learn from past reviews, or the
// progress once your agent is reading them.
type BootCard struct {
	Reading  bool // an extraction request is open
	Scanning bool // the scan hasn't finished
	Stores   []BootStore
	Picked   int
	CanRead  bool // an agent is attached
	Waiting  int  // learnings proposed and waiting for you
}

// BootStore is one past review in the offer.
type BootStore struct {
	Store, Label, Counts string
	Picked               bool
}

// BootCard is the offer above the file view: until it's answered on this machine,
// and while the agent reads.
func (s PrereviewState) BootCard() *BootCard {
	if s.ShowLibrary {
		return nil
	}
	return s.bootCard(!s.Bootstrapped)
}

// LibraryBootCard is the same offer on the Learnings tab: reopened there by
// "Read past reviews", or still unanswered.
func (s PrereviewState) LibraryBootCard() *BootCard {
	if !s.ShowLibrary || !s.LibraryOnLearnings() {
		return nil
	}
	return s.bootCard(!s.Bootstrapped || s.BootOpen)
}

func (s PrereviewState) bootCard(offer bool) *BootCard {
	if s.extractionOpen() {
		return &BootCard{Reading: true, Waiting: s.LearnNew}
	}
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
	c := &BootCard{CanRead: s.AgentMode}
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

// extractionOpen reports an extraction request the agent hasn't finished.
func (s PrereviewState) extractionOpen() bool {
	for _, c := range s.Comments {
		if c.Kind == commentKindFile && !c.Resolved && !c.Processed && library.IsExtractionRequest(c.Body) {
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
