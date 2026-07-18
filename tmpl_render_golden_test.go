package main

import (
	"flag"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/livetemplate/livetemplate"
	"github.com/livetemplate/prereview/gitdiff"
	"github.com/livetemplate/prereview/internal/review"
)

// This is the neutrality oracle for the page.tmpl decomposition (#122).
//
// Extracting inline page-shell markup into {{define}} partials + `{{template
// "x" $}}` calls DOES change the template signature (TestTemplateOutputSignature
// goes red — that is intentional, see CLAUDE.md), so the signature guard can no
// longer prove the split is rendering-neutral. This test does: it renders the
// real "prereview" template for a fixture per mutually-exclusive branch and
// compares the FULL emitted HTML against a golden captured from pre-refactor
// main. A pure extraction must leave every golden byte-identical.
//
// It renders through the SAME path the server uses — stageTemplates +
// livetemplate.New(WithParseFiles) — so it exercises livetemplate's parse-time
// flattening of {{template}} calls, on both the initial tree (Execute) and the
// live-update payload (ExecuteUpdates), which is where a fragment boundary at a
// {{template}} call would surface.
//
// Regenerate goldens after an INTENTIONAL rendering change:
//
//	go test -run TestTemplateRenderGolden -update-render .
const renderGoldenDir = "testdata/render"

var updateRender = flag.Bool("update-render", false,
	"regenerate the render + update-payload goldens under testdata/render/ from the current templates/ set")

// newLiveTemplate stages the embedded template set and parses it exactly as the
// server does. Each call returns a fresh instance so Execute/ExecuteUpdates
// don't leak state across fixtures.
func newLiveTemplate(t *testing.T) (*livetemplate.Template, func()) {
	t.Helper()
	paths, cleanup, err := stageTemplates(templatesFS)
	if err != nil {
		t.Fatalf("stage templates: %v", err)
	}
	tmpl, err := livetemplate.New("prereview", livetemplate.WithParseFiles(paths...))
	if err != nil {
		cleanup()
		t.Fatalf("livetemplate.New: %v", err)
	}
	return tmpl, cleanup
}

// lvtInstanceID matches livetemplate's per-instance managed-node id
// (`data-lvt-id="lvt-<16 hex>"`), which is random per template instance and the
// only non-deterministic token in the output. The `lvt-fx`/`lvt-el` directive
// names are never 16 hex chars, so this can't touch them.
var lvtInstanceID = regexp.MustCompile(`lvt-[0-9a-f]{16}`)

// normalizeRender strips the one per-instance random token so two renders of the
// same state are byte-identical. Structure (how many managed regions, where) is
// preserved, so an extraction that added/moved a region still shows up.
func normalizeRender(s string) string {
	return lvtInstanceID.ReplaceAllString(s, "lvt-ID")
}

// renderInitial returns the initial HTML tree livetemplate emits for st, with
// the per-instance id normalized.
func renderInitial(t *testing.T, st review.PrereviewState) string {
	t.Helper()
	tmpl, cleanup := newLiveTemplate(t)
	defer cleanup()
	var b strings.Builder
	if err := tmpl.Execute(&b, st); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	return normalizeRender(b.String())
}

func TestTemplateRenderGolden(t *testing.T) {
	for _, tc := range renderFixtures() {
		t.Run(tc.name, func(t *testing.T) {
			// Determinism: text/template ranges maps in key order, but assert it
			// rather than trust it — a non-deterministic fixture would make the
			// golden comparison a coin flip.
			got := renderInitial(t, tc.state)
			if again := renderInitial(t, tc.state); again != got {
				t.Fatalf("non-deterministic render for %q: two runs differ (or the id normalizer is stale)", tc.name)
			}
			checkGolden(t, filepath.Join(renderGoldenDir, tc.name+".html.golden"), "rendered output", tc.name, got)
		})
	}
}

// TestTemplateUpdateGolden guards the LIVE-UPDATE path. The neutrality risk the
// initial-render golden can't see: if livetemplate places a fragment boundary at
// a `{{template}}` call, first paint can stay identical while the ExecuteUpdates
// payload (the bytes patched over the WebSocket) diverges. Each fixture drives a
// before→after transition that re-renders an extracted region and goldens the
// update payload; a pure extraction must leave it byte-identical.
func TestTemplateUpdateGolden(t *testing.T) {
	for _, tc := range updateFixtures() {
		t.Run(tc.name, func(t *testing.T) {
			got := renderUpdate(t, tc.before, tc.after)
			if again := renderUpdate(t, tc.before, tc.after); again != got {
				t.Fatalf("non-deterministic update payload for %q (or the id normalizer is stale)", tc.name)
			}
			checkGolden(t, filepath.Join(renderGoldenDir, "update-"+tc.name+".payload.golden"), "update payload", tc.name, got)
		})
	}
}

