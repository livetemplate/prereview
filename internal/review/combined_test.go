package review

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/livetemplate/prereview/csv"
)

// newCombinedController is a #114 combined session: a repo review that also
// fronts a live site, so the proxy fields are set and ExternalMode is not.
func newCombinedController(t *testing.T) *PrereviewController {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	csvPath := filepath.Join(dir, ".prereview", "comments.csv")
	if err := os.MkdirAll(filepath.Dir(csvPath), 0o755); err != nil {
		t.Fatal(err)
	}
	return &PrereviewController{
		RepoPath:     dir,
		NoGit:        true,
		ProxyBaseURL: "http://127.0.0.1:9999/",
		TargetURL:    "http://localhost:3000",
		CSVPath:      csvPath,
		CSVWriter:    csv.NewWriter(csvPath),
	}
}

// TestMount_CombinedSetsLiveSurface pins decision 10: Mount mirrors the live
// surface from the controller on every connect, keeps the repo path (files
// load), and clears a persisted ExternalMode from an earlier live-only launch.
func TestMount_CombinedSetsLiveSurface(t *testing.T) {
	c := newCombinedController(t)
	st, err := c.Mount(PrereviewState{ExternalMode: true, ProxyBaseURL: "http://127.0.0.1:1/", LiveView: true}, nil)
	if err != nil {
		t.Fatalf("Mount: %v", err)
	}
	if st.ExternalMode {
		t.Error("ExternalMode must stay false in a combined session (it means live-only)")
	}
	if !st.LiveAvailable() {
		t.Error("LiveAvailable must be true when the controller has a proxy")
	}
	if st.ProxyBaseURL != c.ProxyBaseURL || st.TargetURL != c.TargetURL {
		t.Errorf("proxy fields = %q/%q, want the controller's %q/%q", st.ProxyBaseURL, st.TargetURL, c.ProxyBaseURL, c.TargetURL)
	}
	if !st.LiveView {
		t.Error("a persisted LiveView must survive a reconnect")
	}
	if len(st.Files) != 1 || st.SelectedFile != "a.md" {
		t.Errorf("the repo path must still run: files=%v selected=%q", st.Files, st.SelectedFile)
	}
}

// TestMount_RepoClearsStaleLive pins the other half of decision 10: a plain repo
// review drops a persisted live surface rather than rendering an iframe at a
// proxy that no longer exists.
func TestMount_RepoClearsStaleLive(t *testing.T) {
	c := newCombinedController(t)
	c.ProxyBaseURL, c.TargetURL = "", ""
	st, err := c.Mount(PrereviewState{ExternalMode: true, ProxyBaseURL: "http://127.0.0.1:1/", LiveView: true}, nil)
	if err != nil {
		t.Fatalf("Mount: %v", err)
	}
	if st.ExternalMode || st.LiveAvailable() || st.LiveView || st.ProxyBaseURL != "" {
		t.Errorf("stale live state survived: external=%v available=%v view=%v proxy=%q",
			st.ExternalMode, st.LiveAvailable(), st.LiveView, st.ProxyBaseURL)
	}
}

// TestShowLive pins decision 5's switch: picking a surface closes a full-page
// view and disarms the region overlay (the HTML preview and the live stage share
// RegionSelectArmed); a session with nothing to switch to ignores it.
func TestShowLive(t *testing.T) {
	c := &PrereviewController{}
	st := PrereviewState{ProxyBaseURL: "http://127.0.0.1:9999/", ShowAllComments: true, RegionSelectArmed: true}
	st, _ = c.ToggleLiveView(st, regionCtx("toggleLiveView", nil))
	if !st.LiveView || st.ShowAllComments || st.RegionSelectArmed || !st.LiveShowing() {
		t.Errorf("to Live: view=%v allComments=%v armed=%v showing=%v", st.LiveView, st.ShowAllComments, st.RegionSelectArmed, st.LiveShowing())
	}
	st.RegionSelectArmed = true
	st, _ = c.ToggleLiveView(st, regionCtx("toggleLiveView", nil))
	if st.LiveView || st.RegionSelectArmed || st.LiveShowing() {
		t.Errorf("toggle to Files: view=%v armed=%v", st.LiveView, st.RegionSelectArmed)
	}
	// All comments open over the live site: the toggle reads off (LiveShowing is
	// false), so a tap brings the site back rather than switching to Files.
	st = PrereviewState{ProxyBaseURL: "http://127.0.0.1:9999/", LiveView: true, ShowAllComments: true}
	if st, _ = c.ToggleLiveView(st, regionCtx("toggleLiveView", nil)); !st.LiveShowing() {
		t.Errorf("toggle from All comments over Live: view=%v allComments=%v", st.LiveView, st.ShowAllComments)
	}

	for name, s := range map[string]PrereviewState{
		"repo":      {},
		"live-only": {ExternalMode: true, ProxyBaseURL: "http://127.0.0.1:9999/"},
	} {
		got, _ := c.ToggleLiveView(s, regionCtx("toggleLiveView", nil))
		if got.LiveView {
			t.Errorf("%s: LiveView set in a session with nothing to switch to", name)
		}
	}
}

