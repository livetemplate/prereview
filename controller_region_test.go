package main

import (
	"path/filepath"
	"testing"

	"github.com/livetemplate/prereview/csv"
)

// newRegionController returns a controller wired to a temp CSV store, in
// external mode, for exercising the --external region-annotation flow.
func newRegionController(t *testing.T) *PrereviewController {
	t.Helper()
	path := filepath.Join(t.TempDir(), "comments.csv")
	return &PrereviewController{
		ExternalMode: true,
		ProxyBaseURL: "http://127.0.0.1:9999/",
		TargetURL:    "http://localhost:8080",
		CSVPath:      path,
		CSVWriter:    csv.NewWriter(path),
	}
}

// TestToggleAnnotations_Flips pins the collapsed-by-default annotations
// sidebar toggle: a pure on/off flip of AnnoDrawerOpen.
func TestToggleAnnotations_Flips(t *testing.T) {
	c := &PrereviewController{}
	st := PrereviewState{}
	if st.AnnoDrawerOpen {
		t.Fatal("annotations drawer must be collapsed by default")
	}
	st, _ = c.ToggleAnnotations(st, regionCtx("toggleAnnotations", nil))
	if !st.AnnoDrawerOpen {
		t.Error("expected open after first toggle")
	}
	st, _ = c.ToggleAnnotations(st, regionCtx("toggleAnnotations", nil))
	if st.AnnoDrawerOpen {
		t.Error("expected collapsed after second toggle")
	}
}

// TestToggleFocusMode_Flips pins the desktop focus-mode reading toggle:
// off by default, then a pure on/off flip of FocusMode.
func TestToggleFocusMode_Flips(t *testing.T) {
	c := &PrereviewController{}
	st := PrereviewState{}
	if st.FocusMode {
		t.Fatal("focus mode must be off by default")
	}
	st, _ = c.ToggleFocusMode(st, regionCtx("toggleFocusMode", nil))
	if !st.FocusMode {
		t.Error("expected focus mode on after first toggle")
	}
	st, _ = c.ToggleFocusMode(st, regionCtx("toggleFocusMode", nil))
	if st.FocusMode {
		t.Error("expected focus mode off after second toggle")
	}
}

// TestFocusComment_SetsIDAndBumpsSeq pins that tapping "Locate" records the
// annotation to highlight and bumps FocusSeq so re-tapping the same id still
// re-triggers the client, plus the FocusedComment lookup.
func TestFocusComment_SetsIDAndBumpsSeq(t *testing.T) {
	c := &PrereviewController{}
	st := PrereviewState{Comments: []Comment{
		{ID: "x", Kind: commentKindRegion, URL: "/pricing", Area: Area{X: 0.1, Y: 0.4, W: 0.2, H: 0.1}},
	}}

	st, err := c.FocusComment(st, regionCtx("focusComment", map[string]interface{}{"id": "x"}))
	if err != nil {
		t.Fatalf("FocusComment: %v", err)
	}
	if st.FocusedCommentID != "x" || st.FocusSeq != 1 {
		t.Errorf("focus = (%q, seq %d), want (x, 1)", st.FocusedCommentID, st.FocusSeq)
	}
	if fc := st.FocusedComment(); fc == nil || fc.ID != "x" || fc.URL != "/pricing" {
		t.Errorf("FocusedComment() = %+v, want the x/pricing comment", fc)
	}
	// Re-tapping the same id still bumps the sequence (so the client re-fires).
	st, _ = c.FocusComment(st, regionCtx("focusComment", map[string]interface{}{"id": "x"}))
	if st.FocusSeq != 2 {
		t.Errorf("FocusSeq = %d after re-tap, want 2", st.FocusSeq)
	}
	// Missing id is rejected.
	if _, err := c.FocusComment(st, regionCtx("focusComment", nil)); err == nil {
		t.Error("expected error for missing id")
	}
}

