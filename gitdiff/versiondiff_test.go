package gitdiff

import "testing"

func kindsOf(fd *FileDiff) map[string]int {
	m := map[string]int{}
	for _, l := range fd.Lines {
		m[l.Kind]++
	}
	return m
}

func TestRenderBytesAsFile_AllContext(t *testing.T) {
	fd := RenderBytesAsFile("a.txt", []byte("one\ntwo\nthree\n"))
	if fd.IsBinary || fd.Note != "" {
		t.Fatalf("unexpected: binary=%v note=%q", fd.IsBinary, fd.Note)
	}
	if len(fd.Lines) != 3 {
		t.Fatalf("want 3 lines, got %d", len(fd.Lines))
	}
	for i, l := range fd.Lines {
		if l.Kind != "ctx" {
			t.Errorf("line %d kind=%q, want ctx", i, l.Kind)
		}
	}
}

func TestRenderBytesAsFile_Binary(t *testing.T) {
	fd := RenderBytesAsFile("img.png", []byte{0x89, 0x00, 0x01})
	if !fd.IsBinary {
		t.Fatal("NUL byte should be detected as binary")
	}
}

func TestDiffContents_AddDelCtx(t *testing.T) {
	old := []byte("alpha\nbeta\ngamma\n")
	new := []byte("alpha\nBETA\ngamma\ndelta\n")
	fd := DiffContents("f.txt", old, new)
	if fd.IsBinary {
		t.Fatal("unexpected binary")
	}
	k := kindsOf(fd)
	// "beta"→"BETA" is a del+add; "delta" is a pure add; alpha/gamma are context.
	if k["del"] < 1 {
		t.Errorf("expected a deletion (beta), kinds=%v", k)
	}
	if k["add"] < 2 {
		t.Errorf("expected additions (BETA + delta), kinds=%v", k)
	}
	if k["ctx"] < 1 {
		t.Errorf("expected context lines, kinds=%v", k)
	}
	// The new content must be present in the rendered add lines.
	var sawDelta, sawBETA bool
	for _, l := range fd.Lines {
		if l.Kind == "add" && l.Content == "delta" {
			sawDelta = true
		}
		if l.Kind == "add" && l.Content == "BETA" {
			sawBETA = true
		}
	}
	if !sawDelta || !sawBETA {
		t.Errorf("added lines missing: sawDelta=%v sawBETA=%v", sawDelta, sawBETA)
	}
}

func TestDiffContents_Identical(t *testing.T) {
	data := []byte("same\ncontent\n")
	fd := DiffContents("f.txt", data, data)
	if fd.Note != "identical to current" {
		t.Errorf("identical inputs should note it; note=%q", fd.Note)
	}
	for _, l := range fd.Lines {
		if l.Kind != "ctx" {
			t.Errorf("identical diff should be all-context, got %q", l.Kind)
		}
	}
}