// renderUpdate emits the ExecuteUpdates payload for a before→after transition on
// one instance (Execute establishes the base tree first), id-normalized.
func renderUpdate(t *testing.T, before, after review.PrereviewState) string {
	t.Helper()
	tmpl, cleanup := newLiveTemplate(t)
	defer cleanup()
	if err := tmpl.Execute(io.Discard, before); err != nil {
		t.Fatalf("Execute(before): %v", err)
	}
	var b strings.Builder
	if err := tmpl.ExecuteUpdates(&b, after); err != nil {
		t.Fatalf("ExecuteUpdates: %v", err)
	}
	return normalizeRender(b.String())
}

type updateFixture struct {
	name          string
	before, after review.PrereviewState
}

func updateFixtures() []updateFixture {
	// toolbar region: the agent "working" pill appears (LLMState transition).
	tbBefore := goBase()
	tbBefore.AgentMode = true
	tbAfter := tbBefore
	tbAfter.LLMState = "working"
	tbAfter.LLMMessage = "applying"

	// diff-line card region: an agent reply + worked-on badge reaches the card.
	cardBefore := goBase()
	cardBefore.Comments = []review.Comment{
		{ID: "c1", File: "app.go", Body: "rename", Kind: "line", FromLine: 2, ToLine: 2, Side: "new"},
	}
	cardAfter := cardBefore
	cardAfter.Comments = []review.Comment{{ID: "c1", File: "app.go", Body: "rename", Kind: "line", FromLine: 2, ToLine: 2, Side: "new", Processed: true}}
	cardAfter.ThreadEntries = []review.ThreadEntry{{TargetID: "c1", Author: "agent", Body: "done", At: 1700000000000000000}}

	// markdown-view re-entry: a comment appears on a rendered md block, re-
	// rendering inside {{with $.CurrentDiff}} where the $bs/$be/$mbkey clusters
	// live — the highest-risk sub-view for an update-path divergence.
	mdBefore := repoBase()
	mdBefore.SelectedFile = "README.md"
	mdBefore.CurrentDiff = &gitdiff.FileDiff{
		Path:           "README.md",
		Lines:          []gitdiff.DiffLine{{Kind: "add", NewNum: 1, Content: "# Title"}},
		MarkdownBlocks: []gitdiff.MarkdownBlock{{HTML: "<h1>Title</h1>", StartLine: 1, EndLine: 1}},
	}
	mdAfter := mdBefore
	mdAfter.Comments = []review.Comment{
		{ID: "m1", File: "README.md", Body: "tighten this heading", Kind: "line", FromLine: 1, ToLine: 1, Side: "new"},
	}

	return []updateFixture{
		{"toolbar-pill", tbBefore, tbAfter},
		{"diff-card-thread", cardBefore, cardAfter},
		{"markdown-block-comment", mdBefore, mdAfter},
	}
}

// checkGolden compares got against the golden at path, or (under -update-render)
// rewrites it. `what`/`name` label the artifact in a mismatch message. It owns the
// directory-creation so either golden set can be regenerated on its own.
func checkGolden(t *testing.T, path, what, name, got string) {
	t.Helper()
	if *updateRender {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
		t.Logf("wrote %s (%d bytes)", path, len(got))
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v\ncreate it with: go test -run 'TestTemplate.*Golden' -update-render .", path, err)
	}
	if got != string(want) {
		reportGoldenDiff(t, what, name, string(want), got)
	}
}

// reportGoldenDiff fails t with a readable window around the first byte where got
// diverges from want — a pure page.tmpl extraction must keep these byte-identical.
// It reuses firstDivergence (tmpl_signature_test.go), which returns the offset and
// a context window from each side (got first, then want).
func reportGoldenDiff(t *testing.T, what, name, want, got string) {
	t.Helper()
	off, gotCtx, wantCtx := firstDivergence(want, got)
	t.Errorf("%s for %q changed at offset %d — a pure extraction must be byte-identical.\n golden: %q\n now:    %q",
		what, name, off, wantCtx, gotCtx)
}

// ---- fixtures: one per mutually-exclusive template branch ----

type renderFixture struct {
	name  string
	state review.PrereviewState
}

// repoBase is a minimal valid repo-mode session; fixtures clone and tweak it.
func repoBase() review.PrereviewState {
	return review.PrereviewState{
		RepoPath:     "/repo",
		Base:         "HEAD",
		SelectedFile: "app.go",
		Files: []gitdiff.FileEntry{
			{Path: "app.go", Status: "M", Added: 3, Deleted: 1},
			{Path: "README.md", Status: "M", Added: 2, Deleted: 0},
		},
	}
}

func goDiff() *gitdiff.FileDiff {
	return &gitdiff.FileDiff{
		Path:         "app.go",
		MaxLineChars: 20,
		Lines: []gitdiff.DiffLine{
			{Kind: "ctx", OldNum: 1, NewNum: 1, Content: "package main"},
			{Kind: "del", OldNum: 2, Content: "var x = 1"},
			{Kind: "add", NewNum: 2, Content: "var x = 2"},
		},
	}
}

// goBase is repoBase already viewing app.go's Go diff — the common starting point
// for the fixtures that exercise the diff/code view (as opposed to md/html/binary).
func goBase() review.PrereviewState {
	s := repoBase()
	s.CurrentDiff = goDiff()
	return s
}

