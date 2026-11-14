package review

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/livetemplate/prereview/gitdiff"
	"github.com/livetemplate/prereview/internal/checks"
)

// writeRepoFile writes a file under the controller's repo.
func writeRepoFile(t *testing.T, c *PrereviewController, rel, body string) {
	t.Helper()
	p := filepath.Join(c.RepoPath, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// reviewState is a state with the Checks page open whose file list holds the
// given changed paths plus one unchanged file the page must skip.
func reviewState(paths ...string) PrereviewState {
	s := PrereviewState{ShowLibrary: true}
	for _, p := range paths {
		s.Files = append(s.Files, gitdiff.FileEntry{Path: p, Status: "M", Added: 1})
	}
	s.Files = append(s.Files, gitdiff.FileEntry{Path: "unchanged.md"})
	return s
}

func pageRow(t *testing.T, p *CheckPage, id string) CheckRow {
	t.Helper()
	for _, g := range append(slices.Clone(p.Groups), p.Folded...) {
		for _, r := range g.Rows {
			if r.ID == id {
				return r
			}
		}
	}
	t.Fatalf("no row %s in %+v", id, p)
	return CheckRow{}
}

func TestReviewChecks_AllChangedFiles(t *testing.T) {
	c := checksController(t)
	writeRepoFile(t, c, "a.md", "This is very good.\n")
	writeRepoFile(t, c, "docs/b.md", "Clearly fine. It is really very nice.\n")
	writeRepoFile(t, c, "unchanged.md", "Very unchanged.\n")
	writeRepoFile(t, c, "img.png", "\x89PNG")
	s := reviewState("a.md", "docs/b.md")
	s.Files = append(s.Files, gitdiff.FileEntry{Path: "img.png", Status: "A", Added: -1})
	c.applyReviewChecks(&s)
	if s.ReviewChecks == nil {
		t.Fatal("no review checks")
	}
	var paths []string
	for _, pc := range s.ReviewChecks.Files {
		paths = append(paths, pc.Path)
	}
	if !slices.Equal(paths, []string{"a.md", "docs/b.md"}) {
		t.Fatalf("checked %v; want the two changed text files (not unchanged.md, not the binary)", paths)
	}

	s.ShowLibrary = true
	s.ExpandedCheck = "filler-intensifiers"
	p := s.CheckPage()
	row := pageRow(t, p, "filler-intensifiers")
	if row.Status != checks.StatusFail || row.Count != 3 || row.Summary != "3 in 2 files" {
		t.Errorf("filler row %+v", row)
	}
	if len(row.ByFile) != 2 || row.ByFile[1].Path != "docs/b.md" || row.ByFile[1].Count != 2 {
		t.Errorf("findings by file %+v", row.ByFile)
	}

	// A second pass over unchanged files re-reads nothing: the memoised disk key
	// (mtime, size) and the cache answer it. Prove it by making the file
	// unreadable without changing its stat.
	os.Chmod(filepath.Join(c.RepoPath, "a.md"), 0)
	defer os.Chmod(filepath.Join(c.RepoPath, "a.md"), 0o644)
	s2 := reviewState("a.md", "docs/b.md")
	c.applyReviewChecks(&s2)
	if len(s2.ReviewChecks.Files) != 2 {
		t.Errorf("second pass dropped a file it should have served from the cache: %+v", s2.ReviewChecks.Files)
	}
}

func TestReviewChecks_NothingChangedChecksEveryFile(t *testing.T) {
	c := checksController(t)
	writeRepoFile(t, c, "a.md", "Very.\n")
	writeRepoFile(t, c, "blob.md", "Very\x00binary.\n") // git didn't flag it; ReadLines does
	s := PrereviewState{ShowLibrary: true}
	s.Files = []gitdiff.FileEntry{{Path: "a.md"}, {Path: "blob.md"}}
	c.applyReviewChecks(&s)
	if s.ReviewChecks == nil || len(s.ReviewChecks.Files) != 1 {
		t.Fatalf("with no changes the page covers every text file (the drawer's rule): %+v", s.ReviewChecks)
	}
}

func TestReviewChecks_OnlyWhileTheLibraryIsOpen(t *testing.T) {
	c := checksController(t)
	writeRepoFile(t, c, "a.md", "Very.\n")
	s := reviewState("a.md")
	s.ShowLibrary = false
	c.applyReviewChecks(&s)
	if s.ReviewChecks != nil {
		t.Fatal("the review-wide pass ran with the Library closed")
	}
	s, _ = c.ToggleLibrary(s, nil)
	if s.ReviewChecks == nil || len(s.ReviewChecks.Files) != 1 {
		t.Fatalf("opening the Library runs it: %+v", s.ReviewChecks)
	}
	s, _ = c.ToggleLibrary(s, nil)
	if s.ReviewChecks != nil {
		t.Error("closing the Library drops its results")
	}
}

func TestReviewChecks_AsyncLandsPerFile(t *testing.T) {
	c := checksController(t)
	os.MkdirAll(c.ChecksDir, 0o755)
	sh := filepath.Join(c.ChecksDir, "slow.sh")
	os.WriteFile(sh, []byte("#!/bin/sh\ncat >/dev/null\nsleep 0.2\necho '[{\"line\":1,\"from_col\":0,\"to_col\":4,\"message\":\"slow\"}]'\n"), 0o755)
	os.WriteFile(filepath.Join(c.ChecksDir, "slow.yml"), []byte("extends: command\ncommand: ['./slow.sh']\n"), 0o644)
	writeRepoFile(t, c, "a.md", "Plain.\n")
	writeRepoFile(t, c, "b.md", "Plain.\n")
	s := reviewState("a.md", "b.md")
	start := time.Now()
	c.applyReviewChecks(&s)
	if time.Since(start) > 150*time.Millisecond {
		t.Errorf("the review-wide pass blocked on commands for %s", time.Since(start))
	}
	s.ShowLibrary = true
	if r := pageRow(t, s.CheckPage(), "slow"); r.Status != checks.StatusRunning {
		t.Fatalf("slow row %+v", r)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		s, _ = c.ChecksUpdated(s, nil)
		if r := pageRow(t, s.CheckPage(), "slow"); r.Status == checks.StatusFail {
			if r.Summary != "2 in 2 files" {
				t.Errorf("slow row %+v", r)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("async results never landed for both files")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// pageState is a page over two files with hand-made results, for the view model.
func pageState() PrereviewState {
	s := PrereviewState{ShowLibrary: true, SelectedFile: "a.md"}
	filler := func(msgs ...string) checks.Result {
		r := checks.Result{ID: "filler", Name: "Filler words", Description: "basically, simply", Category: "Concision", Source: checks.SourceBuiltin, Status: checks.StatusFail}
		for i, m := range msgs {
			r.Findings = append(r.Findings, checks.Finding{Line: i + 1, EndLine: i + 1, Message: m, Match: strings.Fields(m)[0]})
		}
		return r
	}
	pass := checks.Result{ID: "weasel", Name: "Weasel words", Category: "Clarity", Source: checks.SourceBuiltin, Status: checks.StatusPass}
	length := checks.Result{ID: "length", Name: "Sentence length", Category: "Readability", Source: checks.SourceBuiltin, Status: checks.StatusPass}
	mine := checks.Result{ID: "house", Name: "house-style.sh", Category: "Yours", Source: checks.SourceUser, Status: checks.StatusError, Err: "timed out after 5s"}
	s.Checks = &FileChecks{Results: []checks.Result{filler("basically adds nothing", "very is an intensifier"), pass, length, mine}}
	s.ReviewChecks = &ReviewChecks{Total: 2, Files: []PathChecks{
		{Path: "a.md", Results: []checks.Result{filler("stale text the open file no longer has"), pass, length, mine}},
		{Path: "b.md", Results: []checks.Result{filler("simply adds nothing"), pass, length, mine}},
	}}
	return s
}

func TestCheckPage_OpenFileUsesDisplayedText(t *testing.T) {
	s := pageState()
	s.ExpandedCheck = "filler"
	row := pageRow(t, s.CheckPage(), "filler")
	if row.Count != 3 || row.ByFile[0].Findings[0].Message != "basically adds nothing" || !row.ByFile[0].Open {
		t.Errorf("the open file must come from its displayed text (state.Checks): %+v", row.ByFile)
	}
}

func TestCheckPage_FoldsPassingGroups(t *testing.T) {
	s := pageState()
	p := s.CheckPage()
	var shown, folded []string
	for _, g := range p.Groups {
		shown = append(shown, g.Name)
	}
	for _, g := range p.Folded {
		folded = append(folded, g.Name)
	}
	if !slices.Equal(shown, []string{"Concision", "Yours"}) || !slices.Equal(folded, []string{"Clarity", "Readability"}) {
		t.Errorf("shown %v folded %v", shown, folded)
	}
	s.ShowPassingGroups = true
	if p := s.CheckPage(); len(p.Folded) != 0 || len(p.Groups) != 4 {
		t.Errorf("show passing groups: %+v", p.Groups)
	}
}

func TestCheckPage_SearchAndFilters(t *testing.T) {
	s := pageState()

	// A search on a finding's words keeps only matching findings.
	s.ChecksQuery = "SIMPLY"
	s.ExpandedCheck = "filler"
	// "simply" is in Filler words' description, so the whole check matches.
	p := s.CheckPage()
	if p.Matches != 1 {
		t.Errorf("matches %d", p.Matches)
	}
	if r := pageRow(t, p, "filler"); r.Count != 3 || r.MatchNote != "" {
		t.Errorf("a description hit keeps every finding: %+v", r)
	}
	s.ChecksQuery = "intensifier"
	p = s.CheckPage()
	r := pageRow(t, p, "filler")
	if r.Count != 1 || r.MatchNote != "1 of 3 match" || len(r.ByFile) != 1 || r.ByFile[0].Path != "a.md" {
		t.Errorf("a finding hit narrows the findings: %+v", r)
	}
	if p.Matches != 1 || p.Chips[0].Count != 1 {
		t.Errorf("chips recount to the matches: matches %d chips %+v", p.Matches, p.Chips)
	}
	s.ChecksQuery = "readab" // category
	if p := s.CheckPage(); p.Matches != 1 || p.Groups[0].Name != "Readability" {
		t.Errorf("category search %+v", p.Groups)
	}
	s.ChecksQuery = "nothing-matches-this"
	if p := s.CheckPage(); p.Matches != 0 || len(p.Groups) != 0 {
		t.Errorf("no match %+v", p.Groups)
	}
	s.ChecksQuery = ""

	// Status chip: counted under the other filters, then filters the list.
	s.ChecksStatus = checks.StatusError
	p = s.CheckPage()
	if len(p.Groups) != 1 || p.Groups[0].Rows[0].ID != "house" || p.Groups[0].Rows[0].Summary != "timed out" {
		t.Errorf("errors chip %+v", p.Groups)
	}
	if got := map[string]int{"": p.Chips[0].Count, "fail": p.Chips[1].Count, "error": p.Chips[2].Count, "pass": p.Chips[3].Count}; got[""] != 4 || got["fail"] != 1 || got["error"] != 1 || got["pass"] != 2 {
		t.Errorf("chip counts %v", got)
	}
	s.ChecksStatus = ""

	// Source picker and category sidebar. A picked source lists its passing
	// checks too (no folding under a filter).
	s.ChecksSource = checks.SourceUser
	if p := s.CheckPage(); p.Matches != 1 {
		t.Errorf("source=user %d", p.Matches)
	}
	s.ChecksSource = checks.SourceBuiltin
	if p := s.CheckPage(); len(p.Folded) != 0 || len(p.Groups) != 3 {
		t.Errorf("source=builtin must list its passing groups: groups %d folded %d", len(p.Groups), len(p.Folded))
	}
	s.ChecksSource = ""
	s.ChecksCategory = "Clarity"
	if p := s.CheckPage(); len(p.Groups) != 1 || p.Groups[0].Name != "Clarity" {
		t.Errorf("category filter shows the group even though it passes: %+v", p.Groups)
	}
	s.ChecksCategory = ""

	// File chip: only that file's findings.
	s.ChecksFile = "b.md"
	if r := pageRow(t, s.CheckPage(), "filler"); r.Count != 1 || r.ByFile[0].Path != "b.md" {
		t.Errorf("file chip %+v", r)
	}
	s.ChecksFile = ""

	// A disabled check is "off" and counts under the Off chip.
	s.DisabledChecks = []string{"weasel"}
	s.ShowPassingGroups = true
	if r := pageRow(t, s.CheckPage(), "weasel"); r.Status != checkStatusOff {
		t.Errorf("disabled row %+v", r)
	}

	if (PrereviewState{}).CheckPage() != nil {
		t.Error("a closed page must build nothing (zero-arg methods run every render)")
	}
}

func TestCheckPage_ExpandedRowCapsFindings(t *testing.T) {
	s := PrereviewState{ShowLibrary: true, ExpandedCheck: "noisy"}
	r := checks.Result{ID: "noisy", Name: "Noisy", Category: "X", Status: checks.StatusFail}
	for i := range maxPageFindings + 30 {
		r.Findings = append(r.Findings, checks.Finding{Line: i + 1, EndLine: i + 1, Message: "m"})
	}
	s.ReviewChecks = &ReviewChecks{Files: []PathChecks{{Path: "a.md", Results: []checks.Result{r}}}}
	row := pageRow(t, s.CheckPage(), "noisy")
	if n := len(row.ByFile[0].Findings); n != maxPageFindings || row.More != 30 {
		t.Errorf("listed %d, more %d", n, row.More)
	}
}

func TestCheckStrip_ListThenStepper(t *testing.T) {
	c := checksController(t)
	d := mdDiff()
	d.Lines[9].Content = "Really, this is very nice." // a second "very"-family finding, line 10
	s := PrereviewState{SelectedFile: d.Path}
	c.showDiff(&s, d)
	st := s.CheckStrip()
	if st == nil || st.Selected || len(st.Failing) != 1 || st.Failing[0].ID != "filler-intensifiers" || st.Failing[0].Count != 3 {
		t.Fatalf("idle bar %+v", st)
	}

	s, _ = c.SelectCheck(s, checksCtx("selectCheck", map[string]interface{}{"id": "filler-intensifiers"}))
	st = s.CheckStrip()
	if st == nil || !st.Selected || st.Pos != 1 || st.Count != 3 {
		t.Fatalf("stepper %+v", st)
	}
	if s.CursorKey == "" {
		t.Error("selecting from the bar moves to the first finding")
	}
	// Findings: L10 "Really", L10 "very", L25 "very".
	if s.CursorKey != "L10-10" {
		t.Errorf("first finding cursor %q", s.CursorKey)
	}
	s, _ = c.StepFinding(s, checksCtx("stepFinding", map[string]interface{}{"dir": 1}))
	if s.CheckStrip().Pos != 2 {
		t.Errorf("› moves on: pos %d", s.CheckStrip().Pos)
	}
	s, _ = c.StepFinding(s, checksCtx("stepFinding", map[string]interface{}{"dir": 1}))
	if s.CheckStrip().Pos != 3 || s.CursorKey != "L25-25" {
		t.Errorf("› to the third: pos %d cursor %q", s.CheckStrip().Pos, s.CursorKey)
	}
	s, _ = c.StepFinding(s, checksCtx("stepFinding", map[string]interface{}{"dir": 1}))
	if s.CheckStrip().Pos != 1 || s.CursorKey != "L10-10" {
		t.Errorf("› from the last wraps to the first: pos %d cursor %q", s.CheckStrip().Pos, s.CursorKey)
	}
	s, _ = c.StepFinding(s, checksCtx("stepFinding", map[string]interface{}{"dir": -1}))
	if s.CheckStrip().Pos != 3 {
		t.Errorf("‹ from the first wraps to the last: pos %d", s.CheckStrip().Pos)
	}
	// × (selectCheck again) returns the bar to the list.
	s, _ = c.SelectCheck(s, checksCtx("selectCheck", map[string]interface{}{"id": "filler-intensifiers"}))
	if st := s.CheckStrip(); st == nil || st.Selected {
		t.Errorf("after × %+v", st)
	}
}

func TestJumpToFinding_OpensFileAndClosesLibrary(t *testing.T) {
	c := checksController(t)
	c.NoGit = true
	writeRepoFile(t, c, "a.md", "Plain.\n")
	writeRepoFile(t, c, "b.md", "Plain.\nThis is very good.\n")
	s := reviewState("a.md", "b.md")
	a, _ := c.loadDiffCached("", "a.md")
	s.SelectedFile = "a.md"
	c.showDiff(&s, a)
	c.applyReviewChecks(&s)
	s, _ = c.OpenChecks(s, nil)
	if !s.ShowLibrary || s.ChecksFile != "a.md" {
		t.Fatalf("OpenChecks from a file filters to it: %+v", s.ChecksFile)
	}
	s, err := c.JumpToFinding(s, checksCtx("jumpToFinding", map[string]interface{}{"path": "b.md", "id": "filler-intensifiers", "index": 0}))
	if err != nil {
		t.Fatal(err)
	}
	if s.ShowLibrary || s.SelectedFile != "b.md" || s.SelectedCheck != "filler-intensifiers" || s.CursorKey == "" {
		t.Errorf("after Show in file: library %v file %s check %s cursor %q", s.ShowLibrary, s.SelectedFile, s.SelectedCheck, s.CursorKey)
	}
}

func TestQueueFinding_InAnotherFile(t *testing.T) {
	c := checksController(t)
	c.NoGit = true
	writeRepoFile(t, c, "a.md", "Plain.\n")
	writeRepoFile(t, c, "b.md", "Plain.\nThis is very good.\n")
	s := reviewState("a.md", "b.md")
	a, _ := c.loadDiffCached("", "a.md")
	s.SelectedFile = "a.md"
	c.showDiff(&s, a)
	c.applyReviewChecks(&s)
	s, err := c.QueueFinding(s, checksCtx("queueFinding", map[string]interface{}{"path": "b.md", "id": "filler-intensifiers", "index": 0}))
	if err != nil {
		t.Fatal(err)
	}
	cm := s.Comments[len(s.Comments)-1]
	if cm.File != "b.md" || cm.FromLine != 2 || cm.Anchor.Snippet != "very" || s.SelectedFile != "a.md" {
		t.Errorf("queued %+v (selected %s)", cm, s.SelectedFile)
	}
}

func TestCloseFullViews_LibraryExcludesOthers(t *testing.T) {
	c := checksController(t)
	s := PrereviewState{ShowAllComments: true}
	s, _ = c.ToggleLibrary(s, nil)
	if !s.ShowLibrary || s.ShowAllComments {
		t.Errorf("opening the Library closes All comments: %+v", s.ShowAllComments)
	}
	s, _ = c.ToggleCommentList(s, nil)
	if s.ShowLibrary || !s.ShowAllComments {
		t.Errorf("opening All comments closes the Library")
	}
}
