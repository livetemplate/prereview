package main

import (
	"flag"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/livetemplate/livetemplate"
	"github.com/livetemplate/prereview/csv"
	"github.com/livetemplate/prereview/gitdiff"
	"github.com/livetemplate/prereview/internal/checks"
	"github.com/livetemplate/prereview/internal/library"
	"github.com/livetemplate/prereview/internal/review"
	"github.com/livetemplate/prereview/internal/skills"
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
			checkGolden(t, filepath.Join(renderGoldenDir, tc.name+".html.golden"), renderGolden, tc.name, got)
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
			checkGolden(t, filepath.Join(renderGoldenDir, "update-"+tc.name+".payload.golden"), payloadGolden, tc.name, got)
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

// goldenKind labels a golden set and carries the diagnosis shown when it
// mismatches. The two sets fail for materially different reasons, so a single
// generic "a pure extraction must be byte-identical" message would point the
// next reader at the wrong cause — see payloadGolden.
type goldenKind struct {
	what string
	hint string
}

var (
	// renderGolden is the rendered HTML: prereview's own markup, nothing else.
	renderGolden = goldenKind{
		what: "rendered output",
		hint: "  This is prereview's own markup, so a diff here is a real rendering change.\n" +
			"  A pure template extraction (inline markup -> {{define}} + {{template}} call)\n" +
			"  must leave it byte-identical.",
	}
	// payloadGolden is the ExecuteUpdates payload — livetemplate's wire format,
	// which is why it can break without prereview changing at all.
	payloadGolden = goldenKind{
		what: "update payload",
		hint: "  These bytes are livetemplate's fragment-diff WIRE FORMAT, not just prereview\n" +
			"  markup, so there are two very different causes:\n" +
			"    (a) a template change altered the rendered region, or\n" +
			"    (b) livetemplate changed its payload encoding — i.e. a version bump,\n" +
			"        with prereview's markup completely untouched.\n" +
			"  Tell them apart before you touch anything: a bare `go test` resolves\n" +
			"  livetemplate through the parent go.work (the local checkout), while go.mod\n" +
			"  pins the version CI builds. If `GOWORK=off go test` PASSES and this fails,\n" +
			"  it is (b) — the workspace is building unreleased livetemplate, not a\n" +
			"  regression in your change. (Seen for real: v0.18.1 encodes a list update as\n" +
			"  [[\"r\",<hash>],[\"a\",[...]]]; v0.19 encodes it as {\"d\":[...]}.)",
	}
)

// checkGolden compares got against the golden at path, or (under -update-render)
// rewrites it. `kind`/`name` label the artifact in a mismatch message. It owns the
// directory-creation so either golden set can be regenerated on its own.
func checkGolden(t *testing.T, path string, kind goldenKind, name, got string) {
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
		reportGoldenDiff(t, kind, name, string(want), got)
	}
}

