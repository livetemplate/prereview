package gitdiff

import (
	"strings"
	"testing"
)

// sanitizeImageHTML is the security boundary for the re-admitted raw-HTML
// image: it must pass through EXACTLY a local <img> (optionally wrapped in a
// single <p>/<div>) and reject everything else by returning ok=false, so the
// caller drops it. These cases cover the allow set and every injection vector.
func TestSanitizeImageHTML(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		ok   bool
		want []string // substrings required in the sanitized output (ok cases)
	}{
		// --- allowed ---
		{
			name: "wrapped hero",
			raw:  `<p align="center"><img src="docs/hero.gif" alt="hi" width="820"></p>`,
			ok:   true,
			want: []string{`<p align="center">`, `<img src="docs/hero.gif"`, `alt="hi"`, `width="820"`, `</p>`},
		},
		{name: "bare img", raw: `<img src="docs/hero.gif" alt="hi">`, ok: true,
			want: []string{`<img src="docs/hero.gif"`, `alt="hi"`}},
		{name: "div wrapper", raw: `<div align="center"><img src="x.png"></div>`, ok: true,
			want: []string{`<div align="center">`, `<img src="x.png"`, `</div>`}},
		{name: "multiline wrapped", raw: "<p align=\"center\">\n<img src=\"x.png\">\n</p>", ok: true,
			want: []string{`<p align="center">`, `<img src="x.png"`}},
		{name: "self-closing img", raw: `<img src="x.png" alt="a"/>`, ok: true,
			want: []string{`<img src="x.png"`, `alt="a"`}},

		// --- sanitized: the img survives, the dangerous bits are stripped ---
		{name: "onerror handler stripped", raw: `<img src="x.png" onerror="alert(1)">`, ok: true,
			want: []string{`<img src="x.png"`}}, // onerror absence checked by the allowlist invariants below

		// --- rejected: whole block dropped ---
		{name: "javascript src", raw: `<img src="javascript:alert(1)">`, ok: false},
		{name: "data src", raw: `<img src="data:image/svg+xml,<svg onload=alert(1)>">`, ok: false},
		{name: "remote https src", raw: `<img src="https://evil.example/x.png">`, ok: false},
		{name: "protocol-relative src", raw: `<img src="//evil.example/x.png">`, ok: false},
		{name: "server-absolute src", raw: `<img src="/etc/passwd">`, ok: false},
		{name: "no src", raw: `<img alt="x">`, ok: false},
		{name: "empty src", raw: `<img src="">`, ok: false},
		{name: "script sibling in block", raw: `<p><img src="x.png"><script>alert(1)</script></p>`, ok: false},
		{name: "style on img dropped — img still ok", raw: `<img src="x.png" style="x:url(javascript:1)">`, ok: true,
			want: []string{`<img src="x.png"`}}, // style must be gone — checked below
		{name: "style on wrapper", raw: `<div style="background:url(javascript:1)"><img src="x.png"></div>`, ok: true,
			want: []string{`<div>`, `<img src="x.png"`}}, // wrapper kept but style dropped
		{name: "non-img/non-wrapper tag", raw: `<a href="x"><img src="x.png"></a>`, ok: false},
		{name: "two images", raw: `<p><img src="a.png"><img src="b.png"></p>`, ok: false},
		{name: "text alongside image", raw: `<p>caption <img src="x.png"></p>`, ok: false},
		{name: "unclosed wrapper", raw: `<p><img src="x.png">`, ok: false},
		{name: "stray close tag", raw: `<img src="x.png"></span>`, ok: false},
		{name: "comment in block", raw: `<p><!-- evil --><img src="x.png"></p>`, ok: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := sanitizeImageHTML(tc.raw)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v (got %q)", ok, tc.ok, got)
			}
			if !ok {
				return
			}
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("output %q missing %q", got, w)
				}
			}
			// Allowlist invariants on every accepted output.
			for _, banned := range []string{"onerror", "onload", "style=", "javascript:", "<script"} {
				if strings.Contains(strings.ToLower(got), banned) {
					t.Errorf("sanitized output leaked %q: %q", banned, got)
				}
			}
		})
	}
}

// End-to-end: a raw-HTML hero in a README renders a resolved, server-absolute
// <img> (not the omitted-comment), and a malicious raw-HTML block is dropped.
func TestRenderMarkdownBlocks_RawHTMLImage(t *testing.T) {
	src := `<p align="center"><img src="docs/hero.gif" alt="hero" width="820"></p>` + "\n\n" +
		"# Title\n\n" +
		"Inline <img src=\"icon.png\"> here.\n\n" +
		`<img src="x" onerror="alert(1)">` + "\n\n" +
		"<script>alert('xss')</script>\n"
	blocks := RenderMarkdownBlocks([]byte(src), "README.md")
	var joined string
	for _, b := range blocks {
		joined += string(b.HTML)
	}

	// Hero: rendered, centered, src resolved server-absolute.
	if !strings.Contains(joined, `<img src="/docs/hero.gif"`) {
		t.Errorf("hero img not rendered/resolved: %q", joined)
	}
	if !strings.Contains(joined, `align="center"`) {
		t.Errorf("hero centering lost: %q", joined)
	}
	// Inline image rendered (root-relative → /icon.png).
	if !strings.Contains(joined, `<img src="/icon.png"`) {
		t.Errorf("inline img not rendered: %q", joined)
	}
	// Security: handler stripped, script dropped.
	if strings.Contains(strings.ToLower(joined), "onerror") {
		t.Errorf("onerror handler leaked: %q", joined)
	}
	if strings.Contains(joined, "<script") || strings.Contains(joined, "alert('xss')") {
		t.Errorf("script not dropped: %q", joined)
	}
}

// Non-image raw HTML must keep emitting its block so the per-block line cursor
// is unchanged (emit() skips an empty string). The omitted-comment placeholder
// stands in for it, exactly as before the image extension existed.
func TestRenderMarkdownBlocks_NonImageRawHTMLStillOmitted(t *testing.T) {
	blocks := RenderMarkdownBlocks([]byte("<details><summary>x</summary>body</details>\n"), "")
	if len(blocks) == 0 {
		t.Fatal("non-image raw HTML produced no block")
	}
	joined := ""
	for _, b := range blocks {
		joined += string(b.HTML)
	}
	if strings.Contains(joined, "<details") {
		t.Errorf("non-image raw HTML leaked through: %q", joined)
	}
}
