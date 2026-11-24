package review

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/livetemplate/livetemplate"
	"github.com/livetemplate/prereview/csv"
	"github.com/livetemplate/prereview/gitdiff"
)

// TestAnnotationColors: live annotations are coloured by their number on their
// page, an HTML file's comments by their order in the file, and the palette
// wraps; other comments get none.
func TestAnnotationColors(t *testing.T) {
	var cs []Comment
	add := func(id string, c Comment) { c.ID = id; cs = append(cs, c) }
	add("a1", Comment{Kind: commentKindRegion, URL: "http://x/a"})
	add("b1", Comment{Kind: commentKindRegion, URL: "http://x/b"})
	add("a2", Comment{Kind: commentKindRegion, URL: "http://x/a"})
	add("go", Comment{File: "main.go", FromLine: 1, ToLine: 1})
	add("hf", Comment{File: "index.html", Kind: commentKindFile})
	for _, id := range []string{"h1", "h2", "h3", "h4", "h5", "h6", "h7"} {
		add(id, Comment{File: "index.html", FromLine: 4, ToLine: 4})
	}
	got := PrereviewState{Comments: cs}.AnnotationColors()
	want := map[string]string{
		"a1": "ann-c1", "a2": "ann-c2", "b1": "ann-c1",
		"h1": "ann-c1", "h2": "ann-c2", "h6": "ann-c6", "h7": "ann-c1",
	}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("%s = %q, want %q", id, got[id], w)
		}
	}
	for _, id := range []string{"go", "hf"} {
		if c, ok := got[id]; ok {
			t.Errorf("%s has colour %q, want none", id, c)
		}
	}
}

func previewState() PrereviewState {
	return PrereviewState{
		SelectedFile: "index.html",
		CurrentDiff: &gitdiff.FileDiff{
			Path:       "index.html",
			Lines:      []gitdiff.DiffLine{{NewNum: 1, Kind: "add", Content: "<section>Top</section>"}, {NewNum: 2, Kind: "add", Content: "<section>Bottom</section>"}},
			HTMLBlocks: []gitdiff.HTMLBlock{{StartLine: 1, EndLine: 1}, {StartLine: 2, EndLine: 2}},
		},
	}
}

// TestHTMLMarkers_Boxes: a block's marker carries each comment's drawn box in
// its colour, and Unboxed when a comment there has none.
func TestHTMLMarkers_Boxes(t *testing.T) {
	s := previewState()
	box := Area{X: 0.1, Y: 0.1, W: 0.2, H: 0.05}
	s.Comments = []Comment{
		{ID: "c1", File: "index.html", FromLine: 1, ToLine: 1, Side: "new", Anchor: CommentAnchor{Text: "x", Box: box}},
		{ID: "c2", File: "index.html", FromLine: 1, ToLine: 1, Side: "new", Anchor: CommentAnchor{Text: "x", Box: box}},
		{ID: "c3", File: "index.html", FromLine: 2, ToLine: 2, Side: "new", Anchor: CommentAnchor{Text: "y"}},
	}
	ms := s.HTMLMarkers()
	if len(ms) != 2 {
		t.Fatalf("%d markers, want 2: %+v", len(ms), ms)
	}
	top, bottom := ms[0], ms[1]
	if top.Total != 2 || top.Unboxed || len(top.Boxes) != 2 {
		t.Errorf("top marker = %+v, want 2 comments, 2 boxes, none unboxed", top)
	}
	if len(top.Boxes) == 2 && (top.Boxes[0].Color != "ann-c1" || top.Boxes[1].Color != "ann-c2" || top.Boxes[0].Area != box) {
		t.Errorf("top boxes = %+v, want ann-c1 then ann-c2 at %+v", top.Boxes, box)
	}
	if !bottom.Unboxed || len(bottom.Boxes) != 0 {
		t.Errorf("bottom marker = %+v, want unboxed with no boxes", bottom)
	}
}

func boxCtx(action string, data map[string]interface{}) *livetemplate.Context {
	return livetemplate.NewContext(context.TODO(), action, data)
}

// TestPreviewComment_KeepsItsBox: a box drawn on the preview is saved in the
// comment's anchor (and survives the CSV), and an edit that draws no new box
// keeps it.
func TestPreviewComment_KeepsItsBox(t *testing.T) {
	csvPath := filepath.Join(t.TempDir(), "comments.csv")
	c := &PrereviewController{CSVPath: csvPath, CSVWriter: csv.NewWriter(csvPath)}
	s := previewState()
	s.RegionSelectArmed = true
	s, err := c.SelectBlock(s, boxCtx("selectBlock", map[string]interface{}{"from": 1, "to": 1, "bx": 0.1, "by": 0.2, "bw": 0.3, "bh": 0.05}))
	if err != nil {
		t.Fatal(err)
	}
	s, err = c.AddComment(s, boxCtx("addComment", map[string]interface{}{"body": "too small"}))
	if err != nil {
		t.Fatal(err)
	}
	want := Area{X: 0.1, Y: 0.2, W: 0.3, H: 0.05}
	if len(s.Comments) != 1 || s.Comments[0].Anchor.Box != want {
		t.Fatalf("saved comments = %+v, want one with box %+v", s.Comments, want)
	}
	if !s.SelectionArea.Empty() {
		t.Errorf("the drawn box outlived the save: %+v", s.SelectionArea)
	}
	rows, err := csv.Read(csvPath)
	if err != nil || len(rows) != 1 {
		t.Fatalf("read CSV: %v %+v", err, rows)
	}
	if got := parseAnchor(rows[0].Anchor); got.Box != want {
		t.Errorf("CSV anchor box = %+v, want %+v", got.Box, want)
	}

	// Edit the body only: the box stays.
	s, err = c.EditComment(s, boxCtx("editComment", map[string]interface{}{"id": s.Comments[0].ID}))
	if err != nil {
		t.Fatal(err)
	}
	s, err = c.AddComment(s, boxCtx("addComment", map[string]interface{}{"body": "far too small"}))
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Comments[0]; got.Body != "far too small" || got.Anchor.Box != want {
		t.Errorf("after an edit: %+v, want the new body and box %+v", got, want)
	}
}

// TestDrawnBox rejects a missing or out-of-range box.
func TestDrawnBox(t *testing.T) {
	for _, tc := range []struct {
		name string
		data map[string]interface{}
		ok   bool
	}{
		{"none", map[string]interface{}{"from": 1}, false},
		{"inside", map[string]interface{}{"bx": 0.5, "by": 0.5, "bw": 0.5, "bh": 0.5}, true},
		{"past the edge", map[string]interface{}{"bx": 0.8, "by": 0, "bw": 0.5, "bh": 0.1}, false},
		{"negative", map[string]interface{}{"bx": -0.1, "by": 0, "bw": 0.5, "bh": 0.1}, false},
	} {
		if got := !drawnBox(boxCtx("selectBlock", tc.data)).Empty(); got != tc.ok {
			t.Errorf("%s: got a box = %v, want %v", tc.name, got, tc.ok)
		}
	}
}
