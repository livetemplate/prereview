package review

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/livetemplate/livetemplate"
	"github.com/livetemplate/prereview/csv"
	"github.com/livetemplate/prereview/gitdiff"
	"github.com/livetemplate/prereview/internal/checks"
)

func checksController(t *testing.T) *PrereviewController {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "comments.csv")
	return &PrereviewController{
		RepoPath: dir, CSVPath: path, CSVWriter: csv.NewWriter(path),
		UIPrefsPath: filepath.Join(dir, "ui-prefs.json"),
		ChecksDir:   filepath.Join(dir, "checks"),
	}
}

func checksCtx(action string, data map[string]interface{}) *livetemplate.Context {
	return livetemplate.NewContext(context.TODO(), action, data)
}

// mdDiff is a 40-line Markdown file whose only prose problem is "very" on line 25,
// far from the single change on line 1, so it would fold in Diff view.
func mdDiff() *gitdiff.FileDiff {
	d := &gitdiff.FileDiff{Path: "doc.md"}
	d.Lines = append(d.Lines, gitdiff.DiffLine{NewNum: 1, Kind: "add", Content: "# Title"})
	for i := 2; i <= 40; i++ {
		c := "Plain line."
		if i == 25 {
			c = "This is very good."
		}
		d.Lines = append(d.Lines, gitdiff.DiffLine{OldNum: i, NewNum: i, Kind: "ctx", Content: c})
	}
	return d
}

func resultByID(t *testing.T, s PrereviewState, id string) checks.Result {
	t.Helper()
	r, ok := s.checkResult(id)
	if !ok {
		t.Fatalf("no result for %s in %+v", id, s.Checks)
	}
	return r
}

func TestApplyChecks_BuiltinsOnMarkdown(t *testing.T) {
	c := checksController(t)
	var s PrereviewState
	s.SelectedFile = "doc.md"
	c.showDiff(&s, mdDiff())
	if !s.ShowChecks() {
		t.Fatal("a Markdown file should get the built-in checks")
	}
	r := resultByID(t, s, "filler-intensifiers")
	if r.Status != checks.StatusFail || len(r.Findings) != 1 || r.Findings[0].Line != 25 {
		t.Fatalf("filler-intensifiers %+v", r)
	}
	cs := s.CheckSummary()
	if cs.Total != len(s.Checks.Results) || cs.Passed != cs.Total-1 || !cs.Failing {
		t.Errorf("summary %+v", cs)
	}

	// Disabling leaves the count.
	s, err := c.ToggleCheckEnabled(s, checksCtx("toggleCheckEnabled", map[string]interface{}{"id": "filler-intensifiers"}))
	if err != nil {
		t.Fatal(err)
	}
	if got := s.CheckSummary(); got.Total != cs.Total-1 || got.Failing {
		t.Errorf("disabled check still counted: %+v", got)
	}
	// ...and persists per user.
	if p := loadUIPrefs(c.UIPrefsPath); len(p.DisabledChecks) != 1 || p.DisabledChecks[0] != "filler-intensifiers" {
		t.Errorf("prefs %+v", p.DisabledChecks)
	}
}

func TestApplyChecks_CodeFileHasNone(t *testing.T) {
	c := checksController(t)
	var s PrereviewState
	c.showDiff(&s, &gitdiff.FileDiff{Path: "main.go", Lines: []gitdiff.DiffLine{{NewNum: 1, Kind: "add", Content: "// very"}}})
	if s.ShowChecks() {
		t.Errorf("prose rules ran on a .go file: %+v", s.Checks)
	}
}

func TestApplyChecks_HistoricalVersionHasNone(t *testing.T) {
	c := checksController(t)
	var s PrereviewState
	c.showDiff(&s, mdDiff())
	c.showHistoricalDiff(&s, mdDiff())
	if s.Checks != nil {
		t.Error("checks must not show on a historical version")
	}
}