// reportGoldenDiff fails t with a readable window around the first byte where got
// diverges from want, plus the kind's diagnosis hint and the regeneration path.
// It reuses firstDivergence (tmpl_signature_test.go), which returns the offset and
// a context window from each side (got first, then want).
func reportGoldenDiff(t *testing.T, kind goldenKind, name, want, got string) {
	t.Helper()
	off, gotCtx, wantCtx := firstDivergence(want, got)
	t.Errorf("%s for %q changed at offset %d.\n golden: %q\n now:    %q\n\n%s\n\n"+
		"  If the change IS intended, regenerate and commit the goldens in the same change:\n"+
		"    go test -run 'TestTemplate.*Golden' -update-render .\n"+
		"  Then read the regenerated diff before committing — confirm it has the shape you\n"+
		"  expect (markup vs. wire-format), so a real regression can't ride along unnoticed.",
		kind.what, name, off, wantCtx, gotCtx, kind.hint)
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

	// quiz view (#191) — one question of each interesting kind: answered-correct,
	// unanswered, an UNGROUNDED anchor (cited a line the diff doesn't have), and an
	// anchorless `decision`. Those last two render differently on purpose and must
	// stay visually distinguishable, so both are pinned here.
	quiz := goBase()
	quiz.ShowQuiz = true
	quiz.Quizzes = quizFixtureQuizzes()
	quiz.QuizAnswers = map[string]review.QuizAnswer{
		"z1\x00q1": {QuizID: "z1", QuestionID: "q1", Choice: 1},
	}

	// the file-header "Quiz me" control, which degrades by library size: ONE
	// prompt renders a plain one-tap button (the phone case), two or more render a
	// picker. Both shapes are pinned so the degrade logic can't silently regress.
	quizBtn := goBase()
	quizBtn.QuizPrompts = []review.Prompt{{Slug: "comprehension", Title: "Quiz me on this diff", Body: "generate a quiz"}}
	quizPicker := goBase()
	quizPicker.QuizPrompts = []review.Prompt{
		{Slug: "comprehension", Title: "Quiz me on this diff", Body: "generate a quiz"},
		{Slug: "security", Title: "Quiz me like a security reviewer", Body: "security quiz"},
	}

	// #147/#211: the "Ask for suggestions" picker with the real built-in library, so
	// the golden pins every shipped prompt row and the "Add your own prompts" link.
	promptsPicker := goBase()
	promptsPicker.Prompts = review.LoadPrompts("")

	// #204: a line composer with the shortcut chips (every built-in, file order).
	shortcuts := goBase()
	shortcuts.SelectionAnchor, shortcuts.SelectionEnd, shortcuts.SelectionSide = 2, 2, "new"
	for _, p := range review.LoadShortcuts("") {
		shortcuts.Shortcuts = append(shortcuts.Shortcuts, review.Shortcut{Slug: p.Slug, Label: p.Title, Body: p.Body})
	}

	// #191: the quiz questions rendered INLINE in the diff — the primary surface.
	// The separate quiz screen is only the overview, so without this fixture the
	// annotation rendering (line-anchored cards + the file-head card) is unguarded.
	quizInline := goBase()
	quizInline.Quizzes = quizFixtureQuizzes()
	quizInline.QuizAnswers = map[string]review.QuizAnswer{
		"z1\x00q1": {QuizID: "z1", QuestionID: "q1", Choice: 1},
	}

	// quiz view with no quiz for the selected file — the empty state must offer a
	// way out, or the reviewer is stranded on a near-blank page (issue #27).
	quizEmpty := goBase()
	quizEmpty.ShowQuiz = true

	// external (live-site) mode
	external := review.PrereviewState{
		RepoPath:     "/repo",
		ExternalMode: true,
		TargetURL:    "http://localhost:3000",
		CurrentURL:   "http://localhost:3000/",
		ProxyBaseURL: "http://localhost:7000",
	}

	// #248: live-site annotations in every agent state: worked on, answered, replied
	// on by the reviewer (awaiting agent, reply form open), resolved, untouched. Pins the
	// sidebar badges + thread + Reply and the pin boxes' is-acted / tooltip.
	extActed := external
	extActed.AgentMode = true
	region := func(id string, y float64) review.Comment {
		return review.Comment{ID: id, Kind: "region", URL: "http://localhost:3000/", Body: "fix " + id,
			Area: review.Area{X: 0.1, Y: y, W: 0.3, H: 0.1}}
	}
	worked, ans, replied, done, fresh := region("r1", 0.1), region("r2", 0.25), region("r3", 0.4), region("r4", 0.55), region("r5", 0.7)
	worked.Processed, ans.Answered, replied.Processed, done.Resolved = true, true, true, true
	extActed.Comments = []review.Comment{worked, ans, replied, done, fresh}
	extActed.ThreadEntries = []review.ThreadEntry{
		{TargetID: "r1", Author: review.AuthorAgent, Body: "Fixed the padding.", At: 1},
		{TargetID: "r2", Author: review.AuthorAgent, Body: "That spacing is intentional.", At: 2},
		{TargetID: "r3", Author: review.AuthorAgent, Body: "Moved it left.", At: 3},
		{TargetID: "r3", Author: review.AuthorReviewer, Body: "Further left, please.", At: 4},
	}
	extActed.ReplyingID = "r3"

	// #248: image area comments in the same states (worked on, answered, resolved, open).
	imgAreas := binary("logo.png")
	imgAreas.AgentMode = true
	area := func(id string, x float64) review.Comment {
		return review.Comment{ID: id, Kind: "area", File: "logo.png", Body: "box " + id,
			Area: review.Area{X: x, Y: 0.1, W: 0.2, H: 0.2}}
	}
	a1, a2, a3, a4 := area("a1", 0.05), area("a2", 0.3), area("a3", 0.55), area("a4", 0.8)
	a1.Processed, a2.Answered, a3.Resolved = true, true, true
	imgAreas.Comments = []review.Comment{a1, a2, a3, a4}

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

	// #205: an answered comment (agent replied, no edit) beside a queued one — the
	// queue's answered row/count/legend and the outlined card badge.
	answered := goBase()
	answered.AgentMode = true
	answered.Comments = []review.Comment{
		{ID: "c1", File: "app.go", Body: "rename", Kind: "line", FromLine: 2, ToLine: 2, Side: "new"},
		{ID: "c2", File: "app.go", Body: "why a map here?", Kind: "line", FromLine: 2, ToLine: 2, Side: "new", Answered: true},
	}
	answered.ThreadEntries = []review.ThreadEntry{{TargetID: "c2", Author: review.AuthorAgent, Body: "Lookups dominate; order never matters.", At: 1}}

	// #149 reply form open on a comment card, with a half-typed draft: pins the shared
	// replyForm partial (#248) so its markup can't drift.
	replyOpen := goBase()
	replyOpen.AgentMode = true
	replyOpen.Comments = []review.Comment{{ID: "c1", File: "app.go", Body: "rename", Kind: "line", FromLine: 2, ToLine: 2, Side: "new"}}
	replyOpen.ReplyingID = "c1"
	replyOpen.ReplyDraft = "half-typed"

	// #218: a comment and a suggestion whose lines the shrunk file no longer has render
	// in the end-of-file block (an outdated comment and an undecided suggestion).
	pastEOF := goBase()
	pastEOF.Comments = []review.Comment{
		{ID: "c1", File: "app.go", Body: "drop this line", Kind: "line", FromLine: 40, ToLine: 40, Side: "new", AnchorStatus: "outdated"},
	}
	pastEOF.Suggestions = []review.Suggestion{
		{ID: "s1", File: "app.go", FromLine: 41, ToLine: 41, Side: "new", OriginalText: "old", ProposedText: "new"},
	}

	// #233: a hidden suggestion leaves a "Show" marker where its card was, on a diff
	// row and on a rendered-Markdown block.
	hiddenSg := review.Suggestion{ID: "s1", File: "app.go", FromLine: 2, ToLine: 2, Side: "new", OriginalText: "old", ProposedText: "new"}
	hiddenRow := goBase()
	hiddenRow.Suggestions = []review.Suggestion{hiddenSg}
	hiddenRow.Hidden = []review.HiddenSuggestion{review.NewHidden(hiddenSg)}
	mdSg := review.Suggestion{ID: "s2", File: "README.md", FromLine: 1, ToLine: 1, Side: "new", OriginalText: "# Title", ProposedText: "# Better"}
	hiddenBlock := md
	hiddenBlock.Suggestions = []review.Suggestion{mdSg}
	hiddenBlock.Hidden = []review.HiddenSuggestion{review.NewHidden(mdSg)}

	// #193: the open-items stepper with its cursor on the second item (a suggestion,
	// which also carries the scroll target) and the Suggestions filter row listed.
	annNav := goBase()
	annNav.Comments = []review.Comment{{ID: "c1", File: "app.go", Body: "rename", Kind: "line", FromLine: 1, ToLine: 1, Side: "new"}}
	annNav.Suggestions = []review.Suggestion{{ID: "s1", File: "app.go", FromLine: 2, ToLine: 2, Side: "new", OriginalText: "old", ProposedText: "new"}}
	navAt := annNav.NavItems()[1].Pos
	annNav.NavAt = &navAt
	annNav.ScrollToItemKey = "sg:s1"

	// checks M1b: the file view with one check selected — the bar is its finding
	// stepper, and its finding is marked and carded inline.
	fileChecks := &review.FileChecks{
		ValeNotice: "This repo has a Vale config. Running it can start tools the config names, so it stays off until you trust this repo.",
		ValeTrust:  true,
		Results: []checks.Result{
			{ID: "filler-intensifiers", Name: "Filler intensifiers", Category: "Concision", Type: "existence", Level: "suggestion", Source: "builtin", Status: "fail",
				Findings: []checks.Finding{{Line: 1, EndLine: 1, FromCol: 8, ToCol: 12, Message: "'very' adds emphasis, not meaning; try cutting it.", Severity: "suggestion", Match: "very"}}},
			{ID: "house-style", Name: "House style", Category: "Yours", Type: "command", Level: "warning", Source: "user", Path: "/home/u/.config/prereview/checks/house-style.yml", Status: "running"},
			{ID: "broken", Name: "broken", Category: "Yours", Source: "user", Path: "/home/u/.config/prereview/checks/broken.yml", Status: "error", Err: "invalid YAML: yaml: line 1: did not find expected node content"},
			{ID: "weasel-words", Name: "Weasel words", Category: "Clarity", Type: "existence", Level: "warning", Source: "builtin", Status: "pass"},
			{ID: "off", Name: "Disabled one", Category: "Clarity", Type: "existence", Level: "warning", Source: "builtin", Status: "pass"},
		},
	}
	checksStrip := repoBase()
	checksStrip.SelectedFile = "notes.txt"
	checksStrip.CurrentDiff = &gitdiff.FileDiff{
		Path: "notes.txt",
		Lines: []gitdiff.DiffLine{
			{Kind: "add", NewNum: 1, Content: "This is very good."},
			{Kind: "add", NewNum: 2, Content: "Plain line."},
		},
	}
	checksStrip.SelectedCheck = "filler-intensifiers"
	checksStrip.DisabledChecks = []string{"off"}
	checksStrip.Checks = fileChecks

	// checks M1b: the Checks page across two files — status chips, the category
	// sidebar, one expanded row with findings grouped by file, a running and an
	// errored rule, a disabled one (which keeps its group listed) and the Vale
	// trust prompt in the Vale category.
	checksPage := checksStrip
	checksPage.SelectedCheck = ""
	checksPage.ShowLibrary = true
	checksPage.LibraryTab = "checks"
	checksPage.ExpandedCheck = "filler-intensifiers"
	checksPage.Editor = review.EditorVSCode
	checksPage.EditorSSHHost = "devbox"
	checksPage.ReviewChecks = &review.ReviewChecks{
		Total:      2,
		ValeNotice: fileChecks.ValeNotice,
		ValeTrust:  true,
		Files: []review.PathChecks{
			{Path: "notes.txt", Results: fileChecks.Results},
			{Path: "docs/guide.md", Results: []checks.Result{
				{ID: "filler-intensifiers", Name: "Filler intensifiers", Category: "Concision", Type: "existence", Level: "suggestion", Source: "builtin", Status: "fail",
					Findings: []checks.Finding{{Line: 3, EndLine: 3, FromCol: 0, ToCol: 6, Message: "'Really' adds emphasis, not meaning; try cutting it.", Severity: "suggestion", Match: "Really"}}},
				{ID: "weasel-words", Name: "Weasel words", Category: "Clarity", Type: "existence", Level: "warning", Source: "builtin", Status: "pass"},
			}},
		},
	}

	// checks M3: the expanded rule's file history (library versions) with a diff
	// and Restore on the older version.
	checksHistory := checksPage
	checksHistory.CheckHistory = []review.RuleVersion{
		{Seq: 3, When: "15 Nov 2026 10:02", Current: true, Stat: "+1 −1",
			Diff: []review.RuleDiffRow{{Kind: "del", Text: "tokens: [very]"}, {Kind: "add", Text: "tokens: [very, really]"}}},
		{Seq: 0, When: "14 Nov 2026 09:00"},
	}

	// checks M2: judges — the page filtered to judges with one expanded (a fail
	// whose quote was found, a stale verdict, a not-run file, your Agree), the file
	// bar with that judge picked (its critique card), and the idle bar offering
	// Run judges.
	judgeRes := func(status, critique string, findings []checks.Finding) checks.Result {
		at := "2026-11-15T10:00:00Z"
		if status == "notrun" {
			at = ""
		}
		return checks.Result{ID: "reader-first", Name: "Written for the reader", Category: "Reader-first", Type: "judge",
			Level: "suggestion", Source: "user", Path: "/home/u/.config/prereview/checks/reader-first.md", Status: status,
			Critique: critique, Model: "claude-opus-5-5", JudgedAt: at, Findings: findings, Advisory: true}
	}
	failJudge := judgeRes("fail", "Explains storage before saying what to do.",
		[]checks.Finding{{Line: 1, EndLine: 1, FromCol: 0, ToCol: 4, Message: "Explains storage before saying what to do.", Severity: "suggestion", Match: "This"}})
	judgesFile := &review.FileChecks{Results: append(append([]checks.Result(nil), fileChecks.Results...), failJudge)}
	judgePage := checksPage
	judgePage.Checks = judgesFile
	judgePage.ExpandedCheck = "reader-first"
	judgePage.ChecksKind = "judge"
	judgePage.JudgeLabels = map[string]string{"notes.txt\x00reader-first\x002026-11-15T10:00:00Z": "agree"}
	judgePage.ReviewChecks = &review.ReviewChecks{Total: 3, Files: []review.PathChecks{
		{Path: "notes.txt", Results: judgesFile.Results},
		{Path: "docs/guide.md", Results: []checks.Result{judgeRes("stale", "Leads with the diagram.", nil)}},
		{Path: "docs/faq.md", Results: []checks.Result{judgeRes("notrun", "", nil)}},
	}}
	// checks M5: calibration — the page's judge is advisory (7 of 9, two you
	// disagreed with); the file bar's picked judge is calibrated.
	judgePage.JudgeCal = map[string]library.Calibration{"reader-first": {Agree: 7, Total: 9, WindowAgree: 7,
		Disagreed: []library.Label{{File: "docs/guide.md", Verdict: "pass", Critique: "Leads with the diagram."},
			{File: "notes.txt", Verdict: "fail", Critique: "Buries the action."}}}}
	// Markup snapshot of a described judge row beside an undescribed checker in one
	// group. (The {{with}}-in-range shift this shape once hit only showed on live
	// updates; TestE2E_WithInRangeChecksRows guards that.)
	descJudge := judgeRes("pass", "", nil)
	descJudge.Category, descJudge.Description = "Clarity", "Does every section carry the narrative forward?"
	descPage := checksPage
	descPage.ExpandedCheck = ""
	descChecker := fileChecks.Results[0] // filler-intensifiers: fails, no description
	descChecker.Category = "Clarity"
	descPage.Checks = &review.FileChecks{Results: []checks.Result{descChecker, descJudge}}
	descPage.ReviewChecks = &review.ReviewChecks{Total: 1, Files: []review.PathChecks{{Path: "notes.txt", Results: descPage.Checks.Results}}}
	judgeCard := checksStrip
	judgeCard.Checks = judgesFile
	judgeCard.SelectedCheck = "reader-first"
	calibratedJudge := failJudge
	calibratedJudge.Advisory = false // calibrated: it counts
	judgeCard.Checks = &review.FileChecks{Results: append(append([]checks.Result(nil), fileChecks.Results...), calibratedJudge)}
	judgeCard.AgentMode = true
	judgeRun := checksStrip
	judgeRun.SelectedCheck = ""
	judgeRun.AgentMode = true
	judgeRun.Checks = &review.FileChecks{Results: append(append([]checks.Result(nil), fileChecks.Results...), judgeRes("notrun", "", nil))}

	// checks M3: the Library's Learnings tab — two proposals (one grounded in
	// this review and another store, one replacing an accepted learning, being
	// edited), the badge, and the superseded filter with Restore.
	ev := func(store, kind, file string, line int, quote, at string) library.Evidence {
		return library.Evidence{Store: store, ID: "c1", Kind: kind, File: file, Line: line, Quote: quote, At: at}
	}
	learns := []library.Learning{
		{ID: "l1", Text: "Cut filler words such as “basically”, “simply” and “very”.", Scope: library.DefaultScope, Status: "proposed",
			Produces: []string{"instruction", "checker"}, CreatedAt: "2026-11-15T09:00:00Z", UpdatedAt: "2026-11-15T09:00:00Z",
			Evidence: []library.Evidence{
				ev("/repo/.prereview", "comment", "release-notes.md", 5, "Drop “basically” — it adds nothing.", "2026-11-15T08:00:00Z"),
				ev("/home/u/notes/.prereview", "reply", "", 0, "no “very” please", "2026-10-28T12:00:00Z"),
			}},
		{ID: "l2", Text: "Use numbered lists for steps.", Scope: "Technical docs", Status: "proposed", Replaces: "l3",
			CreatedAt: "2026-11-14T09:00:00Z", UpdatedAt: "2026-11-14T09:00:00Z",
			Evidence: []library.Evidence{ev("/repo/.prereview", "suggestion", "docs/a.md", 7, "too wordy", "")}},
		{ID: "l3", Text: "Prefer prose over bullet lists.", Scope: "Technical docs", Status: "accepted",
			Produces: []string{"instruction", "checker"}, Checker: "extends: existence\nname: Lists\ntokens: [bullet]\n",
			CreatedAt: "2026-08-02T09:00:00Z", UpdatedAt: "2026-08-02T09:00:00Z"},
		{ID: "l4", Text: "Lead with what changed for the user.", Scope: "Release notes", Status: "superseded", SupersededBy: "l3",
			CreatedAt: "2026-07-01T09:00:00Z", UpdatedAt: "2026-11-09T09:00:00Z"},
	}
	learnings := repoBase()
	learnings.ShowLibrary = true
	learnings.Learnings = learns
	learnings.LearnNew = 2
	learnings.LearnStore = "/repo/.prereview"
	learnings.LearnEdit = "l2"
	superseded := learnings
	superseded.LearnStatus = "superseded"
	superseded.LearnEdit = ""
	acceptedL := learnings
	acceptedL.LearnStatus = "accepted"
	acceptedL.LearnEdit = ""

	// #204: a proposed learning that adds a comment-box shortcut, and the Library's
	// Shortcuts tab with a built-in, one of yours, and a hidden learned one.
	shortcutLearn := repoBase()
	shortcutLearn.ShowLibrary = true
	shortcutLearn.LearnNew = 1
	shortcutLearn.LearnStore = "/repo/.prereview"
	shortcutLearn.Learnings = []library.Learning{{ID: "l9", Text: "Ask why a passage is there before keeping it.", Scope: library.DefaultScope,
		Status: "proposed", Produces: []string{"instruction", "shortcut"}, ShortcutLabel: "Why is this here?",
		ShortcutBody: "Why is this here? Reply with the case for keeping it.", CreatedAt: "2026-11-15T09:00:00Z", UpdatedAt: "2026-11-15T09:00:00Z",
		Evidence: []library.Evidence{ev("/repo/.prereview", "comment", "plan.md", 12, "why is this here", "2026-11-15T08:00:00Z")}}}
	shortcutsTab := repoBase()
	shortcutsTab.ShowLibrary, shortcutsTab.LibraryTab = true, "shortcuts"
	shortcutsTab.ShortcutsTab = &review.ShortcutsTab{Rows: []review.Shortcut{
		{Slug: "shorter", Label: "Make this shorter", Body: "Make this shorter.", Source: "built-in", Uses: 3},
		{Slug: "tone", Label: "Warmer", Body: "Make the tone warmer.", Source: "yours", Uses: 1},
		{Slug: "learned-l9", Label: "Why is this here?", Body: "Why is this here?", Source: "learned", Hidden: true},
	}}

	// checks M4: the Skills tab — a generated Writing skill installed for Claude
	// but not set up for Cursor, edited by you, with its history; a scope proposal.
	skillsTab := repoBase()
	skillsTab.ShowLibrary, skillsTab.LibraryTab = true, "skills"
	skillsTab.SkillsTab = &review.SkillsTab{
		Proposals: []skills.Proposal{{Scope: "Technical docs", Count: 5}},
		Cards: []review.SkillCard{{ID: "learned-writing", Name: "Writing", Scope: "Writing (all documents)",
			Source: "/home/u/.config/prereview/skills/learned-writing/SKILL.md", SourceRel: "~/.config/prereview/skills/learned-writing/SKILL.md",
			Lines: 9, Checkers: 2, Judges: 1, Edited: true, Versions: 2,
			Installs: []review.SkillInstall{{Label: "Claude Code", State: "installed", OK: true}, {Label: "Cursor", State: "not set up here"}},
			History: &review.HistoryView{ID: "learned-writing", Action: "restoreSkillVersion", Versions: []review.RuleVersion{
				{Seq: 4, When: "15 Nov 2026 10:41", Current: true, Stat: "+1 −0", Note: "Accepted “Cut filler words.”",
					Diff: []review.RuleDiffRow{{Kind: "add", Text: "- Cut filler words. <!-- learning l1 -->"}}},
				{Seq: 1, When: "9 Nov 2026 09:00"}}}}},
		Imported: []review.ImportedCard{{Name: "house-style", Path: "/home/u/.claude/skills/house-style/SKILL.md", PathRel: "~/.claude/skills/house-style/SKILL.md",
			Changed: true, Overlay: 2, Checks: 2,
			Skipped: []string{"notes/ran-tests.md: a tool_used grader grades an agent run, not a document"}}},
		Found: []review.FoundSkill{{Name: "blog-voice", Hash: "abc123", Where: "~/.claude/skills/blog-voice/SKILL.md", Copies: 2, HasEvals: true}},
	}

	// checks M3: first launch — the offer above the file (two past reviews, one
	// unticked, an agent attached) and the progress once the agent is reading.
	boot := goBase()
	boot.AgentMode = true
	boot.BootScanned = true
	boot.BootStores = []library.StoreInfo{
		{Store: "/home/u/notes/.prereview", Label: "~/notes", Comments: 16, Suggestions: 6, Versions: 3, Signals: 16},
		{Store: "/home/u/code/app/.prereview", Label: "~/code/app", Comments: 1, Signals: 1},
	}
	boot.BootSkip = []string{"/home/u/code/app/.prereview"}
	bootReading := goBase()
	bootReading.Bootstrapped = true
	bootReading.LearnNew = 3
	bootReading.Comments = append(bootReading.Comments, review.Comment{ID: "x1", File: bootReading.SelectedFile, Kind: "file", Request: csv.RequestExtraction,
		Body: library.ExtractionPrompt([]string{"/home/u/notes/.prereview"}, nil), Created: time.Date(2026, 11, 15, 9, 0, 0, 0, time.UTC)})

	// checks M5: the Benchmarks tab — a benchmark with its score over 3 runs, the
	// latest run's matrix (a judge still waiting, an errored checker), a run that
	// isn't comparable, the new-benchmark form and the judge calibration table.
	benchTab := repoBase()
	benchTab.ShowLibrary, benchTab.LibraryTab = true, "benchmarks"
	benchTab.BenchTab = &review.BenchTab{CanAdd: true,
		Cards: []review.BenchCard{{ID: "release-notes", Name: "Release notes", Cases: 2, Checkers: 2, Judges: 1,
			Score: "0.83", Spread: "± 0.06 over 3 runs · weighted pass rate", Asking: true,
			Headers: []string{"Filler intensifiers", "House", "Reader first"},
			Rows: []review.BenchRow{
				{Label: "v0.27 notes", Cells: []review.BenchCell{{Mark: "✓", Class: "bm-ok"}, {Mark: "!", Class: "bm-err", Title: "errored (not counted): timed out"}, {Mark: "…", Class: "bm-wait", Title: "waiting for your agent"}}},
				{Label: "v0.28 draft", Cells: []review.BenchCell{{Mark: "✗", Class: "bm-no", Title: "2 findings"}, {Mark: "✓", Class: "bm-ok"}, {Mark: "✗", Class: "bm-no", Title: "Buries the action."}}}},
			Runs: []review.BenchRunRow{
				{When: "15 Nov 2026 10:12", Score: "0.67 so far (1 waiting)", Pins: "filler@3f2a · house@91c0 · reader@77ab", Comparable: true},
				{When: "14 Nov 2026 09:00", Score: "0.88", Model: "claude-opus-5-5", Pins: "filler@3f2a · house@91c0 · reader@1d4e"}},
			DefPath: "/home/u/.config/prereview/benchmarks/release-notes/benchmark.yml"}},
		Judges: []review.JudgeCalRow{{Name: "Reader first", Agrees: "9 / 10", Status: "calibrated", Calibrated: true},
			{Name: "No marketing", Agrees: "6 / 6", Status: "advisory · needs 10 answers"}},
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
		{"binary-image-areas", imgAreas},
		{"binary-pdf", binary("doc.pdf")},
		{"binary-video", binary("clip.mp4")},
		{"binary-audio", binary("sound.mp3")},
		{"binary-fallback", binary("archive.zip")},
		{"all-comments", allComments},
		{"quiz", quiz},
		{"quiz-empty", quizEmpty},
		{"quiz-inline", quizInline},
		{"quiz-me-button", quizBtn},
		{"quiz-me-picker", quizPicker},
		{"prompts-picker", promptsPicker},
		{"shortcuts-composer", shortcuts},
		{"external", external},
		{"external-acted", extActed},
		{"version-view", version},
		{"agent-mode", agent},
		{"agent-answered", answered},
		{"reply-open", replyOpen},
		{"past-eof", pastEOF},
		{"hidden-marker-row", hiddenRow},
		{"hidden-marker-block", hiddenBlock},
		{"ann-nav", annNav},
		{"checks-strip", checksStrip},
		{"checks-page", checksPage},
		{"checks-page-judge", judgePage},
		{"checks-page-desc", descPage},
		{"checks-page-history", checksHistory},
		{"checks-judge-card", judgeCard},
		{"checks-judge-run", judgeRun},
		{"learnings", learnings},
		{"learnings-superseded", superseded},
		{"learnings-accepted", acceptedL},
		{"learnings-shortcut", shortcutLearn},
		{"library-shortcuts", shortcutsTab},
		{"library-skills", skillsTab},
		{"library-benchmarks", benchTab},
		{"boot-offer", boot},
		{"boot-reading", bootReading},
		{"search-open", searchOpen},
		{"banners-quitting", quitting},
		{"banners-session-ended", sessionEnded},
		{"toasts", toasts},
		{"empty-pick-file", emptyPick},
		{"empty-no-files", emptyNoFiles},
	}
}

