package gitdiff

import "testing"

func TestParseHash(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want ParsedHash
	}{
		{name: "empty", in: "", want: ParsedHash{}},
		{name: "leading hash stripped", in: "#foo.go", want: ParsedHash{Path: "foo.go"}},
		{name: "path only", in: "foo.go", want: ParsedHash{Path: "foo.go"}},
		{name: "nested path", in: "sub/dir/foo.go", want: ParsedHash{Path: "sub/dir/foo.go"}},
		{name: "single line", in: "foo.go:L42", want: ParsedHash{Path: "foo.go", FromLine: 42, ToLine: 42}},
		{name: "line range", in: "foo.go:L42-L48", want: ParsedHash{Path: "foo.go", FromLine: 42, ToLine: 48}},
		{name: "anchor", in: "README.md:h-architecture", want: ParsedHash{Path: "README.md", Anchor: "architecture"}},
		{name: "anchor with dashes", in: "README.md:h-foo-bar-baz", want: ParsedHash{Path: "README.md", Anchor: "foo-bar-baz"}},
		{name: "encoded path", in: "with%20space.md", want: ParsedHash{Path: "with space.md"}},
		{name: "encoded anchor", in: "doc.md:h-h%C3%A9llo", want: ParsedHash{Path: "doc.md", Anchor: "héllo"}},
		// Tolerance:
		{name: "no leading L means colon stays in path", in: "foo:bar.go", want: ParsedHash{Path: "foo:bar.go"}},
		{name: "invalid line number → no range", in: "foo.go:Labc", want: ParsedHash{Path: "foo.go"}},
		{name: "to < from → no range", in: "foo.go:L10-L5", want: ParsedHash{Path: "foo.go"}},
		{name: "zero line → no range", in: "foo.go:L0", want: ParsedHash{Path: "foo.go"}},
		{name: "negative line → no range", in: "foo.go:L-5", want: ParsedHash{Path: "foo.go"}},
		// Edge: leading colon (no path) → empty.
		{name: "no path", in: ":L42", want: ParsedHash{}},
		// Dialog-like ids pass through as Path — the server treats
		// unresolvable paths as no-op.
		{name: "dialog id passes as path", in: "confirm-delete-abc", want: ParsedHash{Path: "confirm-delete-abc"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseHash(tt.in)
			if got != tt.want {
				t.Errorf("ParseHash(%q) = %+v, want %+v", tt.in, got, tt.want)
			}
		})
	}
}

func TestFormatHash(t *testing.T) {
	tests := []struct {
		name                       string
		path, anchor               string
		fromLine, toLine           int
		want                       string
	}{
		{name: "empty path", path: "", want: ""},
		{name: "path only", path: "foo.go", want: "foo.go"},
		{name: "nested path", path: "sub/dir/foo.go", want: "sub/dir/foo.go"},
		{name: "single line", path: "foo.go", fromLine: 42, toLine: 42, want: "foo.go:L42"},
		{name: "line range", path: "foo.go", fromLine: 42, toLine: 48, want: "foo.go:L42-L48"},
		{name: "anchor", path: "README.md", anchor: "architecture", want: "README.md:h-architecture"},
		{name: "lines override anchor", path: "foo.md", fromLine: 5, toLine: 5, anchor: "skipped", want: "foo.md:L5"},
		{name: "path with spaces is encoded", path: "with space.md", want: "with%20space.md"},
		{name: "anchor with unicode is encoded", path: "doc.md", anchor: "héllo", want: "doc.md:h-h%C3%A9llo"},
		// Round-trip:
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatHash(tt.path, tt.fromLine, tt.toLine, tt.anchor)
			if got != tt.want {
				t.Errorf("FormatHash(%q, %d, %d, %q) = %q, want %q",
					tt.path, tt.fromLine, tt.toLine, tt.anchor, got, tt.want)
			}
		})
	}
}

func TestFormatHash_RoundTripThroughParseHash(t *testing.T) {
	cases := []ParsedHash{
		{Path: "foo.go"},
		{Path: "sub/dir/foo.go"},
		{Path: "foo.go", FromLine: 42, ToLine: 42},
		{Path: "foo.go", FromLine: 42, ToLine: 48},
		{Path: "README.md", Anchor: "architecture"},
		{Path: "with space.md"},
		{Path: "doc.md", Anchor: "héllo-world"},
	}
	for _, c := range cases {
		t.Run(c.Path, func(t *testing.T) {
			h := FormatHash(c.Path, c.FromLine, c.ToLine, c.Anchor)
			got := ParseHash(h)
			if got != c {
				t.Errorf("round-trip failed: %+v → %q → %+v", c, h, got)
			}
		})
	}
}

func TestResolveRelativeLink(t *testing.T) {
	tests := []struct {
		name        string
		current, in string
		wantOut     string
		wantExt     bool
	}{
		// In-repo relative paths.
		{name: "sibling in same dir", current: "README.md", in: "OTHER.md", wantOut: "#OTHER.md", wantExt: false},
		{name: "sibling with ./", current: "README.md", in: "./OTHER.md", wantOut: "#OTHER.md", wantExt: false},
		{name: "nested current to sibling", current: "docs/README.md", in: "OTHER.md", wantOut: "#docs/OTHER.md", wantExt: false},
		{name: "parent dir reference", current: "docs/README.md", in: "../top.md", wantOut: "#top.md", wantExt: false},
		{name: "two parents up", current: "a/b/c/README.md", in: "../../top.md", wantOut: "#a/top.md", wantExt: false},
		{name: "escapes repo root", current: "README.md", in: "../escape.md", wantOut: "../escape.md", wantExt: true},
		// Anchors.
		{name: "intra-doc anchor", current: "README.md", in: "#hero", wantOut: "#README.md:h-hero", wantExt: false},
		{name: "sibling with anchor", current: "README.md", in: "OTHER.md#api", wantOut: "#OTHER.md:h-api", wantExt: false},
		{name: "intra-doc anchor in nested file", current: "docs/api.md", in: "#endpoints", wantOut: "#docs/api.md:h-endpoints", wantExt: false},
		// External — pass-through, isExternal=true.
		{name: "https url", current: "README.md", in: "https://example.com/x", wantOut: "https://example.com/x", wantExt: true},
		{name: "http url", current: "README.md", in: "http://example.com", wantOut: "http://example.com", wantExt: true},
		{name: "mailto", current: "README.md", in: "mailto:foo@bar.com", wantOut: "mailto:foo@bar.com", wantExt: true},
		{name: "absolute path is external", current: "README.md", in: "/api/foo", wantOut: "/api/foo", wantExt: true},
		{name: "protocol relative", current: "README.md", in: "//cdn.com/x.js", wantOut: "//cdn.com/x.js", wantExt: true},
		{name: "query string is external", current: "README.md", in: "page.html?id=1", wantOut: "page.html?id=1", wantExt: true},
		// Edge — empty.
		{name: "empty target", current: "README.md", in: "", wantOut: "", wantExt: true},
		// Case-insensitive scheme detection.
		{name: "uppercase HTTPS", current: "README.md", in: "HTTPS://x.com", wantOut: "HTTPS://x.com", wantExt: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotOut, gotExt := ResolveRelativeLink(tt.current, tt.in)
			if gotOut != tt.wantOut || gotExt != tt.wantExt {
				t.Errorf("ResolveRelativeLink(%q, %q) = (%q, %v), want (%q, %v)",
					tt.current, tt.in, gotOut, gotExt, tt.wantOut, tt.wantExt)
			}
		})
	}
}
