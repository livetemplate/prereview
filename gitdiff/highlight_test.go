package gitdiff

import (
	"strings"
	"testing"
)

// TestHighlightCSS_ModeScoped pins the three-block structure of /syntax.css:
// an unscoped light block, an explicit-Dark block scoped to [data-mode="dark"],
// and a System-dark copy inside @media (prefers-color-scheme: dark). This is
// what lets one stylesheet recolor both modes with no JS and no refetch — and
// it guards against a chroma upgrade silently changing WriteCSS's output shape
// such that scopeSyntax stops prefixing (which would leak dark tokens into
// light mode).
func TestHighlightCSS_ModeScoped(t *testing.T) {
	css := HighlightCSS
	if css == "" {
		t.Fatal("HighlightCSS empty")
	}

	// Light tokens are unscoped — the default block.
	if !strings.Contains(css, ".chroma .k {") {
		t.Error("missing unscoped light keyword rule `.chroma .k {`")
	}

	// Dark tokens carry the explicit-mode scope.
	const darkPrefix = `[data-scheme="solarized"][data-mode="dark"] .chroma`
	if !strings.Contains(css, darkPrefix) {
		t.Errorf("missing explicit-dark scoped rules (%q)", darkPrefix)
	}

	// System-dark repeats the dark block inside the media query, scoped so an
	// explicit Light opt-out still wins.
	if !strings.Contains(css, "@media (prefers-color-scheme: dark) {") {
		t.Error("missing System-dark media query")
	}
	if !strings.Contains(css, `[data-scheme="solarized"]:not([data-mode="light"]) .chroma`) {
		t.Error("missing System-dark scoped rules")
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
