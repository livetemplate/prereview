package review

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/livetemplate/livetemplate"
	"github.com/livetemplate/prereview/csv"
)

// #171. A single-file review's .prereview/ store lives in the file's PARENT directory,
// so it is shared with every other file ever reviewed from there. SingleFile is the
// session's scope; these tests pin that only the file under review ever surfaces.

// twoFileState is a review scoped to b.md whose store also holds a.md's work from an
// earlier session — the exact situation that produced the bug.
func twoFileState() PrereviewState {
	return PrereviewState{
		SingleFile:   "b.md",
		SelectedFile: "b.md",
		ShowResolved: true, // so nothing is dropped by the resolved rule instead of by scope
		Comments: []Comment{
			{ID: "a1", File: "a.md", Body: "stale", FromLine: 1, ToLine: 1},
			{ID: "a2", File: "a.md", Body: "stale resolved", FromLine: 2, ToLine: 2, Resolved: true},
			{ID: "b1", File: "b.md", Body: "current", FromLine: 1, ToLine: 1},
		},
		Suggestions: []Suggestion{
			{ID: "as1", File: "a.md", FromLine: 1, ToLine: 1, OriginalText: "x", ProposedText: "y"},
			{ID: "bs1", File: "b.md", FromLine: 1, ToLine: 1, OriginalText: "p", ProposedText: "q"},
		},
		Decisions: []SuggestionDecision{
			{SuggestionID: "as1", Verdict: verdictAccept, Fingerprint: suggestionFingerprint(
				Suggestion{ID: "as1", File: "a.md", FromLine: 1, ToLine: 1, OriginalText: "x", ProposedText: "y"})},
		},
		ThreadEntries: []ThreadEntry{
			{TargetID: "a1", Author: "reviewer", Body: "on the old file", At: 1},
		},
	}
}

// (a) Every FILE-AGNOSTIC surface — the queue, the all-comments view, the global
// counts — must show b.md's work only. A missed read site shows up here as an extra
// card, which is the fail-safe direction: never as a deleted row.
func TestScope_FileAgnosticSurfacesShowOnlyTheReviewedFile(t *testing.T) {
	s := twoFileState()

	for _, c := range s.VisibleComments() {
		if c.File != "b.md" {
			t.Errorf("VisibleComments leaked %s (%s)", c.ID, c.File)
		}
	}
	if got := s.CommentCount(); got != 1 {
		t.Errorf("CommentCount = %d, want 1 (only b.md)", got)
	}
	if got := s.ResolvedCount(); got != 0 {
		t.Errorf("ResolvedCount = %d, want 0 — a.md's resolved comment is out of scope", got)
	}
	for _, it := range s.QueueItems() {
		if it.File != "b.md" {
			t.Errorf("QueueItems leaked %s (%s) — the reported bug", it.ID, it.File)
		}
	}
	// a.md's suggestion is accepted, so unscoped it would count as queued work.
	if got := s.QueuedCount(); got != 1 {
		t.Errorf("QueuedCount = %d, want 1 (b1 only; a.md's accepted suggestion is out of scope)", got)
	}
	for id := range s.DecisionsBySuggestion() {
		if id != "bs1" {
			t.Errorf("DecisionsBySuggestion leaked %s", id)
		}
	}
	if got := s.DecisionCount(); got != 0 {
		t.Errorf("DecisionCount = %d, want 0 — the only decision is on a.md", got)
	}
	for _, sg := range s.visibleSuggestions() {
		if sg.File != "b.md" {
			t.Errorf("visibleSuggestions leaked %s (%s)", sg.ID, sg.File)
		}
	}

	// Threads/AwaitingAgent are keyed by ID and consumed only as by-ID lookups from an
	// already-scoped card, so they may legitimately carry out-of-scope ids. Assert the
	// property that actually matters: nothing renders them for an out-of-scope target.
	if len(s.VisibleComments()) != 1 || s.VisibleComments()[0].ID != "b1" {
		t.Fatalf("precondition: only b1 should render, got %v", s.VisibleComments())
	}
}