// TestSetProxyURL_TracksPageAndDropsStaleComposer pins that the beacon's nav
// report updates CurrentURL and that navigating away abandons an in-progress
// region composer (it belonged to the page we just left).
func TestSetProxyURL_TracksPageAndDropsStaleComposer(t *testing.T) {
	c := newRegionController(t)

	st, err := c.SetProxyURL(PrereviewState{}, regionCtx("setProxyURL",
		map[string]interface{}{"url": "/pricing"}))
	if err != nil {
		t.Fatalf("SetProxyURL: %v", err)
	}
	if st.CurrentURL != "/pricing" {
		t.Errorf("CurrentURL = %q, want /pricing", st.CurrentURL)
	}

	// An open region composer is dropped when the page changes.
	st.CommentMode = commentKindRegion
	st.SelectionArea = Area{X: 0.1, Y: 0.1, W: 0.2, H: 0.2}
	st.DraftBody = "half-written"
	st, err = c.SetProxyURL(st, regionCtx("setProxyURL", map[string]interface{}{"url": "/dashboard"}))
	if err != nil {
		t.Fatalf("SetProxyURL nav: %v", err)
	}
	if st.CurrentURL != "/dashboard" || st.CommentMode != "" || !st.SelectionArea.Empty() || st.DraftBody != "" {
		t.Errorf("stale composer not cleared on nav: %+v", st)
	}
}

// TestSelectRegion_ValidatesAndArmsComposer pins that a drawn region needs a
// current page, rejects out-of-range rectangles, and arms the composer with
// the document-fraction rectangle on success.
func TestSelectRegion_ValidatesAndArmsComposer(t *testing.T) {
	c := newRegionController(t)

	// No current page → rejected.
	if _, err := c.SelectRegion(PrereviewState{}, regionCtx("selectRegion",
		map[string]interface{}{"x": 0.1, "y": 0.1, "w": 0.2, "h": 0.2})); err == nil {
		t.Error("expected error when no current page")
	}

	// Out-of-range rectangle → rejected.
	base := PrereviewState{CurrentURL: "/pricing"}
	if _, err := c.SelectRegion(base, regionCtx("selectRegion",
		map[string]interface{}{"x": 0.9, "y": 0.1, "w": 0.5, "h": 0.2})); err == nil {
		t.Error("expected error for x+w > 1")
	}

	// Valid → composer armed in region mode; capturing disarms the overlay
	// so the live page is interactive again and the composer is reachable.
	armed := PrereviewState{CurrentURL: "/pricing", RegionSelectArmed: true}
	st, err := c.SelectRegion(armed, regionCtx("selectRegion",
		map[string]interface{}{"x": 0.4, "y": 0.5, "w": 0.2, "h": 0.1}))
	if err != nil {
		t.Fatalf("SelectRegion: %v", err)
	}
	if st.CommentMode != commentKindRegion {
		t.Errorf("CommentMode = %q, want region", st.CommentMode)
	}
	if st.RegionSelectArmed {
		t.Errorf("capturing a region should disarm the overlay")
	}
	want := Area{X: 0.4, Y: 0.5, W: 0.2, H: 0.1}
	if st.SelectionArea != want {
		t.Errorf("SelectionArea = %+v, want %+v", st.SelectionArea, want)
	}
}

// TestAddRegionComment_PersistsAndRoundTrips pins the full happy path: a
// region selection + body becomes a kind=region comment anchored to URL +
// rectangle, written to CSV and reloadable with every field intact.
func TestAddRegionComment_PersistsAndRoundTrips(t *testing.T) {
	c := newRegionController(t)

	st := PrereviewState{CurrentURL: "/pricing"}
	st, err := c.SelectRegion(st, regionCtx("selectRegion",
		map[string]interface{}{"x": 0.4, "y": 0.5, "w": 0.2, "h": 0.1}))
	if err != nil {
		t.Fatalf("SelectRegion: %v", err)
	}
	st, err = c.AddComment(st, regionCtx("addComment", map[string]interface{}{"body": "CTA too low contrast"}))
	if err != nil {
		t.Fatalf("AddComment: %v", err)
	}
	// Composer reset after save.
	if st.CommentMode != "" || !st.SelectionArea.Empty() || st.DraftBody != "" {
		t.Errorf("composer not reset after save: %+v", st)
	}
	if len(st.Comments) != 1 {
		t.Fatalf("got %d comments, want 1", len(st.Comments))
	}
	cm := st.Comments[0]
	if !cm.IsRegionLevel() || cm.URL != "/pricing" || cm.Body != "CTA too low contrast" {
		t.Errorf("comment wrong: %+v", cm)
	}
	if cm.Area != (Area{X: 0.4, Y: 0.5, W: 0.2, H: 0.1}) {
		t.Errorf("area wrong: %+v", cm.Area)
	}

	// Reload from disk: every region field survives the CSV round-trip.
	reloaded := c.loadCommentsFromDisk()
	if len(reloaded) != 1 {
		t.Fatalf("reloaded %d, want 1", len(reloaded))
	}
	if r := reloaded[0]; r.Kind != commentKindRegion || r.URL != "/pricing" || r.Area != cm.Area {
		t.Errorf("round-trip lost data: %+v", r)
	}
}

