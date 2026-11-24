package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/livetemplate/prereview/gitdiff"
)

// TestFileTreeUpdateOneRow: a change to one file's row (its comment count) updates
// that row and its folder's count, not the drawer. The drawer used to be one
// HTML string rendered in Go, so any count change resent all of it (about 1.3 KB
// per file).
func TestFileTreeUpdateOneRow(t *testing.T) {
	before := goBase()
	before.Files = nil
	for _, dir := range []string{"api", "store", "web"} {
		for i := range 10 {
			before.Files = append(before.Files, gitdiff.FileEntry{Path: fmt.Sprintf("%s/f%02d.go", dir, i), Status: "M", Added: 1})
		}
	}
	after := before
	after.Files = append([]gitdiff.FileEntry(nil), before.Files...)
	after.Files[14].CommentCount = 1 // store/f04.go

	payload := renderUpdate(t, before, after)
	if strings.Contains(payload, "api/f00.go") || strings.Contains(payload, "web/f09.go") {
		t.Errorf("resent other files' rows (%d bytes):\n%.800s", len(payload), payload)
	}
	if len(payload) > 1500 {
		t.Errorf("one count change sent %d bytes, want under 1,500:\n%.800s", len(payload), payload)
	}
}