// (c) A directory / git review narrows NOTHING — SingleFile is "". This guards against
// "fixing" the bug by gating on SelectedFile, which would silently break the
// all-comments view (its whole job is to span every file).
func TestScope_DirectoryReviewSeesEveryFile(t *testing.T) {
	s := twoFileState()
	s.SingleFile = "" // a directory review, still sitting on b.md

	if got := s.CommentCount(); got != 3 {
		t.Errorf("CommentCount = %d, want 3 — a directory review spans every file", got)
	}
	if got := len(s.VisibleComments()); got != 3 {
		t.Errorf("VisibleComments = %d, want 3", got)
	}
	if got := s.ResolvedCount(); got != 1 {
		t.Errorf("ResolvedCount = %d, want 1", got)
	}
	files := map[string]bool{}
	for _, it := range s.QueueItems() {
		files[it.File] = true
	}
	if !files["a.md"] || !files["b.md"] {
		t.Errorf("QueueItems should span both files, got %v", files)
	}
}

// (b) THE LANDMINE LOCK — must never regress.
//
// persist() atomically REWRITES comments.csv from the in-memory slice, from 16 call
// sites. So the scope filter must never touch state.Comments (nor loadCommentsFromDisk,
// which fills it): filter the reads, not the buffer. If someone ever "simplifies" this
// by scoping at load — the intuitive fix, and the one the bug report suggested — the
// next resolve/add/delete silently erases every other file's comments from disk.
//
// It drives the REAL path (Mount → reviewer action → persist), not a hand-built state,
// so it catches a filter introduced anywhere along it, not just in loadCommentsFromDisk.
func TestScope_PersistKeepsOutOfScopeRowsOnDisk(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"a.md", "b.md"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("line one\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	csvPath := filepath.Join(dir, ".prereview", "comments.csv")
	if err := os.MkdirAll(filepath.Dir(csvPath), 0o755); err != nil {
		t.Fatal(err)
	}
	c := &PrereviewController{
		RepoPath:   dir,
		SingleFile: "b.md", // reviewing b.md; the store also holds a.md's earlier review
		NoGit:      true,
		CSVPath:    csvPath,
		CSVWriter:  csv.NewWriter(csvPath),
	}

	if err := c.persist([]Comment{
		{ID: "a1", File: "a.md", Body: "from the earlier review", FromLine: 1, ToLine: 1, Side: "new"},
		{ID: "b1", File: "b.md", Body: "current", FromLine: 1, ToLine: 1, Side: "new"},
	}); err != nil {
		t.Fatalf("seed persist: %v", err)
	}

	// The real connect path.
	state, err := c.Mount(PrereviewState{}, nil)
	if err != nil {
		t.Fatalf("Mount: %v", err)
	}

	// The buffer the server holds must remain the FULL store — scope or no scope. This
	// is the invariant; everything below just proves it has teeth.
	if len(state.Comments) != 2 {
		t.Fatalf("state.Comments must stay UNSCOPED (it is the buffer persist rewrites the CSV from), "+
			"got %d comments — a scope filter has been applied at load", len(state.Comments))
	}
	// ...while what the reviewer/agent actually SEES is scoped.
	if state.CommentCount() != 1 {
		t.Fatalf("CommentCount = %d, want 1 — the reads should be scoped even though the buffer isn't",
			state.CommentCount())
	}

	// A perfectly ordinary reviewer action on the file under review → persist fires.
	if _, err := c.ToggleResolved(state, livetemplate.NewContext(context.TODO(), "toggleResolved",
		map[string]interface{}{"id": "b1"})); err != nil {
		t.Fatalf("ToggleResolved: %v", err)
	}

	// Re-read the CSV FROM DISK: a.md's row must still be there.
	rows, err := csv.Read(csvPath)
	if err != nil {
		t.Fatalf("re-read csv: %v", err)
	}
	onDisk := map[string]bool{}
	for _, r := range rows {
		onDisk[r.ID] = true
	}
	if !onDisk["a1"] {
		t.Fatal("DATA LOSS: a.md's comment was erased from comments.csv by an action on b.md — " +
			"a scope filter reached persist(). Filter the reads and the emits, never state.Comments.")
	}
	if !onDisk["b1"] {
		t.Error("b.md's own comment went missing")
	}
}

// (f) The per-file surfaces (CommentsByEndLine and friends) are scoped by SelectedFile,
// not SingleFile, and this fix leaves them alone — which is only safe because a
// single-file review's SelectedFile is pinned to SingleFile. That pin is EMERGENT, not
// explicit: SelectedFile is lvt:"persist", so a stale value CAN arrive from an earlier
// review of another file in this directory; Mount then drops it because it isn't in
// state.Files, and single-file mode's file list has exactly one entry.
//
// Emergent means fragile — one change to ListFilesNoGit and the inline diff would render
// the WRONG file's annotations. Pin it here so that change fails loudly.
func TestScope_StaleSelectedFileIsResetToTheReviewedFile(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"a.md", "b.md"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("line one\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	csvPath := filepath.Join(dir, ".prereview", "comments.csv")
	if err := os.MkdirAll(filepath.Dir(csvPath), 0o755); err != nil {
		t.Fatal(err)
	}
	c := &PrereviewController{
		RepoPath: dir, SingleFile: "b.md", NoGit: true,
		CSVPath: csvPath, CSVWriter: csv.NewWriter(csvPath),
	}

	// A session store left over from reviewing a.md carries SelectedFile=a.md in.
	state, err := c.Mount(PrereviewState{SelectedFile: "a.md"}, nil)
	if err != nil {
		t.Fatalf("Mount: %v", err)
	}
	if state.SelectedFile != "b.md" {
		t.Errorf("SelectedFile = %q, want b.md — a stale persisted selection must not survive "+
			"into a single-file review of another file, or the inline diff renders the wrong file",
			state.SelectedFile)
	}
}

// (d) The agent's snapshot must carry only the reviewed file's work. This is the surface
// the reported bug was actually seen through (`prereview watch` / the Queue), and it is
// emitted from a state built OUTSIDE Mount — so the scope has to be carried there too.
func TestScope_EmittedSnapshotIsScoped(t *testing.T) {
	s := twoFileState()

	for _, sc := range actionableComments(s.scopedComments(), s.Threads()) {
		if sc.File != "b.md" {
			t.Errorf("snapshot comment leaked %s (%s)", sc.ID, sc.File)
		}
	}
	for _, sd := range actionableDecisions(s.scopedSuggestions(), s.DecisionsBySuggestion(), s.Threads(), s.Applied) {
		if sd.File != "b.md" {
			t.Errorf("snapshot decision leaked %s (%s) — the agent would act on a file "+
				"the reviewer isn't reviewing", sd.ID, sd.File)
		}
	}
}

// (e) The CLI's read path honours the session scope — `prereview comments` / `done` are
// handed only `--out <dir>`, so the store is where they learn what's under review. An
// absent scope stays unscoped, so pre-#171 stores and directory reviews behave as before.
func TestScope_LoadCommentsHonoursSessionScope(t *testing.T) {
	dir := t.TempDir()
	csvPath := filepath.Join(dir, "comments.csv")
	w := csv.NewWriter(csvPath)
	if err := w.Write([]csv.Row{
		{ID: "a1", File: "a.md", Body: "stale", FromLine: 1, ToLine: 1, Side: "new"},
		{ID: "b1", File: "b.md", Body: "current", FromLine: 1, ToLine: 1, Side: "new"},
	}); err != nil {
		t.Fatal(err)
	}

	// No session scope recorded (directory review / pre-#171 store) → everything.
	all, err := LoadComments(csvPath, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("unscoped store: want 2 comments, got %d", len(all))
	}

	// Scoped to b.md → the agent sees only b.md's work.
	if err := WriteSessionScope(csvPath, "b.md"); err != nil {
		t.Fatal(err)
	}
	scoped, err := LoadComments(csvPath, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(scoped) != 1 || scoped[0].ID != "b1" {
		t.Fatalf("scoped store: want only b1, got %+v", scoped)
	}
}

// (g) The scope must not outlive its session. openStore clears it every launch and only
// a single-file review rewrites it — otherwise reviewing a.md, then reviewing the whole
// DIRECTORY, would leave the directory review scoped to a.md: the same bug, inverted.
func TestScope_DoesNotOutliveItsSession(t *testing.T) {
	dir := t.TempDir()
	csvPath := filepath.Join(dir, "comments.csv")

	if err := WriteSessionScope(csvPath, "a.md"); err != nil {
		t.Fatal(err)
	}
	if got := SessionScope(csvPath); got != "a.md" {
		t.Fatalf("SessionScope = %q, want a.md", got)
	}

	// A directory review writes no scope — and the launch reset removes the old one.
	// (openStore does the remove; replicate it here, then assert the write is a no-op.)
	if err := os.Remove(SessionPath(csvPath)); err != nil {
		t.Fatal(err)
	}
	if err := WriteSessionScope(csvPath, ""); err != nil {
		t.Fatal(err)
	}
	if got := SessionScope(csvPath); got != "" {
		t.Errorf("SessionScope = %q after a directory-review launch, want \"\" (unscoped) — "+
			"a stale scope would narrow the directory review to one file", got)
	}
}
