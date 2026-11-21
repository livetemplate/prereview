package gitdiff

import (
	"strings"
	"testing"
)

func TestBlockText(t *testing.T) {
	src := "# A *title*\n\nSome **bold** and `code` &amp; [a link](x.md).\n\n- first\n- second\n\n| a | b |\n|---|---|\n| one | two |\n"
	var got []string
	for _, b := range RenderMarkdownBlocks([]byte(src), "doc.md") {
		got = append(got, strings.Join(strings.Fields(BlockText(b.HTML)), " "))
	}
	all := strings.Join(got, " | ")
	for _, want := range []string{"A title", "Some bold and code & a link.", "first", "second", "one two"} {
		if !strings.Contains(all, want) {
			t.Errorf("rendered text %q lacks %q", all, want)
		}
	}
}
