package main

import (
	"flag"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
	"text/template"
)

// prereview.tmpl is whitespace-significant: livetemplate emits the template's
// static text verbatim into the response, so any whitespace a reflow formatter
// adds or strips lands in the rendered DOM (see CLAUDE.md). No off-the-shelf
// formatter can touch it safely. This test is the guard that replaces one:
// it computes a rendering-equivalent SIGNATURE of the template and compares it
// to a committed golden, so any edit that would change what the browser renders
// fails — in CI and locally — while a safe reformat stays green.
//
// Regenerate the golden after an INTENTIONAL content change:
//
//	go test -run TestTemplateOutputSignature -update-sig .
const (
	tmplPath   = "prereview.tmpl"
	goldenPath = "testdata/prereview.tmpl.sig.golden"
)

var updateSig = flag.Bool("update-sig", false,
	"regenerate testdata/prereview.tmpl.sig.golden from the current prereview.tmpl")

func TestTemplateOutputSignature(t *testing.T) {
	src, err := os.ReadFile(tmplPath)
	if err != nil {
		t.Fatalf("reading %s: %v", tmplPath, err)
	}
	got := templateSignature(t, string(src))

	if *updateSig {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatalf("creating testdata/: %v", err)
		}
		if err := os.WriteFile(goldenPath, []byte(got), 0o644); err != nil {
			t.Fatalf("writing %s: %v", goldenPath, err)
		}
		t.Logf("wrote %s (%d bytes)", goldenPath, len(got))
		return
	}

	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("reading golden %s: %v\n"+
			"create it with: go test -run TestTemplateOutputSignature -update-sig .", goldenPath, err)
	}
	if got != string(want) {
		off, gctx, wctx := firstDivergence(string(want), got)
		t.Errorf(`prereview.tmpl's rendered-output signature changed.

If you only REFORMATTED (layout/whitespace), this means the edit changed what the
browser will actually render — i.e. it CORRUPTED the template. Revert the offending
change. Common culprits: a reflow formatter (prettier/djlint — never run them on this
file), a deleted space between inline elements, or a broken {{- -}} trim marker.

If this was an INTENTIONAL content change, regenerate the golden and commit it:
    go test -run TestTemplateOutputSignature -update-sig .

First divergence at signature offset %d:
    golden: %q
    now:    %q`, off, wctx, gctx)
	}
}

// templateSignature returns a fingerprint that is equal for two templates exactly
// when they emit DOM-identical output for every possible state:
//   - Each defined template's parse tree is serialized with text/template's
//     .String(), which preserves static text VERBATIM and canonicalizes template
//     actions — so wrapping an action like {{if and (a) (b)}} across source lines
//     leaves the signature untouched.
//   - Only intra-tag (between-attribute) whitespace is then collapsed, because the
//     HTML parser always ignores it — so splitting attributes onto their own lines
//     is free. Whitespace OUTSIDE tags and INSIDE quoted attribute values is left
//     exactly as-is, since whether it matters depends on CSS the parser can't see;
//     keeping it strict means the guard never green-lights a real change.
func templateSignature(t *testing.T, src string) string {
	tmpl := parseWithStubs(t, src)

	names := make([]string, 0, len(tmpl.Templates()))
	bodies := make(map[string]string)
	for _, tt := range tmpl.Templates() {
		if tt.Tree == nil || tt.Tree.Root == nil {
			continue
		}
		names = append(names, tt.Name())
		bodies[tt.Name()] = normalizeIntraTag(tt.Tree.Root.String())
	}
	sort.Strings(names) // map iteration order is random; pin it

	var b strings.Builder
	for _, name := range names {
		b.WriteString("=== ")
		b.WriteString(name)
		b.WriteString(" ===\n")
		b.WriteString(bodies[name])
		b.WriteString("\n")
	}
	return b.String()
}

var undefinedFuncRe = regexp.MustCompile(`function "([^"]+)" not defined`)

// parseWithStubs parses src with text/template. The template uses only builtin
// functions today, but if a future edit introduces a custom func, parsing would
// fail with `function "x" not defined` — which, if swallowed, would silently turn
// this guard into a no-op (green while unprotected: the worst outcome). Instead we
// stub each unknown name and retry, so the guard keeps working; the stub is never
// executed (we only parse + .String()), so its behavior is irrelevant to the
// signature. Any OTHER parse error is a real syntax error and fails loudly.
func parseWithStubs(t *testing.T, src string) *template.Template {
	funcs := template.FuncMap{}
	for i := 0; i < 200; i++ {
		tmpl, err := template.New("prereview").Funcs(funcs).Parse(src)
		if err == nil {
			return tmpl
		}
		m := undefinedFuncRe.FindStringSubmatch(err.Error())
		if m == nil {
			t.Fatalf("parsing %s: %v", tmplPath, err)
		}
		funcs[m[1]] = func(...any) any { return nil }
	}
	t.Fatalf("parsing %s: gave up stubbing undefined functions (possible loop)", tmplPath)
	return nil
}

// normalizeIntraTag collapses runs of whitespace that sit between attributes
// inside a start/end tag (and drops whitespace abutting the closing '>'). It does
// NOT touch whitespace inside quoted attribute values or outside of tags.
func normalizeIntraTag(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	inTag := false
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		if quote != 0 { // inside an attribute value — copy verbatim
			b.WriteByte(c)
			if c == quote {
				quote = 0
			}
			continue
		}
		switch {
		case c == '<':
			inTag = true
			b.WriteByte(c)
		case c == '>':
			inTag = false
			b.WriteByte(c)
		case inTag && (c == '"' || c == '\''):
			quote = c
			b.WriteByte(c)
		case inTag && isASCIISpace(c):
			j := i
			for j < len(s) && isASCIISpace(s[j]) {
				j++
			}
			if j >= len(s) || s[j] != '>' { // keep one separator unless it hugs '>'
				b.WriteByte(' ')
			}
			i = j - 1
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

func isASCIISpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f'
}

// firstDivergence locates the first differing byte and returns a small window of
// context from each side, to make a signature mismatch easy to read.
func firstDivergence(want, got string) (offset int, gotCtx, wantCtx string) {
	n := len(want)
	if len(got) < n {
		n = len(got)
	}
	i := 0
	for i < n && want[i] == got[i] {
		i++
	}
	window := func(s string) string {
		lo, hi := i-30, i+50
		if lo < 0 {
			lo = 0
		}
		if hi > len(s) {
			hi = len(s)
		}
		return s[lo:hi]
	}
	return i, window(got), window(want)
}
