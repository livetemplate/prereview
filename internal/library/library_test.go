package library

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/livetemplate/prereview/csv"
)

// seedStore writes a review store with one comment (c1), one suggestion (s1)
// and one reviewer reply on c1, and returns its path.
func seedStore(t *testing.T, root string) string {
	t.Helper()
	store := filepath.Join(root, ".prereview")
	if err := os.MkdirAll(store, 0o755); err != nil {
		t.Fatal(err)
	}
	writeComments(t, store, csv.Row{ID: "c1", File: "README.md", FromLine: 3, ToLine: 3, Body: "Drop 'basically'",
		CreatedAt: time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)})
	write(t, filepath.Join(store, suggestionsFile),
		`{"id":"s1","file":"docs/a.md","from_line":7,"proposed":"cut it","note":"too wordy"}`+"\n")
	write(t, filepath.Join(store, reviewerReplies),
		`{"target_id":"c1","body":"still filler","at":1762000000000000000}`+"\n")
	return store
}

func writeComments(t *testing.T, store string, rows ...csv.Row) {
	t.Helper()
	if err := csv.NewWriter(filepath.Join(store, commentsFile)).Write(rows); err != nil {
		t.Fatal(err)
	}
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func appendRaw(t *testing.T, dir string, ls ...Learning) {
	t.Helper()
	for _, l := range ls {
		if err := Append(filepath.Join(dir, LearningsFile), l); err != nil {
			t.Fatal(err)
		}
	}
}

func TestFold_LatestWinsTieByMachine(t *testing.T) {
	dir := t.TempDir()
	appendRaw(t, dir,
		Learning{ID: "a", Text: "v2", UpdatedAt: "2026-11-02T00:00:00Z", Machine: "m1"},
		Learning{ID: "a", Text: "v1", UpdatedAt: "2026-11-01T00:00:00Z", Machine: "m9"}, // older, later in file
		Learning{ID: "b", Text: "from m1", UpdatedAt: "2026-11-03T00:00:00Z", Machine: "m1"},
		Learning{ID: "b", Text: "from m2", UpdatedAt: "2026-11-03T00:00:00Z", Machine: "m2"},
		Learning{ID: "b", Text: "from m0", UpdatedAt: "2026-11-03T00:00:00Z", Machine: "m0"},
		Learning{ID: "gone", Text: "x", Status: StatusDeleted, UpdatedAt: "2026-11-04T00:00:00Z"},
	)
	// A torn line must not poison the fold.
	f, _ := os.OpenFile(filepath.Join(dir, LearningsFile), os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`{"id":"a","text":"tor`)
	f.Close()

	got := map[string]string{}
	for _, l := range LoadLearnings(dir) {
		got[l.ID] = l.Text
	}
	want := map[string]string{"a": "v2", "b": "from m2"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("fold = %v, want %v", got, want)
	}
}

func TestCompact_PreservesFold(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 5; i++ {
		appendRaw(t, dir, Learning{ID: "a", Text: fmt.Sprint("v", i), UpdatedAt: fmt.Sprintf("2026-11-0%dT00:00:00Z", i+1)})
	}
	appendRaw(t, dir, Learning{ID: "d", Status: StatusDeleted, UpdatedAt: "2026-11-09T00:00:00Z"})
	before := foldLearnings(filepath.Join(dir, LearningsFile))
	if err := Compact(dir); err != nil {
		t.Fatal(err)
	}
	after := foldLearnings(filepath.Join(dir, LearningsFile))
	if fmt.Sprint(before) != fmt.Sprint(after) {
		t.Fatalf("compaction changed the fold:\n%v\n%v", before, after)
	}
	if n := lines(t, filepath.Join(dir, LearningsFile)); n != 2 {
		t.Fatalf("compacted log has %d lines, want 2 (one per id, tombstone kept)", n)
	}
}

func lines(t *testing.T, path string) int {
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(b), "\n")
}