// Only the SELECTED check's findings unfold rows and draw; the badge count (the
// annotation total) never includes findings.
func TestSelectedCheck_UnfoldsAndMarks(t *testing.T) {
	c := checksController(t)
	var s PrereviewState
	s.SelectedFile = "doc.md"
	c.showDiff(&s, mdDiff())
	if hasLine(s.VisibleLines(), 25) {
		t.Fatal("precondition: an unselected check must not unfold line 25")
	}
	if strings.Contains(string(s.LineDisplay()["L25-25"]), "check-finding") {
		t.Fatal("an unselected check drew a mark")
	}
	s, _ = c.SelectCheck(s, checksCtx("selectCheck", map[string]interface{}{"id": "filler-intensifiers"}))
	if !hasLine(s.VisibleLines(), 25) {
		t.Fatal("the selected check's finding row is folded")
	}
	if got := string(s.LineDisplay()["L25-25"]); !strings.Contains(got, `<mark class="check-finding">very</mark>`) {
		t.Errorf("line 25 display %q", got)
	}
	if cards := s.FindingsByRow()["25-new"]; len(cards) != 1 || cards[0].Index != 0 {
		t.Errorf("finding cards %+v", cards)
	}
	if n := s.AnnotationCountLines()["25-new"]; n != 0 {
		t.Errorf("findings leaked into the badge count: %d", n)
	}
	// Selecting again clears it; disabling the selected check clears it too.
	s2, _ := c.SelectCheck(s, checksCtx("selectCheck", map[string]interface{}{"id": "filler-intensifiers"}))
	if s2.SelectedCheck != "" || hasLine(s2.VisibleLines(), 25) {
		t.Error("re-select didn't clear")
	}
	s3, _ := c.ToggleCheckEnabled(s, checksCtx("toggleCheckEnabled", map[string]interface{}{"id": "filler-intensifiers"}))
	if s3.SelectedCheck != "" || s3.FindingsByRow() != nil {
		t.Error("disabling the selected check left its findings drawn")
	}
}

// A noisy rule (a hit on every line) must not unfold anything until selected.
func TestNoisyCheckBounded(t *testing.T) {
	c := checksController(t)
	if err := os.MkdirAll(c.ChecksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(c.ChecksDir, "noisy.yml"), []byte("extends: existence\ntokens: [Plain]\n"), 0o644)
	var s PrereviewState
	c.showDiff(&s, mdDiff())
	if r := resultByID(t, s, "noisy"); len(r.Findings) < 30 {
		t.Fatalf("noisy rule findings %d", len(r.Findings))
	}
	if !hasKind(s.VisibleLines(), "fold") {
		t.Error("an unselected noisy check unfolded the file")
	}
}

func TestQueueFinding_CreatesTextComment(t *testing.T) {
	c := checksController(t)
	var s PrereviewState
	s.SelectedFile = "doc.md"
	c.showDiff(&s, mdDiff())
	s, err := c.QueueFinding(s, checksCtx("queueFinding", map[string]interface{}{"id": "filler-intensifiers", "index": 0}))
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Comments) != 1 {
		t.Fatalf("comments %+v", s.Comments)
	}
	cm := s.Comments[0]
	if cm.Kind != commentKindText || cm.FromLine != 25 || cm.ToLine != 25 || cm.FromCol != 8 || cm.ToCol != 12 ||
		cm.Side != "new" || cm.Anchor.Snippet != "very" || cm.Draft {
		t.Errorf("comment %+v", cm)
	}
	if !strings.Contains(cm.Body, "Filler intensifiers") {
		t.Errorf("body %q", cm.Body)
	}
	if s.EnqueueTick == 0 {
		t.Error("a queued finding must pulse the queue like any new comment")
	}
	disk := c.loadCommentsFromDisk()
	if len(disk) != 1 || disk[0].ID != cm.ID {
		t.Errorf("not persisted: %+v", disk)
	}
}

// An edited rule file reruns the checks (the library hash is in the cache key).
func TestRuleEditReruns(t *testing.T) {
	c := checksController(t)
	os.MkdirAll(c.ChecksDir, 0o755)
	rule := filepath.Join(c.ChecksDir, "mine.yml")
	os.WriteFile(rule, []byte("extends: existence\ntokens: [Title]\n"), 0o644)
	var s PrereviewState
	c.showDiff(&s, mdDiff())
	if r := resultByID(t, s, "mine"); r.Status != checks.StatusFail {
		t.Fatalf("mine %+v", r)
	}
	time.Sleep(20 * time.Millisecond) // a distinct mtime
	os.WriteFile(rule, []byte("extends: existence\ntokens: [nothing-matches]\n"), 0o644)
	if !c.reloadChecksIfChanged() {
		t.Fatal("rule edit not detected")
	}
	s, _ = c.ChecksUpdated(s, nil)
	if r := resultByID(t, s, "mine"); r.Status != checks.StatusPass {
		t.Errorf("after edit %+v", r)
	}
	os.WriteFile(rule, []byte("extends: ["), 0o644)
	c.reloadChecksIfChanged()
	s, _ = c.ChecksUpdated(s, nil)
	if r := resultByID(t, s, "mine"); r.Status != checks.StatusError || r.Err == "" {
		t.Errorf("broken rule %+v", r)
	}
}

