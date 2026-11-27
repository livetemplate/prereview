package review

import (
	"strings"
	"testing"
)

// listFixture is navFixture opened on the Annotations page, plus a question set.
func listFixture() PrereviewState {
	s := navFixture()
	s.ShowAllComments = true
	s.QuestionSets = []QuestionSet{{ID: "qs-a6", File: "a.go", FromLine: 6, ToLine: 6,
		Steps: []QuestionStep{{ID: "s1", Prompt: "Keep the cache?\nIt costs memory."}}}}
	return s
}

func rowKeys(rows []AnnListRow) string {
	var keys []string
	for _, r := range rows {
		keys = append(keys, r.Key)
	}
	return strings.Join(keys, " ")
}

// The page lists every kind in review order, as the stepper walks them, plus
// the agent requests the stepper skips. Comments keep their bare id as key.
func TestAnnotationList_EveryKindInReviewOrder(t *testing.T) {
	rows := listFixture().AnnotationList()
	if got, want := rowKeys(rows), "c-bfile c-req c-b5 s:s-b5 s:s-a2 z:q-a4 q:qs-a6 c-a9"; got != want {
		t.Errorf("rows = %q, want %q", got, want)
	}
	for _, r := range rows {
		switch r.Key {
		case "q:qs-a6":
			if r.Label != "Question" || r.Span != "L6" || r.Text != "Keep the cache?" {
				t.Errorf("question row = %+v", r)
			}
		case "c-b5":
			if r.Comment == nil || r.Comment.ID != "c-b5" {
				t.Errorf("a comment row must carry its comment, got %+v", r)
			}
		}
	}
}

// The page's own filter: Kinds drop rows, All adds settled ones, None empties
// it, and a file's filter does not reach the page.
func TestAnnotationList_OwnFilter(t *testing.T) {
	s := listFixture()
	decide(&s, "s-a2", verdictAccept)
	s.AnnFilters = map[string]AnnFilter{"a.go": {Show: annShowNone}}
	if got := rowKeys(s.AnnotationList()); !strings.Contains(got, "c-a9") || strings.Contains(got, "s-a2") {
		t.Errorf("open list = %q: a.go's filter must not apply, and a decided suggestion waits for All", got)
	}

	s.AnnFilters = map[string]AnnFilter{annListKey: {Show: annShowAll, Off: []string{navComment, navQuiz}}}
	if got, want := rowKeys(s.AnnotationList()), "s:s-b5 s:s-a2 q:qs-a6"; got != want {
		t.Errorf("All without comments and quiz = %q, want %q", got, want)
	}
	if n := s.UnresolvedListed(); n != 0 {
		t.Errorf("with Comments off, Resolve all must resolve nothing, got %d", n)
	}

	s.AnnFilters = map[string]AnnFilter{annListKey: {Show: annShowNone}}
	if rows := s.AnnotationList(); rows != nil {
		t.Errorf("None lists nothing, got %q", rowKeys(rows))
	}
	s.ShowAllComments = false
	s.AnnFilters = nil
	if rows := s.AnnotationList(); rows != nil {
		t.Errorf("closed page computes nothing, got %q", rowKeys(rows))
	}
}

// The Live site chip lists only the live site's boxes, so no other kind.
func TestAnnotationList_LiveSiteChip(t *testing.T) {
	s := listFixture()
	s.Comments = append(s.Comments, Comment{ID: "box", Kind: commentKindRegion, URL: "/"})
	s.CommentListFilter = CommentFilterAnnotations
	if got := rowKeys(s.AnnotationList()); got != "box" {
		t.Errorf("Live site chip lists %q, want only box", got)
	}
}