func TestImport_SameFileTwiceIsNoop(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	appendRaw(t, src,
		Learning{ID: "a", Text: "theirs", UpdatedAt: "2026-11-05T00:00:00Z", Machine: "laptop"},
		Learning{ID: "b", Text: "old theirs", UpdatedAt: "2026-11-01T00:00:00Z", Machine: "laptop"},
	)
	appendRaw(t, dst, Learning{ID: "b", Text: "mine, newer", UpdatedAt: "2026-11-06T00:00:00Z", Machine: "box"})
	export := filepath.Join(t.TempDir(), "export.jsonl")
	if err := Export(src, export); err != nil {
		t.Fatal(err)
	}
	n, err := Import(dst, export)
	if err != nil || n != 1 {
		t.Fatalf("first import took %d (%v), want 1 (only a; b is older than ours)", n, err)
	}
	size := lines(t, filepath.Join(dst, LearningsFile))
	n, err = Import(dst, export)
	if err != nil || n != 0 || lines(t, filepath.Join(dst, LearningsFile)) != size {
		t.Fatalf("second import took %d (%v) and grew the log; want a no-op", n, err)
	}
	got := map[string]string{}
	for _, l := range LoadLearnings(dst) {
		got[l.ID] = l.Text
	}
	if got["a"] != "theirs" || got["b"] != "mine, newer" {
		t.Fatalf("after import: %v", got)
	}
}

func TestAppend_ConcurrentLosesNothing(t *testing.T) {
	dir := t.TempDir()
	const writers, each = 2, 200
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				if err := PutLearning(dir, Learning{ID: fmt.Sprintf("w%d-%d", w, i), Text: strings.Repeat("x", 500)}); err != nil {
					t.Error(err)
				}
			}
		}(w)
	}
	wg.Wait()
	if n := len(LoadLearnings(dir)); n != writers*each {
		t.Fatalf("folded %d learnings, want %d", n, writers*each)
	}
}

func TestParseRef(t *testing.T) {
	for _, c := range []struct{ ref, store, id string }{
		{"/home/a/.prereview:c1", "/home/a/.prereview", "c1"},
		{`C:\Users\a\.prereview:c1`, `C:\Users\a\.prereview`, "c1"},
		{"/s:c1@1762000000000000000", "/s", "c1@1762000000000000000"},
	} {
		s, id, err := ParseRef(c.ref)
		if err != nil || s != c.store || id != c.id {
			t.Errorf("ParseRef(%q) = %q, %q, %v", c.ref, s, id, err)
		}
	}
	for _, bad := range []string{"nocolon", ":c1", "/s:"} {
		if _, _, err := ParseRef(bad); err == nil {
			t.Errorf("ParseRef(%q) accepted", bad)
		}
	}
}

func TestResolve_EachKind(t *testing.T) {
	store := seedStore(t, t.TempDir())
	for _, c := range []struct{ id, kind, quote string }{
		{"c1", EvidenceComment, "Drop 'basically'"},
		{"s1", EvidenceSuggestion, "too wordy"},
		{"c1@1762000000000000000", EvidenceReply, "still filler"},
	} {
		e, err := Resolve(store + ":" + c.id)
		if err != nil {
			t.Fatalf("%s: %v", c.id, err)
		}
		if e.Kind != c.kind || e.Quote != c.quote || e.Store != store {
			t.Errorf("%s resolved to %+v", c.id, e)
		}
	}
	for _, bad := range []string{store + ":nope", store + ":c1@1", filepath.Join(store, "missing") + ":c1"} {
		if _, err := Resolve(bad); err == nil {
			t.Errorf("Resolve(%q) found something", bad)
		}
	}
}