// A slow command check never blocks the render: it shows running, and its
// result replaces the placeholder in the cache when it lands.
func TestAsyncCommandCheck(t *testing.T) {
	c := checksController(t)
	os.MkdirAll(c.ChecksDir, 0o755)
	sh := filepath.Join(c.ChecksDir, "slow.sh")
	os.WriteFile(sh, []byte("#!/bin/sh\ncat >/dev/null\nsleep 0.3\necho '[{\"line\":2,\"from_col\":0,\"to_col\":5,\"message\":\"slow\"}]'\n"), 0o755)
	os.WriteFile(filepath.Join(c.ChecksDir, "slow.yml"), []byte("extends: command\ncommand: ['./slow.sh']\n"), 0o644)
	var s PrereviewState
	start := time.Now()
	c.showDiff(&s, mdDiff())
	if time.Since(start) > 200*time.Millisecond {
		t.Errorf("showDiff blocked on the command for %s", time.Since(start))
	}
	if r := resultByID(t, s, "slow"); r.Status != checks.StatusRunning {
		t.Fatalf("slow %+v", r)
	}
	if s.CheckSummary().Running != 1 {
		t.Errorf("summary %+v", s.CheckSummary())
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		s, _ = c.ChecksUpdated(s, nil)
		if r := resultByID(t, s, "slow"); r.Status != checks.StatusRunning {
			if r.Status != checks.StatusFail || r.Findings[0].Message != "slow" {
				t.Errorf("slow result %+v", r)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("command result never landed")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestCopyCheckToUserFolder(t *testing.T) {
	c := checksController(t)
	var s PrereviewState
	c.showDiff(&s, mdDiff())
	s, err := c.CopyCheckToUserFolder(s, checksCtx("copyCheckToUserFolder", map[string]interface{}{"id": "weasel-words"}))
	if err != nil {
		t.Fatal(err)
	}
	r := resultByID(t, s, "weasel-words")
	if r.Source != checks.SourceUser || r.Path != filepath.Join(c.ChecksDir, "weasel-words.yml") {
		t.Errorf("copy not picked up: %+v", r)
	}
	if _, err := c.CopyCheckToUserFolder(s, checksCtx("copyCheckToUserFolder", map[string]interface{}{"id": "nope"})); err == nil {
		t.Error("copied a non-built-in")
	}
}

func TestEditorURL(t *testing.T) {
	p := "/home/u/my checks/a.yml"
	for _, tc := range []struct{ ed, tmpl, host, want string }{
		{EditorVSCode, "", "", "vscode://file/home/u/my%20checks/a.yml:3"},
		{EditorVSCode, "", "devbox", "vscode://vscode-remote/ssh-remote+devbox/home/u/my%20checks/a.yml:3"},
		{EditorCursor, "", "", "cursor://file/home/u/my%20checks/a.yml:3"},
		{EditorCursor, "", "devbox", "cursor://vscode-remote/ssh-remote+devbox/home/u/my%20checks/a.yml:3"},
		{EditorZed, "", "", "zed://file/home/u/my%20checks/a.yml:3"},
		{EditorZed, "", "devbox", "zed://ssh/devbox/home/u/my%20checks/a.yml:3"},
		{EditorCustom, "subl://open?url=file://{path}&line={line}&h={host}", "box", "subl://open?url=file:///home/u/my%20checks/a.yml&line=3&h=box"},
		{EditorCustom, "javascript:alert('{path}')", "", ""},
		{EditorCustom, "", "", ""},
		{"", "", "", ""},
	} {
		if got := string(editorURL(tc.ed, tc.tmpl, tc.host, p, 3)); got != tc.want {
			t.Errorf("%s/%q/%q = %q, want %q", tc.ed, tc.tmpl, tc.host, got, tc.want)
		}
	}
}

// TestShowDiffIsTheOnlyLiveAssignment guards applyChecks' choke point: the live
// view's diff may only change through showDiff (which re-applies checks) or
// showHistoricalDiff (which clears them). A new direct `state.CurrentDiff = …`
// would show findings for the previous text.
func TestShowDiffIsTheOnlyLiveAssignment(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool { return !strings.HasSuffix(fi.Name(), "_test.go") }, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, pkg := range pkgs {
		for _, f := range pkg.Files {
			for _, decl := range f.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Name.Name == "showDiff" || fn.Name.Name == "showHistoricalDiff" {
					continue
				}
				ast.Inspect(fn, func(n ast.Node) bool {
					as, ok := n.(*ast.AssignStmt)
					if !ok {
						return true
					}
					for _, lhs := range as.Lhs {
						if sel, ok := lhs.(*ast.SelectorExpr); ok && sel.Sel.Name == "CurrentDiff" {
							t.Errorf("%s: %s assigns CurrentDiff directly; use c.showDiff", fset.Position(as.Pos()), fn.Name.Name)
						}
					}
					return true
				})
			}
		}
	}
}

// Selecting a check on a rendered Markdown file shows its source (derived from the
// selection) and never touches the durable Preview/Raw preference; deselecting
// returns, and so does a file the selected check has no findings in.
func TestSelectCheck_RevealsWithoutTouchingPrefs(t *testing.T) {
	c := checksController(t)
	d := mdDiff()
	d.MarkdownBlocks = []gitdiff.MarkdownBlock{{HTML: "<p>x</p>", StartLine: 1, EndLine: 40}}
	var s PrereviewState
	s.SelectedFile = "doc.md"
	c.showDiff(&s, d)
	if !s.ShowRenderedMarkdown() {
		t.Fatal("precondition: rendered view")
	}
	s, _ = c.SelectCheck(s, checksCtx("selectCheck", map[string]interface{}{"id": "filler-intensifiers"}))
	if s.ShowRenderedMarkdown() || s.RawMarkdown {
		t.Errorf("want the source shown without the pref: rendered=%v raw=%v", s.ShowRenderedMarkdown(), s.RawMarkdown)
	}
	clean := &gitdiff.FileDiff{Path: "clean.md", Lines: []gitdiff.DiffLine{{NewNum: 1, Kind: "add", Content: "Fine."}},
		MarkdownBlocks: []gitdiff.MarkdownBlock{{HTML: "<p>Fine.</p>", StartLine: 1, EndLine: 1}}}
	other := s
	c.showDiff(&other, clean)
	if !other.ShowRenderedMarkdown() {
		t.Error("a file with no findings for the selected check must stay rendered")
	}
	if _, err := os.Stat(c.UIPrefsPath); err == nil {
		t.Error("selecting a check wrote the prefs file")
	}
	s, _ = c.SelectCheck(s, checksCtx("selectCheck", map[string]interface{}{"id": "filler-intensifiers"}))
	if !s.ShowRenderedMarkdown() {
		t.Error("deselecting should return to the rendered view")
	}
}

// A repo's Vale config can make vale start other tools (an XML Transform runs
// xsltproc on a repo stylesheet), so it never runs until the user trusts the repo.
func TestVale_RunsOnlyWhenTrusted(t *testing.T) {
	bin := t.TempDir()
	marker := filepath.Join(t.TempDir(), "vale-ran")
	fake := "#!/bin/sh\n: > '" + marker + "'\nprintf '%s' '{\"doc.md\":[{\"Check\":\"Demo.Weasel\",\"Message\":\"Avoid very.\",\"Severity\":\"warning\",\"Match\":\"very\",\"Line\":25,\"Span\":[9,12]}]}'\n"
	if err := os.WriteFile(filepath.Join(bin, "vale"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	c := checksController(t)
	os.WriteFile(filepath.Join(c.RepoPath, ".vale.ini"), []byte("StylesPath = styles\n"), 0o644)

	var s PrereviewState
	c.showDiff(&s, mdDiff())
	if !s.Checks.ValeTrust || !strings.Contains(s.Checks.ValeNotice, "trust") {
		t.Fatalf("untrusted config must offer trust: %+v", s.Checks)
	}
	if _, ok := s.checkResult(checks.ValeRuleID); ok {
		t.Error("vale has a row before the repo is trusted")
	}
	time.Sleep(100 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("vale ran for an untrusted repo")
	}

	s, _ = c.TrustVale(s, checksCtx("trustVale", nil))
	if p := loadUIPrefs(c.UIPrefsPath); len(p.TrustedVale) != 1 || p.TrustedVale[0] != c.RepoPath {
		t.Fatalf("trust not saved: %+v", p.TrustedVale)
	}
	if s.Checks.ValeTrust || s.Checks.ValeNotice != "" {
		t.Errorf("trusted repo still shows the notice: %+v", s.Checks)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		s, _ = c.ChecksUpdated(s, nil)
		if r, ok := s.checkResult(checks.ValeRuleID + ":Demo.Weasel"); ok {
			if r.Findings[0].Line != 25 || r.Findings[0].FromCol != 8 {
				t.Errorf("vale finding %+v", r.Findings[0])
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("trusted vale never reported: %+v", s.Checks.Results)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Trusted but not installed: the not-installed notice, no button.
	t.Setenv("PATH", t.TempDir())
	c.reloadChecksIfChanged()
	s, _ = c.ChecksUpdated(s, nil)
	if s.Checks.ValeTrust || !strings.Contains(s.Checks.ValeNotice, "isn't installed") {
		t.Errorf("no vale binary: %+v", s.Checks)
	}
}
