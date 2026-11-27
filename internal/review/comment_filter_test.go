package review

import "testing"

// TestCommentListFilterAnnotations: the Annotations filter lists only the live
// site's (region) comments, counts them on its button, and Resolve all then
// resolves only what it lists.
func TestCommentListFilterAnnotations(t *testing.T) {
	s := PrereviewState{
		ShowAllComments: true,
		Comments: []Comment{
			{ID: "line", File: "app.go", Kind: "line", FromLine: 1, ToLine: 1},
			{ID: "box1", Kind: commentKindRegion, URL: "/"},
			{ID: "box2", Kind: commentKindRegion, URL: "/about"},
			{ID: "box-done", Kind: commentKindRegion, URL: "/", Resolved: true},
		},
	}
	if n := len(listedComments(s)); n != 3 {
		t.Fatalf("no filter: %d listed, want 3 (resolved hidden)", n)
	}
	if n := s.AnnotationsListed(); n != 2 {
		t.Errorf("AnnotationsListed = %d, want 2", n)
	}
	if n := s.UnresolvedListed(); n != 3 {
		t.Errorf("no filter: UnresolvedListed = %d, want 3", n)
	}

	s.CommentListFilter = CommentFilterAnnotations
	var ids []string
	for _, c := range listedComments(s) {
		ids = append(ids, c.ID)
	}
	if len(ids) != 2 || ids[0] != "box1" || ids[1] != "box2" {
		t.Errorf("annotations filter lists %v, want [box1 box2]", ids)
	}
	if n := s.UnresolvedListed(); n != 2 {
		t.Errorf("annotations filter: UnresolvedListed = %d, want 2 (Resolve all must not reach the line comment)", n)
	}
}

// listedComments is the comments the Annotations page lists for s.
func listedComments(s PrereviewState) []Comment {
	s.ShowAllComments = true
	var out []Comment
	for _, r := range s.AnnotationList() {
		if r.Comment != nil {
			out = append(out, *r.Comment)
		}
	}
	return out
}
