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
	"regenerate testdata/render/*.html.golden from the current templates/ set")

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
				t.Fatalf("non-deterministic render for %q: two runs differ", tc.name)
			}

			goldenFile := filepath.Join(renderGoldenDir, tc.name+".html.golden")
			if *updateRender {
				if err := os.MkdirAll(renderGoldenDir, 0o755); err != nil {
					t.Fatalf("mkdir %s: %v", renderGoldenDir, err)
				}
				if err := os.WriteFile(goldenFile, []byte(got), 0o644); err != nil {
					t.Fatalf("write %s: %v", goldenFile, err)
				}
				t.Logf("wrote %s (%d bytes)", goldenFile, len(got))
				return
			}
			want, err := os.ReadFile(goldenFile)
			if err != nil {
				t.Fatalf("read golden %s: %v\ncreate it with: go test -run TestTemplateRenderGolden -update-render .", goldenFile, err)
			}
			if got != string(want) {
				off := firstDiffOffset(string(want), got)
				lo := max(off-40, 0)
				clip := func(s string) string {
					return s[min(lo, len(s)):min(off+80, len(s))]
				}
				t.Errorf("rendered output for %q changed at offset %d.\nA pure page.tmpl extraction must be byte-identical here.\n golden: %q\n now:    %q",
					tc.name, off, clip(string(want)), clip(got))
			}
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
				t.Fatalf("non-deterministic update payload for %q", tc.name)
			}
			goldenFile := filepath.Join(renderGoldenDir, "update-"+tc.name+".payload.golden")
			if *updateRender {
				if err := os.WriteFile(goldenFile, []byte(got), 0o644); err != nil {
					t.Fatalf("write %s: %v", goldenFile, err)
				}
				t.Logf("wrote %s (%d bytes)", goldenFile, len(got))
				return
			}
			want, err := os.ReadFile(goldenFile)
			if err != nil {
				t.Fatalf("read golden %s: %v\ncreate it with: go test -run TestTemplateUpdateGolden -update-render .", goldenFile, err)
			}
			if got != string(want) {
				off := firstDiffOffset(string(want), got)
				lo := max(off-40, 0)
				clip := func(s string) string { return s[min(lo, len(s)):min(off+80, len(s))] }
				t.Errorf("update payload for %q changed at offset %d.\nA pure extraction must be byte-identical.\n golden: %q\n now:    %q",
					tc.name, off, clip(string(want)), clip(got))
			}
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
	tbBefore := repoBase()
	tbBefore.CurrentDiff = goDiff()
	tbBefore.AgentMode = true
	tbAfter := tbBefore
	tbAfter.LLMState = "working"
	tbAfter.LLMMessage = "applying"

	// diff-line card region: an agent reply + worked-on badge reaches the card.
	cardBefore := repoBase()
	cardBefore.CurrentDiff = goDiff()
	cardBefore.Comments = []review.Comment{
		{ID: "c1", File: "app.go", Body: "rename", Kind: "line", FromLine: 2, ToLine: 2, Side: "new"},
	}
	cardAfter := cardBefore
	cardAfter.Comments = []review.Comment{{ID: "c1", File: "app.go", Body: "rename", Kind: "line", FromLine: 2, ToLine: 2, Side: "new", Processed: true}}
	cardAfter.ThreadEntries = []review.ThreadEntry{{TargetID: "c1", Author: "agent", Body: "done", At: 1700000000000000000}}

	return []updateFixture{
		{"toolbar-pill", tbBefore, tbAfter},
		{"diff-card-thread", cardBefore, cardAfter},
	}
}

func firstDiffOffset(a, b string) int {
	n := min(len(a), len(b))
	i := 0
	for i < n && a[i] == b[i] {
		i++
	}
	return i
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

func renderFixtures() []renderFixture {
	// diff (code line) view
	diff := repoBase()
	diff.CurrentDiff = goDiff()

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
	allComments := repoBase()
	allComments.CurrentDiff = goDiff()
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

	// version-view (historical read-only)
	version := repoBase()
	version.CurrentDiff = goDiff()
	version.ViewingVersion = true
	version.VersionViewSeq = 1
	version.Versions = []review.VersionListItem{}

	// agent mode with a queued comment (work-queue dropdown + card)
	agent := repoBase()
	agent.CurrentDiff = goDiff()
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
		{"empty-pick-file", emptyPick},
		{"empty-no-files", emptyNoFiles},
	}
}