// quizFixtureQuizzes is the quiz data shared by the inline and overview
// fixtures: one answered line question, one unanswered, one UNGROUNDED (cites a
// line the diff does not have) and one file-level. Those last two render
// differently on purpose and must stay distinguishable.
func quizFixtureQuizzes() []review.Quiz {
	return []review.Quiz{{
		ID: "z1", File: "app.go",
		Questions: []review.Question{
			{ID: "q1", Kind: review.QuestionKindLine, Probe: review.ProbeConsequence, Prompt: "What breaks?",
				Options: []string{"nothing", "the write"}, Answer: 1, Why: "it is a buffer",
				FromLine: 2, ToLine: 2, Side: "new", AnchorStatus: "ok"},
			{ID: "q2", Kind: review.QuestionKindLine, Probe: review.ProbeRationale, Prompt: "Why this way?",
				Options: []string{"speed", "clarity"}, Answer: 1, Why: "reads better",
				FromLine: 1, ToLine: 1, Side: "new", AnchorStatus: "ok"},
			{ID: "q3", Kind: review.QuestionKindLine, Probe: review.ProbeLocalization, Prompt: "Where does it live?",
				Options: []string{"here", "there"}, Answer: 0, Why: "top of the file",
				FromLine: 999, ToLine: 999, Side: "new", AnchorStatus: "ungrounded"},
			{ID: "q4", Kind: review.QuestionKindFile, Probe: review.ProbeDecision, Prompt: "What did you decide unasked?",
				Options: []string{"a dependency", "skipping a test"}, Answer: 1,
				Why: "the request never mentioned tests", AnchorStatus: "absent"},
		},
	}}
}