func TestPropose_RejectsUnresolvableEvidence(t *testing.T) {
	dir, store := t.TempDir(), seedStore(t, t.TempDir())
	_, err := Propose(dir, Proposal{Text: "Cut filler words", Evidence: []string{store + ":c1", store + ":ghost"}})
	if err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("err = %v, want one naming the missing id", err)
	}
	if _, err := os.Stat(filepath.Join(dir, LearningsFile)); !os.IsNotExist(err) {
		t.Fatal("a rejected proposal wrote to the log")
	}
	if _, err := Propose(dir, Proposal{Text: "x"}); err == nil {
		t.Fatal("a proposal with no evidence was accepted")
	}
	if _, err := Propose(dir, Proposal{Text: "x", Evidence: []string{store + ":c1"}, Replaces: "lnope"}); err == nil {
		t.Fatal("--replaces of an unknown learning was accepted")
	}
	l, err := Propose(dir, Proposal{Text: "Cut filler words", Evidence: []string{store + ":c1"}})
	if err != nil || l.Scope != DefaultScope || l.Status != StatusProposed || l.Evidence[0].Quote != "Drop 'basically'" {
		t.Fatalf("propose = %+v, %v", l, err)
	}
}

func TestReplacesAcceptSupersedes_RestoreUnsupersedes(t *testing.T) {
	dir, store := t.TempDir(), seedStore(t, t.TempDir())
	ev := []string{store + ":c1"}
	old, err := Propose(dir, Proposal{Text: "Avoid 'basically'", Evidence: ev})
	if err != nil {
		t.Fatal(err)
	}
	must(t, Act(dir, old.ID, ActAccept))
	nu, err := Propose(dir, Proposal{Text: "Cut all filler words", Evidence: ev, Replaces: old.ID})
	if err != nil {
		t.Fatal(err)
	}
	if st(t, dir, old.ID) != StatusAccepted {
		t.Fatal("proposing a replacement changed the old learning before accept")
	}
	must(t, Act(dir, nu.ID, ActAccept))
	if st(t, dir, old.ID) != StatusSuperseded || st(t, dir, nu.ID) != StatusAccepted {
		t.Fatalf("after accept: old=%s new=%s", st(t, dir, old.ID), st(t, dir, nu.ID))
	}
	if l, _ := Find(LoadLearnings(dir), old.ID); l.SupersededBy != nu.ID {
		t.Fatalf("superseded_by = %q", l.SupersededBy)
	}
	if err := Act(dir, nu.ID, ActRestore); err == nil {
		t.Fatal("restored a learning that isn't superseded")
	}
	must(t, Act(dir, old.ID, ActRestore))
	if st(t, dir, old.ID) != StatusAccepted || st(t, dir, nu.ID) != StatusSuperseded {
		t.Fatalf("after restore: old=%s new=%s", st(t, dir, old.ID), st(t, dir, nu.ID))
	}
	must(t, Edit(dir, old.ID, " Avoid filler ", ""))
	if l, _ := Find(LoadLearnings(dir), old.ID); l.Text != "Avoid filler" || l.Scope != DefaultScope {
		t.Fatalf("edit = %+v", l)
	}
	must(t, Act(dir, nu.ID, ActDelete))
	if _, ok := Find(LoadLearnings(dir), nu.ID); ok {
		t.Fatal("deleted learning still listed")
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func st(t *testing.T, dir, id string) string {
	t.Helper()
	l, ok := Find(LoadLearnings(dir), id)
	if !ok {
		t.Fatalf("%s not found", id)
	}
	return l.Status
}

func TestScan_FindsNestedSkipsExcludedRespectsDepth(t *testing.T) {
	home := t.TempDir()
	a := seedStore(t, filepath.Join(home, "code", "proj"))
	b := seedStore(t, filepath.Join(home, "notes"))
	// A per-target store (#199) one level inside a .prereview.
	perTarget := filepath.Join(b, "plan.md-1a2b")
	must(t, os.MkdirAll(perTarget, 0o755))
	writeComments(t, perTarget, csv.Row{ID: "c9", File: "plan.md", FromLine: 1, ToLine: 1, Body: "hm", CreatedAt: time.Now()})
	seedStore(t, filepath.Join(home, "code", "proj", "node_modules", "dep")) // excluded dir
	seedStore(t, filepath.Join(home, "a", "b", "c", "d", "e", "f", "g"))     // too deep
	must(t, os.MkdirAll(filepath.Join(home, "empty", ".prereview"), 0o755))  // no signals

	var got []string
	for _, s := range Scan(home, ScanMaxDepth) {
		got = append(got, s.Store)
	}
	want := []string{a, b, perTarget}
	if fmt.Sprint(sorted(got)) != fmt.Sprint(sorted(want)) {
		t.Fatalf("scan = %v\nwant  %v", got, want)
	}
	info := Inspect(a)
	if info.Comments != 1 || info.Suggestions != 1 || info.Signals() != 2 {
		t.Fatalf("inspect = %+v", info)
	}
}

func sorted(xs []string) []string {
	out := append([]string(nil), xs...)
	sort.Strings(out)
	return out
}

func TestSessionsAndExtractions(t *testing.T) {
	dir := t.TempDir()
	must(t, RecordSession(dir, Session{Store: "/a/.prereview", Repo: "/a"}))
	must(t, RecordSession(dir, Session{Store: "/b/.prereview", Repo: "/b"}))
	must(t, RecordSession(dir, Session{Store: "/a/.prereview", Repo: "/a", Target: "x.md"}))
	ss := LoadSessions(dir)
	if len(ss) != 2 || ss[0].Store != "/a/.prereview" || ss[0].Target != "x.md" {
		b, _ := json.Marshal(ss)
		t.Fatalf("sessions = %s", b)
	}
	must(t, RecordExtraction(dir, "/a/.prereview", 3))
	must(t, RecordExtraction(dir, "/a/.prereview", 8))
	if e := LastExtractions(dir)["/a/.prereview"]; e.Signals != 8 {
		t.Fatalf("last extraction = %+v", e)
	}
}

func TestReviewerSignals_SkipRequestsAndAgentSuggestions(t *testing.T) {
	store := seedStore(t, t.TempDir()) // c1 + suggestion s1 + one reviewer reply
	if n := ReviewerSignals(store); n != 2 {
		t.Fatalf("signals = %d, want 2 (comment + reply; the agent's suggestion doesn't count)", n)
	}
	writeComments(t, store,
		csv.Row{ID: "c1", File: "README.md", FromLine: 3, ToLine: 3, Body: "Drop 'basically'", CreatedAt: time.Now()},
		csv.Row{ID: "r1", File: "README.md", Kind: "file", Body: ExtractionPrompt([]string{store}, nil), CreatedAt: time.Now()},
		csv.Row{ID: "j1", File: "README.md", Kind: "file", Body: "Judge `README.md` against 1 rubric(s).", CreatedAt: time.Now()})
	write(t, filepath.Join(store, decisionsFile), `{"id":"s1","decision":"reject"}`+"\n")
	if n := ReviewerSignals(store); n != 3 {
		t.Fatalf("signals = %d, want 3 (comment + reply + decision; requests excluded)", n)
	}
	if _, err := Resolve(store + ":r1"); err == nil || !strings.Contains(err.Error(), "request") {
		t.Fatalf("a request comment resolved as evidence: %v", err)
	}
}

func TestExtractionPrompt(t *testing.T) {
	p := ExtractionPrompt([]string{"/a/.prereview", "/b/.prereview"}, []Learning{
		{ID: "l1", Text: "Keep it short.", Scope: "Email", Status: StatusAccepted},
		{ID: "l2", Text: "rejected one", Status: StatusRejected},
	})
	if !IsExtractionRequest(p) {
		t.Fatal("the prompt isn't recognised as a request")
	}
	for _, want := range []string{"from 2 review stores", "`/a/.prereview`", "`/b/.prereview`", "`l1` [accepted · Email] Keep it short.",
		"<target_id>@<at>", "prereview learn propose", "--replaces <id>"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lacks %q:\n%s", want, p)
		}
	}
	if strings.Contains(p, "rejected one") {
		t.Error("a rejected learning is listed as one to dedupe against")
	}
}

func TestBootstrapMarker(t *testing.T) {
	dir := t.TempDir()
	if Bootstrapped(dir) {
		t.Fatal("bootstrapped before answering")
	}
	must(t, MarkBootstrapped(dir, "dismissed"))
	if !Bootstrapped(dir) {
		t.Fatal("not bootstrapped after answering")
	}
}
