package gitdiff

import (
	"strings"
	"testing"
)

// TestHighlightCSS_ModeScoped pins the per-scheme three-block structure of
// /syntax.css: for EVERY registered scheme, a light block scoped to its
// [data-scheme], an explicit-Dark block scoped to [data-mode="dark"], and a
// System-dark copy inside @media (prefers-color-scheme: dark). This is what
// lets one stylesheet recolor every scheme × mode with no JS and no refetch —
// and it guards against (a) a chroma upgrade changing WriteCSS's output shape
// so scopeSyntax stops prefixing, and (b) a scheme leaking unscoped rules that
// would bleed across schemes.
func TestHighlightCSS_ModeScoped(t *testing.T) {
	css := HighlightCSS
	if css == "" {
		t.Fatal("HighlightCSS empty")
	}
	if !strings.Contains(css, "@media (prefers-color-scheme: dark) {") {
		t.Error("missing System-dark media query")
	}
	for _, s := range Schemes {
		light := `[data-scheme="` + s.Name + `"] .chroma`
		dark := `[data-scheme="` + s.Name + `"][data-mode="dark"] .chroma`
		sys := `[data-scheme="` + s.Name + `"]:not([data-mode="light"]) .chroma`
		if !strings.Contains(css, light) {
			t.Errorf("%s: missing scoped light rules (%q)", s.Name, light)
		}
		if !strings.Contains(css, dark) {
			t.Errorf("%s: missing explicit-dark scoped rules (%q)", s.Name, dark)
		}
		if !strings.Contains(css, sys) {
			t.Errorf("%s: missing System-dark scoped rules (%q)", s.Name, sys)
		}
	}
	// No scheme may emit an UNscoped chroma rule (would bleed across schemes).
	if strings.Contains(css, "\n.chroma ") || strings.HasPrefix(css, ".chroma ") {
		t.Error("found an unscoped `.chroma` rule — a scheme is leaking across [data-scheme]")
	}
}

// TestScopeSyntax_DropsBgPrefixesChroma unit-tests the scoping transform in
// isolation so the contract is pinned independent of chroma's full output: the
// unused `.bg` background-div rule is dropped, every `.chroma` rule is prefixed,
// and the leading token-name comment is preserved.
func TestScopeSyntax_DropsBgPrefixesChroma(t *testing.T) {
	in := "/* Background */ .bg { color: #93a1a1; background-color: #002b36; }\n" +
		"/* PreWrapper */ .chroma { color: #93a1a1; }\n" +
		"/* Keyword */ .chroma .k { color: #859900 }\n"
	out := scopeSyntax(in, "[data-mode=\"dark\"]")

	if strings.Contains(out, ".bg {") {
		t.Errorf("scopeSyntax kept the unused .bg rule:\n%s", out)
	}
	if !strings.Contains(out, `/* Keyword */ [data-mode="dark"] .chroma .k { color: #859900 }`) {
		t.Errorf("scopeSyntax did not prefix the keyword rule / keep its comment:\n%s", out)
	}
	if !strings.Contains(out, `[data-mode="dark"] .chroma { color: #93a1a1; }`) {
		t.Errorf("scopeSyntax did not prefix the PreWrapper rule:\n%s", out)
	}
}
