package review

import "testing"

func TestComment_LineSpan(t *testing.T) {
	cases := []struct {
		name string
		c    Comment
		want string
	}{
		{"single line", Comment{Kind: commentKindLine, FromLine: 42, ToLine: 42}, "L42"},
		{"line range", Comment{Kind: commentKindLine, FromLine: 42, ToLine: 48}, "L42-L48"},
		{"legacy empty kind", Comment{FromLine: 7, ToLine: 7}, "L7"},
		{"file", Comment{Kind: commentKindFile}, "file"},
		{"area", Comment{Kind: commentKindArea}, "area"},
		{"region", Comment{Kind: commentKindRegion}, "region"},
		{"text same line", Comment{Kind: commentKindText, FromLine: 42, ToLine: 42, FromCol: 6, ToCol: 12}, "L42:6-12"},
		{"text multi line", Comment{Kind: commentKindText, FromLine: 42, ToLine: 48, FromCol: 6, ToCol: 3}, "L42:6-L48:3"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.c.LineSpan(); got != tc.want {
				t.Errorf("LineSpan() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestComment_KindPredicates(t *testing.T) {
	text := Comment{Kind: commentKindText, FromCol: 6, ToCol: 12}
	if !text.IsTextLevel() {
		t.Error("IsTextLevel() = false for kind=text")
	}
	// A text comment is a character-anchored comment, not file/area/region —
	// those predicates gate the "no drift" relocate skip, which text must NOT
	// hit (text comments drift with their lines).
	if text.IsFileLevel() || text.IsAreaLevel() || text.IsRegionLevel() {
		t.Errorf("text comment misclassified: file=%v area=%v region=%v",
			text.IsFileLevel(), text.IsAreaLevel(), text.IsRegionLevel())
	}
	line := Comment{Kind: commentKindLine, FromLine: 1, ToLine: 1}
	if line.IsTextLevel() {
		t.Error("IsTextLevel() = true for kind=line")
	}
}