// TestKeyBindings_LiveOnly pins that "l" exists only where there is a live site.
func TestKeyBindings_LiveOnly(t *testing.T) {
	has := func(s PrereviewState) bool {
		for _, b := range s.KeyBindings() {
			if b.Action == "toggleLiveView" {
				return true
			}
		}
		return false
	}
	if has(PrereviewState{}) || has(PrereviewState{AgentMode: true}) {
		t.Error("a repo review lists the live-site shortcut")
	}
	if !has(PrereviewState{ProxyBaseURL: "http://127.0.0.1:9999/"}) {
		t.Error("a combined review lacks the live-site shortcut")
	}
}

// liveComment is a live-site annotation on page url.
func liveComment(id, url string) Comment {
	c := regionComment(id, "fix "+id)
	c.URL = url
	return c
}

// TestQueue_RegionBypassesThisFile pins decision 7: the Queue's "This file" view
// still shows a live-site annotation, located by its page URL.
func TestQueue_RegionBypassesThisFile(t *testing.T) {
	st := PrereviewState{
		ProxyBaseURL: "http://127.0.0.1:9999/",
		SelectedFile: "a.md",
		Comments: []Comment{
			{ID: "l1", File: "a.md", FromLine: 1, ToLine: 1, Side: "new", Body: "line"},
			{ID: "l2", File: "b.md", FromLine: 1, ToLine: 1, Side: "new", Body: "other file"},
			liveComment("r1", "/pricing"),
		},
	}
	got := map[string]string{}
	for _, it := range st.QueueItems() {
		got[it.ID] = it.File
	}
	if _, ok := got["l2"]; ok || got["l1"] != "a.md" || got["r1"] != "/pricing" {
		t.Errorf("This-file queue = %v, want l1 (a.md) and r1 (/pricing) only", got)
	}
}

// TestJump_RegionShowsLive pins decision 7's jumps: All comments / the Queue and
// the n/p stepper land a region item on the live site, focused, with the file
// still selected for the way back.
func TestJump_RegionShowsLive(t *testing.T) {
	c := newCombinedController(t)
	base := PrereviewState{ProxyBaseURL: "http://127.0.0.1:9999/", SelectedFile: "a.md", ShowAllComments: true,
		Comments: []Comment{liveComment("r1", "/pricing")}}

	st, err := c.JumpToComment(base, regionCtx("jumpToComment", map[string]any{"id": "r1"}))
	if err != nil {
		t.Fatalf("JumpToComment: %v", err)
	}
	if !st.LiveShowing() || st.FocusedCommentID != "r1" || st.FocusSeq != 1 || st.SelectedFile != "a.md" {
		t.Errorf("jump: showing=%v focused=%q seq=%d file=%q", st.LiveShowing(), st.FocusedCommentID, st.FocusSeq, st.SelectedFile)
	}

	base.ShowAllComments = false
	st, err = c.NavNext(base, regionCtx("navNext", nil))
	if err != nil {
		t.Fatalf("NavNext: %v", err)
	}
	if !st.LiveShowing() || st.FocusedCommentID != "r1" || st.SelectedFile != "a.md" || st.NavAt == nil {
		t.Errorf("step: showing=%v focused=%q file=%q", st.LiveShowing(), st.FocusedCommentID, st.SelectedFile)
	}
}

// TestOpenFile_LeavesLive pins that a file opened by any path (here j/k and a
// search jump) shows on the Files surface instead of loading hidden behind the
// live site.
func TestOpenFile_LeavesLive(t *testing.T) {
	c := newCombinedController(t)
	st, err := c.Mount(PrereviewState{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	st.LiveView = true
	if st, err = c.NextFile(st, regionCtx("nextFile", nil)); err != nil || st.LiveView {
		t.Errorf("j left the live site showing (err %v)", err)
	}
	st.LiveView = true
	if st, err = c.JumpToSearchResult(st, regionCtx("jumpToSearchResult", map[string]any{"file": "a.md"})); err != nil || st.LiveView {
		t.Errorf("a search jump left the live site showing (err %v)", err)
	}
}

// TestOpenFileComment_FromLive pins that "c" on the live site shows the file its
// composer belongs to, instead of opening it out of sight.
func TestOpenFileComment_FromLive(t *testing.T) {
	c := &PrereviewController{}
	st := PrereviewState{ProxyBaseURL: "http://127.0.0.1:9999/", LiveView: true, SelectedFile: "a.md"}
	st, err := c.OpenFileComment(st, regionCtx("openFileComment", nil))
	if err != nil || st.LiveView || st.CommentMode != commentKindFile {
		t.Errorf("c from Live: err=%v liveView=%v mode=%q", err, st.LiveView, st.CommentMode)
	}
}