func renderFixtures() []renderFixture {
	// diff (code line) view
	diff := goBase()

	// rendered-Markdown view
	md := repoBase()
	md.SelectedFile = "README.md"
	md.CurrentDiff = &gitdiff.FileDiff{
		Path: "README.md",
		Lines: []gitdiff.DiffLine{
			{Kind: "add", NewNum: 1, Content: "# Title"},
		},
		MarkdownBlocks: []gitdiff.MarkdownBlock{
			{HTML: "<h1>Title</h1>", StartLine: 1, EndLine: 1},
		},
		Headings: []gitdiff.Heading{
			{Level: 1, ID: "title", Text: "Title", Line: 1},
			{Level: 2, ID: "sub", Text: "Sub", Line: 3},
		},
	}

	// rendered-HTML view
	html := repoBase()
	html.SelectedFile = "index.html"
	html.CurrentDiff = &gitdiff.FileDiff{
		Path:       "index.html",
		Lines:      []gitdiff.DiffLine{{Kind: "add", NewNum: 1, Content: "<p>hi</p>"}},
		HTMLDoc:    "<body><p data-from=\"1\" data-to=\"1\">hi</p></body>",
		HTMLBlocks: []gitdiff.HTMLBlock{{StartLine: 1, EndLine: 1}},
	}

	// binary previews — one per BinaryKind branch + fallback
	binary := func(path string) review.PrereviewState {
		s := repoBase()
		s.SelectedFile = path
		s.CurrentDiff = &gitdiff.FileDiff{Path: path, IsBinary: true, Note: "file added"}
		return s
	}

	// all-comments overview
	allComments := goBase()
	allComments.ShowAllComments = true
	allComments.Comments = []review.Comment{
		{ID: "c1", File: "app.go", Body: "fix this", Kind: "line", FromLine: 2, ToLine: 2, Side: "new"},
	}

	// external (live-site) mode
	external := review.PrereviewState{
		RepoPath:     "/repo",
		ExternalMode: true,
		TargetURL:    "http://localhost:3000",
		CurrentURL:   "http://localhost:3000/",
		ProxyBaseURL: "http://localhost:7000",
	}

	// version-view (historical read-only) + a populated version timeline so the
	// file-header Versions panel (Phase 2) is exercised, not dark.
	version := goBase()
	version.ViewingVersion = true
	version.VersionViewSeq = 1
	version.Versions = []review.VersionListItem{
		{Seq: 2, Label: "Agent edit", When: "14:03", Current: true},
		{Seq: 1, Label: "Original", When: "14:00", Viewing: true},
	}

	// search palette open (inner search-head/body markup lit)
	searchOpen := goBase()
	searchOpen.SearchOpen = true
	searchOpen.SearchQuery = "app"
	searchOpen.SearchHits = []review.SearchHit{
		{File: "app.go", Kind: "line", NewNum: 2, Line: "var x = 2"},
		{File: "app.go", Kind: "file"},
	}

	// status banners: Quitting / SessionEnded (each its own, they read differently)
	quitting := goBase()
	quitting.Quitting = true

	sessionEnded := goBase()
	sessionEnded.SessionEnded = true

	// transient toasts + in-viewer prompts, all lit at once (independent {{if}}s)
	toasts := goBase()
	toasts.AgentMode = true
	toasts.Flash = "Saved."
	toasts.LLMState = "working"
	toasts.LLMMessage = "applying"
	toasts.LastDeletedComment = &review.Comment{ID: "d1", File: "app.go", Body: "gone", Kind: "line", FromLine: 1, ToLine: 1, Side: "new"}
	toasts.ReanchorCommentID = "c9"
	toasts.PendingRefresh = true
	toasts.AgentPaused = true

	// agent mode with a queued comment (work-queue dropdown + card)
	agent := goBase()
	agent.AgentMode = true
	agent.Comments = []review.Comment{
		{ID: "c1", File: "app.go", Body: "rename", Kind: "line", FromLine: 2, ToLine: 2, Side: "new"},
	}

	// empty states
	emptyPick := repoBase() // Files present, no CurrentDiff selected
	emptyPick.SelectedFile = ""
	emptyPick.CurrentDiff = nil

	emptyNoFiles := review.PrereviewState{RepoPath: "/repo"} // no files at all

	return []renderFixture{
		{"diff-view", diff},
		{"markdown-view", md},
		{"html-view", html},
		{"binary-image", binary("logo.png")},
		{"binary-pdf", binary("doc.pdf")},
		{"binary-video", binary("clip.mp4")},
		{"binary-audio", binary("sound.mp3")},
		{"binary-fallback", binary("archive.zip")},
		{"all-comments", allComments},
		{"external", external},
		{"version-view", version},
		{"agent-mode", agent},
		{"search-open", searchOpen},
		{"banners-quitting", quitting},
		{"banners-session-ended", sessionEnded},
		{"toasts", toasts},
		{"empty-pick-file", emptyPick},
		{"empty-no-files", emptyNoFiles},
	}
}