// TestRegionComments_ScopedByCurrentURL pins that the per-page overlay set
// (RegionComments) is scoped to the page in the iframe, while the sidebar
// set (AllRegionComments) spans every page.
func TestRegionComments_ScopedByCurrentURL(t *testing.T) {
	st := PrereviewState{
		ExternalMode: true,
		CurrentURL:   "/pricing",
		Comments: []Comment{
			{ID: "a", Kind: commentKindRegion, URL: "/pricing", Area: Area{X: 0.1, Y: 0.1, W: 0.1, H: 0.1}},
			{ID: "b", Kind: commentKindRegion, URL: "/dashboard", Area: Area{X: 0.2, Y: 0.2, W: 0.1, H: 0.1}},
			{ID: "c", Kind: commentKindLine, File: "main.go", FromLine: 1, ToLine: 1},
		},
	}
	page := st.RegionComments()
	if len(page) != 1 || page[0].ID != "a" {
		t.Errorf("RegionComments scoped to current page wrong: %+v", page)
	}
	all := st.AllRegionComments()
	if len(all) != 2 {
		t.Errorf("AllRegionComments = %d, want 2 (both pages, no line comment)", len(all))
	}
}

// TestEditRegionComment_UpdatesInPlace pins that editing a saved region
// comment re-arms the composer on its page (Kind + rectangle + URL) and a
// body rewrite updates in place without appending a duplicate.
func TestEditRegionComment_UpdatesInPlace(t *testing.T) {
	c := newRegionController(t)

	// Seed one saved region comment.
	st := PrereviewState{CurrentURL: "/pricing"}
	st, _ = c.SelectRegion(st, regionCtx("selectRegion",
		map[string]interface{}{"x": 0.4, "y": 0.5, "w": 0.2, "h": 0.1}))
	st, _ = c.AddComment(st, regionCtx("addComment", map[string]interface{}{"body": "original"}))
	id := st.Comments[0].ID

	// Edit it from a DIFFERENT current page: EditComment must restore region
	// mode, the saved rectangle, and scope back to the comment's page.
	st.CurrentURL = "/elsewhere"
	st, err := c.EditComment(st, regionCtx("editComment", map[string]interface{}{"id": id}))
	if err != nil {
		t.Fatalf("EditComment: %v", err)
	}
	if st.CommentMode != commentKindRegion || st.CurrentURL != "/pricing" ||
		st.EditingCommentID != id || st.SelectionArea != (Area{X: 0.4, Y: 0.5, W: 0.2, H: 0.1}) {
		t.Errorf("edit did not restore region composer: %+v", st)
	}

	// Rewrite the body; the save updates in place (saved rectangle preserved).
	st, err = c.AddComment(st, regionCtx("addComment", map[string]interface{}{"body": "revised"}))
	if err != nil {
		t.Fatalf("AddComment edit: %v", err)
	}
	if len(st.Comments) != 1 {
		t.Fatalf("edit appended a duplicate: %d comments", len(st.Comments))
	}
	if got := st.Comments[0]; got.Body != "revised" || got.URL != "/pricing" ||
		got.Area != (Area{X: 0.4, Y: 0.5, W: 0.2, H: 0.1}) {
		t.Errorf("edit not applied in place: %+v", got)
	}
}
