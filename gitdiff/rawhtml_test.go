package gitdiff

import (
	"strings"
	"testing"
)

// TestRawHTMLRendersSanitized: raw HTML in Markdown renders like GitHub's
// preview, through an allowlist: structure, text, links and local images
// stay; anything that runs code, loads content, takes input or styles the
// page goes.
func TestRawHTMLRendersSanitized(t *testing.T) {
	for _, tc := range []struct {
		name, src string
		has, not  []string
	}{
		{"structure and text stay", "<section class=\"hero\" id=\"top\"><h1>Build apps</h1><p class=\"sub\">Write <code>html/template</code>.</p></section>\n",
			[]string{"<section>", "<h1>Build apps</h1>", "<code>html/template</code>"}, []string{"class=", "id="}},
		{"details stays", "<details><summary>x</summary>body</details>\n", []string{"<details>", "<summary>x</summary>"}, nil},
		{"safe links stay", "<p><a href=\"/getting-started\">Docs</a> <a href=\"https://example.com\">Site</a></p>\n",
			[]string{`href="/getting-started"`, `href="https://example.com"`}, nil},
		{"script goes with its content", "<div>ok<script>alert(1)</script></div>\n", []string{"<div>ok</div>"}, []string{"script", "alert"}},
		{"event handlers and style go", "<p onclick=\"x()\" style=\"color:red\">hi</p>\n", []string{"<p>hi</p>"}, []string{"onclick", "style"}},
		{"javascript links lose their href", "<p><a href=\"javascript:alert(1)\">x</a><a href=\" JaVaScRiPt:alert(1)\">y</a><a href=\"java\tscript:alert(1)\">z</a></p>\n",
			[]string{"<a>x</a>", "<a>y</a>", "<a>z</a>"}, []string{"alert"}},
		{"iframes, forms and svg go", "<div><iframe src=\"https://x\"></iframe><form><input></form><svg><a href=\"javascript:1\"/></svg>kept</div>\n",
			[]string{"<div>kept</div>"}, []string{"iframe", "form", "input", "svg"}},
		{"a remote image goes", "<div><img src=\"https://evil.example/x.png\" onerror=\"x()\">t</div>\n", []string{"<div>t</div>"}, []string{"img", "onerror"}},
		{"unknown elements keep their text", "<div><my-widget data-x=\"1\">inner</my-widget></div>\n", []string{"<div>inner</div>"}, []string{"my-widget", "data-x"}},
		{"an unclosed tag is closed in its block", "<div class=\"shell\">\nstarts here\n", []string{"<div>", "starts here", "</div>"}, []string{"shell"}},
		{"a wrapper-only block renders nothing", "<main class=\"doc\">\n\nbody\n", []string{"<p>body</p>"}, []string{"<main"}},
		{"a stray close tag is dropped", "</div>\n\ntext\n", []string{"<p>text</p>"}, []string{"</div>"}},
		{"a comment-only block renders nothing", "<!-- editors: keep this compact -->\n\nbody\n", []string{"<p>body</p>"}, []string{"editors", "<!--"}},
		{"inline tags", "Press <kbd>Ctrl</kbd> and <b onclick=\"x()\">go</b><br>.\n", []string{"<kbd>Ctrl</kbd>", "<b>go</b>", "<br/>"}, []string{"onclick"}},
		{"inline script goes", "a <script>alert(1)</script> b\n", nil, []string{"<script"}},
	} {
		var out strings.Builder
		for _, b := range RenderMarkdownBlocks([]byte(tc.src), "page.md") {
			out.WriteString(string(b.HTML))
		}
		got := out.String()
		for _, s := range tc.has {
			if !strings.Contains(got, s) {
				t.Errorf("%s: %q lacks %q", tc.name, got, s)
			}
		}
		for _, s := range tc.not {
			if strings.Contains(got, s) {
				t.Errorf("%s: %q still has %q", tc.name, got, s)
			}
		}
	}
}
