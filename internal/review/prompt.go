package review

import (
	"embed"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// prompt.go is the #147 "ask for suggestions" prompt library: a set of prebuilt
// instructions the reviewer picks and sends to the agent to get inline suggestions
// back. A picked prompt pre-fills the ordinary file-comment composer (its Body
// becomes the comment), so it rides the existing comment → `prereview suggest` path —
// no new comment kind. Built-ins are embedded; a user overlay lives at
// ~/.config/prereview/prompts/*.md and overrides/extends them by slug.

//go:embed builtin_prompts/*.md
var builtinPromptsFS embed.FS

// Prompt is one "ask for suggestions" template. Slug is the stable id (the filename
// without .md); Title is shown in the picker; Body pre-fills the file-comment
// composer and, once saved, is the instruction the agent acts on.
type Prompt struct {
	Slug  string
	Title string
	Body  string
}

// LoadPrompts returns the embedded built-in prompts overlaid with the user's library
// at userDir. A user file with the same slug (filename) OVERRIDES the built-in; new
// slugs are added. Sorted by title for a stable picker. Tolerant by design: a missing
// user dir, or an unreadable / title-only file, is skipped — the picker must never
// break a review.
func LoadPrompts(userDir string) []Prompt {
	return loadPromptLibrary(builtinPromptsFS, "builtin_prompts", userDir)
}

// loadPromptLibrary is the shared built-ins-plus-user-overlay loader behind both
// prompt libraries: the #147 "ask for suggestions" prompts and the #191 quiz
// prompts. Factored rather than copied so the two can't drift on the rules that
// matter — override-by-slug, sort-by-title, and skip-don't-fail — since a user's
// library must never be able to break a review.
func loadPromptLibrary(builtinFS embed.FS, builtinDir, userDir string) []Prompt {
	bySlug := map[string]Prompt{}
	if entries, err := builtinFS.ReadDir(builtinDir); err == nil {
		for _, e := range entries {
			b, _ := builtinFS.ReadFile(builtinDir + "/" + e.Name()) // just-listed → readable
			if p, ok := parsePromptBytes(e.Name(), b); ok {
				bySlug[p.Slug] = p
			}
		}
	}
	if userDir != "" {
		if entries, err := os.ReadDir(userDir); err == nil {
			for _, e := range entries {
				if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
					continue
				}
				b, err := os.ReadFile(filepath.Join(userDir, e.Name()))
				if err != nil {
					continue
				}
				if p, ok := parsePromptBytes(e.Name(), b); ok {
					bySlug[p.Slug] = p
				}
			}
		}
	}
	out := make([]Prompt, 0, len(bySlug))
	for _, p := range bySlug {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Title < out[j].Title })
	return out
}

// parsePromptBytes turns a .md file into a Prompt: slug = filename without .md; a
// leading "# Title" heading sets Title (else the slug is the title); everything after
// the heading is the Body. A blank body yields ok=false (skipped).
func parsePromptBytes(filename string, content []byte) (Prompt, bool) {
	slug := strings.TrimSuffix(filename, ".md")
	if slug == "" {
		return Prompt{}, false
	}
	title := slug
	body := strings.TrimSpace(string(content))
	if rest, ok := strings.CutPrefix(body, "# "); ok {
		parts := strings.SplitN(rest, "\n", 2)
		title = strings.TrimSpace(parts[0])
		body = ""
		if len(parts) == 2 {
			body = strings.TrimSpace(parts[1])
		}
	}
	if body == "" || title == "" {
		return Prompt{}, false
	}
	return Prompt{Slug: slug, Title: title, Body: body}, true
}
