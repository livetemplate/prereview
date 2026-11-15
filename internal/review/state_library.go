package review

import (
	"fmt"
	"path/filepath"
	"sort"
	"time"

	"github.com/livetemplate/prereview/internal/library"
)

// learnStatuses are the Learnings tab's status chips, in order.
var learnStatuses = []string{library.StatusProposed, library.StatusAccepted, library.StatusSuperseded, library.StatusRejected}

// LearnPage is the Library's Learnings tab.
type LearnPage struct {
	Chips  []LearnChip
	Scopes []LearnScope
	Scope  string
	Cards  []LearnCard
	Empty  string // shown when no card matches
}

// LearnChip is one status filter.
type LearnChip struct {
	Status, Label string
	Count         int
	On            bool
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
	Produces                string // "instruction · checker", as proposed (acts in M4)
	Conflict                string // the learning a proposal would supersede
	Editing                 bool
	CanAccept, CanReject    bool
	CanEdit, CanRestore     bool
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
		p.Chips = append(p.Chips, LearnChip{Status: st, Label: titleCase(st), Count: counts[st], On: st == status})
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
		c.Pill, c.CanAccept, c.CanReject, c.CanEdit = "new", true, true, true
		c.Meta = fmt.Sprintf("%d piece%s of evidence", len(l.Evidence), plural(len(l.Evidence)))
		if n := l.NewestEvidence(); n != "" {
			c.Meta += " · newest " + shortDate(n)
		}
	case library.StatusAccepted:
		c.Pill, c.CanEdit = "pass", true
		c.Meta = "accepted " + shortDate(l.UpdatedAt)
	case library.StatusRejected:
		c.Pill, c.CanAccept = "muted", true
		c.Meta = "rejected " + shortDate(l.UpdatedAt)
	case library.StatusSuperseded:
		c.Pill, c.CanRestore = "muted", true
		c.Meta = "superseded " + shortDate(l.UpdatedAt)
		if by, ok := byID[l.SupersededBy]; ok {
			c.Meta = "by “" + clip(by.Text, 48) + "” · " + shortDate(l.UpdatedAt)
		}
	}
	if old, ok := byID[l.Replaces]; ok && l.Status == library.StatusProposed {
		c.Conflict = "“" + clip(old.Text, 80) + "” (" + old.Status + " " + shortDate(old.UpdatedAt) + ")"
	}
	for i, k := range l.Produces {
		if i > 0 {
			c.Produces += " · "
		}
		c.Produces += k
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
	if base == ".prereview" {
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

func titleCase(s string) string {
	if s == "" {
		return s
	}
	return string(s[0]-'a'+'A') + s[1:]
}
