package main

import (
	"testing"

	"github.com/livetemplate/prereview/internal/review"
)

func TestCommentLocation(t *testing.T) {
	tests := []struct {
		name string
		c    review.StreamComment
		want string
	}{
		{"single line", review.StreamComment{File: "a.go", FromLine: 5, ToLine: 5}, "a.go:5"},
		{"line range", review.StreamComment{File: "a.go", FromLine: 5, ToLine: 9}, "a.go:5-9"},
		{"whole file", review.StreamComment{Kind: "file", File: "README.md"}, "README.md"},
		{"region", review.StreamComment{Kind: "region", URL: "/dashboard"}, "/dashboard"},
	}
	for _, tt := range tests {
		if got := commentLocation(tt.c); got != tt.want {
			t.Errorf("%s: commentLocation = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestFirstLine(t *testing.T) {
	if got := firstLine("one line"); got != "one line" {
		t.Errorf("single line = %q", got)
	}
	if got := firstLine("first\nsecond\nthird"); got != "first …" {
		t.Errorf("multi line = %q, want %q", got, "first …")
	}
	if got := firstLine("  spaced  "); got != "spaced" {
		t.Errorf("trim = %q", got)
	}
}
