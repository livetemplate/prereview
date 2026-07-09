package review

import (
	"reflect"
	"testing"

	"github.com/livetemplate/prereview/gitdiff"
)

// TestBlockDiffStatus pins issue #110: rendered-Markdown blocks that span
// added/changed source lines carry a highlight class, unchanged blocks don't,
// and the whole thing is suppressed in the cases where the code view drops its
// green too (version view, wholly-added/deleted file, raw/line view).
func TestBlockDiffStatus(t *testing.T) {
	// A modified .md file: 6 new-side lines, blocks split so each highlight
	// tier is exercised. A trailing pure-deletion row (NewNum==0) must be
	// ignored — it has no block on the new side.
	modified := &gitdiff.FileDiff{
		Path: "README.md",
		Lines: []gitdiff.DiffLine{
			{OldNum: 1, NewNum: 1, Kind: "ctx"},
			{NewNum: 2, Kind: "add"},
			{NewNum: 3, Kind: "add"},
			{OldNum: 4, NewNum: 4, Kind: "ctx"},
			{NewNum: 5, Kind: "add"},
			{OldNum: 6, NewNum: 6, Kind: "ctx"},
			{OldNum: 7, Kind: "del"}, // deletion — no new-side line
		},
		MarkdownBlocks: []gitdiff.MarkdownBlock{
			{StartLine: 1, EndLine: 1}, // ctx only        → omitted
			{StartLine: 2, EndLine: 3}, // all adds         → "added"
			{StartLine: 4, EndLine: 5}, // ctx + add        → "changed"
			{StartLine: 6, EndLine: 6}, // ctx only         → omitted
		},
	}

	cases := []struct {
		name  string
		state PrereviewState
		want  map[string]string
	}{
		{
			name:  "modified file → per-block added/changed",
			state: PrereviewState{CurrentDiff: modified},
			want:  map[string]string{"MB-2-3": "added", "MB-4-5": "changed"},
		},
		{
			name:  "viewing a historical version → suppressed",
			state: PrereviewState{CurrentDiff: modified, ViewingVersion: true},
			want:  nil,
		},
		{
			name:  "File view (Diff/File toggle on File) → suppressed",
			state: PrereviewState{CurrentDiff: modified, FileView: true},
			want:  nil,
		},
		{
			name: "wholly-added file → suppressed (parity with .code.pure-add)",
			state: PrereviewState{CurrentDiff: &gitdiff.FileDiff{
				Path:           "NEW.md",
				Note:           "file added",
				Lines:          []gitdiff.DiffLine{{NewNum: 1, Kind: "add"}, {NewNum: 2, Kind: "add"}},
				MarkdownBlocks: []gitdiff.MarkdownBlock{{StartLine: 1, EndLine: 2}},
			}},
			want: nil,
		},
		{
			name: "raw/line view (no rendered blocks) → suppressed",
			state: PrereviewState{CurrentDiff: &gitdiff.FileDiff{
				Path:  "README.md",
				Lines: modified.Lines,
			}},
			want: nil,
		},
		{
			name: "unchanged file (all ctx) → empty ⇒ nil",
			state: PrereviewState{CurrentDiff: &gitdiff.FileDiff{
				Path:           "README.md",
				Lines:          []gitdiff.DiffLine{{OldNum: 1, NewNum: 1, Kind: "ctx"}},
				MarkdownBlocks: []gitdiff.MarkdownBlock{{StartLine: 1, EndLine: 1}},
			}},
			want: nil,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := c.state.BlockDiffStatus()
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("BlockDiffStatus() = %v, want %v", got, c.want)
			}
		})
	}
}
