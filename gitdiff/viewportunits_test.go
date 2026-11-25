package gitdiff

import (
	"strings"
	"testing"
)

func TestRewriteViewportUnits(t *testing.T) {
	pin := func(n string) string { return "calc(" + n + " * var(--prereview-vh, 9px))" }
	for _, tc := range []struct{ in, want string }{
		{"min-height:100vh", "min-height:" + pin("100")},
		{"height: 70vh;", "height: " + pin("70") + ";"},
		{"top:-12.5vh", "top:" + pin("-12.5")},
		{"height:.5VH", "height:" + pin(".5")},
		{"h:100dvh;h:100svh;h:100lvh", "h:" + pin("100") + ";h:" + pin("100") + ";h:" + pin("100")},
		{"block-size:50vb;b:10dvb", "block-size:" + pin("50") + ";b:" + pin("10")},
		{"width:80vmax", "width:max(80vw, " + pin("80") + ")"},
		{"calc(100vh - 2rem)", "calc(" + pin("100") + " - 2rem)"},
		{"margin:10vh 5vw", "margin:" + pin("10") + " 5vw"},
		// Left alone: units that don't follow the frame's height, names that
		// only contain a unit, and plain lengths.
		{"width:100vw;height:50vmin", "width:100vw;height:50vmin"},
		{".hero-10vh{color:red}", ".hero-10vh{color:red}"},
		{"background:url(a-10vh.png)", "background:url(a-10vh.png)"},
		{"#x10vh{}", "#x10vh{}"},
		{"height:100px", "height:100px"},
		{"height:10vhx", "height:10vhx"},
	} {
		if got := rewriteViewportUnits(tc.in); got != tc.want {
			t.Errorf("rewriteViewportUnits(%q)\n got %q\nwant %q", tc.in, got, tc.want)
		}
	}
}

// TestRenderHTMLPreview_PinsViewportUnits: the preview pins vh in <style> text
// and style attributes, leaves scripts and class names alone, and keeps every
// block's source line range.
func TestRenderHTMLPreview_PinsViewportUnits(t *testing.T) {
	src := "<html><head><style>\n.hero{min-height:100vh}\n</style></head>\n<body>\n" +
		"<section class=\"h-[50vh]\" style=\"height:70vh\">a</section>\n" +
		"<script>el.style.minHeight = '100vh'</script>\n" +
		"<p>b</p>\n</body></html>"
	doc, blocks := RenderHTMLPreview([]byte(src), "x.html")
	for _, want := range []string{
		".hero{min-height:calc(100 * var(--prereview-vh, 9px))}",
		`style="height:calc(70 * var(--prereview-vh, 9px))"`,
		`class="h-[50vh]"`,
		"el.style.minHeight = '100vh'",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("the preview lacks %q:\n%s", want, doc)
		}
	}
	if len(blocks) != 2 || blocks[0].StartLine != 5 || blocks[1].StartLine != 7 {
		t.Errorf("block ranges moved: %+v", blocks)
	}
}
