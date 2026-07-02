package review

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/livetemplate/livetemplate"
	"github.com/livetemplate/prereview/csv"
)

func hideController(t *testing.T) *PrereviewController {
	t.Helper()
	path := filepath.Join(t.TempDir(), "comments.csv")
	return &PrereviewController{CSVPath: path, CSVWriter: csv.NewWriter(path)}
}

func hideCtx(action, id string) *livetemplate.Context {
	return livetemplate.NewContext(context.TODO(), action, map[string]interface{}{"id": id})
}

// TestCommentHiddenFromView is the truth table for the single visibility rule:
// a resolved comment is hidden when the group is off OR it's individually
// hidden; a non-resolved comment is never hidden by this rule (Hidden is inert
// on it).
func TestCommentHiddenFromView(t *testing.T) {
	cases := []struct {
		resolved, hidden, showResolved bool
		wantHidden                     bool
	}{
		{false, false, false, false}, // open comment, group off → visible
		{false, false, true, false},  // open comment, group on → visible
		{false, true, true, false},   // Hidden inert on an open comment → visible
		{true, false, false, true},   // resolved, group off → hidden
		{true, false, true, false},   // resolved, group on, not re-hidden → visible
		{true, true, true, true},     // resolved, group on, re-hidden → hidden
		{true, true, false, true},    // resolved, group off, re-hidden → hidden
	}
	for _, tc := range cases {
		s := PrereviewState{ShowResolved: tc.showResolved}
		c := Comment{Resolved: tc.resolved, Hidden: tc.hidden}
		if got := s.commentHiddenFromView(c); got != tc.wantHidden {
			t.Errorf("resolved=%v hidden=%v show=%v: got hidden=%v want %v",
				tc.resolved, tc.hidden, tc.showResolved, got, tc.wantHidden)
		}
	}
}

// TestVisibleCommentsExcludesIndividuallyHidden guards the removed fast-path:
// with ShowResolved on, an individually-hidden resolved comment must still be
// excluded (the old `if ShowResolved { return all }` would have leaked it).
func TestVisibleCommentsExcludesIndividuallyHidden(t *testing.T) {
	s := PrereviewState{
		ShowResolved: true,
		Comments: []Comment{
			{ID: "open"},
			{ID: "res", Resolved: true},
			{ID: "res-hidden", Resolved: true, Hidden: true},
		},
	}
	vis := s.VisibleComments()
	if len(vis) != 2 {
		t.Fatalf("want 2 visible, got %d: %+v", len(vis), vis)
	}
	for _, c := range vis {
		if c.ID == "res-hidden" {
			t.Error("individually-hidden resolved comment leaked into VisibleComments")
		}
	}
	if n := s.HiddenResolvedCount(); n != 1 {
		t.Errorf("HiddenResolvedCount = %d, want 1", n)
	}
	// ResolvedCount counts both resolved comments (gates the Show-resolved toggle).
	if n := s.ResolvedCount(); n != 2 {
		t.Errorf("ResolvedCount = %d, want 2", n)
	}
}

// TestHideComment_PersistsAndRoundTrips verifies HideComment sets Hidden, writes
// the CSV, and the flag survives a reload from disk (the durability that makes
// per-comment hide outlast a relaunch, like Resolved).
func TestHideComment_PersistsAndRoundTrips(t *testing.T) {
	c := hideController(t)
	st := PrereviewState{Comments: []Comment{{ID: "a", Resolved: true}, {ID: "b"}}}

	st, err := c.HideComment(st, hideCtx("hideComment", "a"))
	if err != nil {
		t.Fatalf("HideComment: %v", err)
	}
	if !st.Comments[0].Hidden {
		t.Fatal("comment a should be Hidden after HideComment")
	}

	// Reload from disk → Hidden persisted.
	reloaded := c.loadCommentsFromDisk()
	var got *Comment
	for i := range reloaded {
		if reloaded[i].ID == "a" {
			got = &reloaded[i]
		}
	}
	if got == nil || !got.Hidden {
		t.Fatalf("Hidden did not round-trip through CSV: %+v", reloaded)
	}
}

// TestUnhideAllResolved clears every hidden flag in one action and persists.
func TestUnhideAllResolved(t *testing.T) {
	c := hideController(t)
	st := PrereviewState{Comments: []Comment{
		{ID: "a", Resolved: true, Hidden: true},
		{ID: "b", Resolved: true, Hidden: true},
		{ID: "c", Resolved: true},
	}}

	st, err := c.UnhideAllResolved(st, hideCtx("unhideAllResolved", ""))
	if err != nil {
		t.Fatalf("UnhideAllResolved: %v", err)
	}
	for _, cm := range st.Comments {
		if cm.Hidden {
			t.Errorf("comment %s still hidden after UnhideAllResolved", cm.ID)
		}
	}
	if st.HiddenResolvedCount() != 0 {
		t.Error("HiddenResolvedCount should be 0 after unhide-all")
	}
	// Persisted: reload shows no hidden flags.
	for _, cm := range c.loadCommentsFromDisk() {
		if cm.Hidden {
			t.Errorf("comment %s hidden on disk after unhide-all", cm.ID)
		}
	}
}

// TestHideComment_MissingID is the error path (mirrors ToggleResolved).
func TestHideComment_MissingID(t *testing.T) {
	c := hideController(t)
	st := PrereviewState{Comments: []Comment{{ID: "a", Resolved: true}}}
	if _, err := c.HideComment(st, hideCtx("hideComment", "nope")); err == nil {
		t.Error("expected error for unknown id")
	}
}
