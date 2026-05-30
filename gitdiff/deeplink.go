// Package gitdiff — deeplink.go centralises the prereview URL-hash
// grammar so every consumer (the SetURLHash action, the markdown +
// HTML link rewriters, the line-gutter permalink template) shares one
// parser and one stringifier. Extending the grammar means changing
// these two functions, not hunting through the codebase.
//
// Grammar:
//
//	hash      := path [":" target]
//	path      := <URL-encoded segments joined by "/">
//	target    := lineRange | "h-" anchorID
//	lineRange := "L" <n> ["-L" <m>]
//	anchorID  := <opaque, URL-encoded>
//
// Examples:
//
//	foo.go                  — open foo.go
//	subdir/foo.go:L42       — open subdir/foo.go, select line 42
//	subdir/foo.go:L42-L48   — open subdir/foo.go, select lines 42–48
//	README.md:h-architecture — open README.md, scroll to heading "architecture"
package gitdiff

import (
	"net/url"
	"strconv"
	"strings"
)

// ParsedHash is the structured form of a prereview URL hash. Empty
// fields mean "not in this hash". Path is the only required field for
// the hash to be a meaningful deep link — a hash without a path (e.g.
// "L42" or ":h-foo") is rejected (Path == "").
type ParsedHash struct {
	Path     string
	FromLine int
	ToLine   int
	Anchor   string
}

// ParseHash splits a URL hash (without leading "#") into its
// components. Tolerant: a hash without a target part is fine (just
// the path); a hash that doesn't parse as a deep link returns
// ParsedHash{Path: hash} on the assumption that the leading segment
// is still a file path, OR an empty struct if the hash isn't even
// path-shaped (no extension, no slash, no colon — likely a dialog/
// popover/details id from the existing setupHashLink machinery).
//
// The server treats any unresolvable Path as "no such file" and
// no-ops, so a permissive parse is safe — the worst case is a
// no-op dispatch.
func ParseHash(hash string) ParsedHash {
	hash = strings.TrimPrefix(hash, "#")
	if hash == "" {
		return ParsedHash{}
	}

	// Split on the FIRST `:` — paths can contain `:` in theory but
	// our line/anchor targets always start with `L` or `h-`, so we
	// look for `:L` or `:h-` as the discriminator. A `:` followed by
	// anything else is treated as part of the path.
	pathEnd := -1
	for i := 0; i < len(hash); i++ {
		if hash[i] != ':' {
			continue
		}
		rest := hash[i+1:]
		if strings.HasPrefix(rest, "L") || strings.HasPrefix(rest, "h-") {
			pathEnd = i
			break
		}
	}

	var pathPart, targetPart string
	if pathEnd >= 0 {
		pathPart, targetPart = hash[:pathEnd], hash[pathEnd+1:]
	} else {
		pathPart = hash
	}

	path := decodePath(pathPart)
	if path == "" {
		// A hash like ":L42" has no path — useless as a deep link.
		return ParsedHash{}
	}

	out := ParsedHash{Path: path}
	if targetPart == "" {
		return out
	}

	if strings.HasPrefix(targetPart, "h-") {
		anchor, _ := url.QueryUnescape(strings.TrimPrefix(targetPart, "h-"))
		out.Anchor = anchor
		return out
	}

	// Line range: "L<n>" or "L<n>-L<m>".
	if strings.HasPrefix(targetPart, "L") {
		body := strings.TrimPrefix(targetPart, "L")
		// Split on "-L" (not just "-") so an anchor like "h-foo-bar"
		// can't be misparsed here, and so a numeric line never
		// matches if the dash is misplaced.
		if dash := strings.Index(body, "-L"); dash >= 0 {
			from, err1 := strconv.Atoi(body[:dash])
			to, err2 := strconv.Atoi(body[dash+2:])
			if err1 == nil && err2 == nil && from > 0 && to >= from {
				out.FromLine = from
				out.ToLine = to
			}
		} else if n, err := strconv.Atoi(body); err == nil && n > 0 {
			out.FromLine = n
			out.ToLine = n
		}
	}
	return out
}

// FormatHash serialises path + line range + anchor back into a URL
// hash (without leading "#"). Empty path → "" (caller decides whether
// to render an empty data-attr). Line range takes precedence over
// anchor when both are present — the line selection is the more
// specific target.
func FormatHash(path string, fromLine, toLine int, anchor string) string {
	if path == "" {
		return ""
	}
	out := encodePath(path)
	switch {
	case fromLine > 0 && toLine > fromLine:
		out += ":L" + strconv.Itoa(fromLine) + "-L" + strconv.Itoa(toLine)
	case fromLine > 0:
		out += ":L" + strconv.Itoa(fromLine)
	case anchor != "":
		out += ":h-" + url.QueryEscape(anchor)
	}
	return out
}

// ResolveRelativeLink rewrites a link target from inside currentFile
// to a prereview hash URL when it points at a relative path inside
// the repo, or returns the original target unchanged when it points
// elsewhere (http://, https://, mailto:, tel:, data:, // protocol-
// relative, or absolute path starting with `/`).
//
// Returns the rewritten target and isExternal=false for in-repo
// relative paths (caller should write the new hash). Returns the
// original target and isExternal=true for everything else (caller
// should pass through unchanged AND, for HTML, add target="_blank"
// rel="noopener" for safety).
//
// Intra-doc anchors (`#foo`) resolve to `currentFile:h-foo`. Pure
// relative paths (`OTHER.md`, `./sibling.md`, `../parent/file.md`)
// resolve against currentFile's directory. A path with its own
// `#anchor` suffix gets the anchor as the h- target. A path with
// `?query` is treated as external (we don't have a query-string
// channel into the hash grammar).
func ResolveRelativeLink(currentFile, target string) (string, bool) {
	t := strings.TrimSpace(target)
	if t == "" {
		return target, true
	}

	// External schemes — pass through.
	lower := strings.ToLower(t)
	for _, scheme := range []string{"http:", "https:", "mailto:", "tel:", "data:", "javascript:", "ftp:", "ssh:"} {
		if strings.HasPrefix(lower, scheme) {
			return target, true
		}
	}
	// Protocol-relative (`//host/...`).
	if strings.HasPrefix(t, "//") {
		return target, true
	}
	// Absolute path (`/foo/bar`) — could be a server route, not a
	// repo file; pass through.
	if strings.HasPrefix(t, "/") {
		return target, true
	}
	// Query string in target — not representable in our hash grammar.
	if strings.Contains(t, "?") {
		return target, true
	}

	// Intra-doc anchor (`#foo`) → currentFile:h-foo.
	if strings.HasPrefix(t, "#") {
		anchor := strings.TrimPrefix(t, "#")
		return "#" + FormatHash(currentFile, 0, 0, anchor), false
	}

	// Split off any `#anchor` suffix from the relative path.
	var anchor string
	if hash := strings.Index(t, "#"); hash >= 0 {
		anchor = t[hash+1:]
		t = t[:hash]
	}

	resolved := resolveRelativePath(currentFile, t)
	if resolved == "" {
		return target, true
	}
	return "#" + FormatHash(resolved, 0, 0, anchor), false
}

// encodePath URL-encodes each path segment so spaces, unicode, and `?`
// in filenames round-trip. Joins with `/` so the result reads as a
// path, not a single opaque blob.
func encodePath(p string) string {
	parts := strings.Split(p, "/")
	for i, seg := range parts {
		parts[i] = url.PathEscape(seg)
	}
	return strings.Join(parts, "/")
}

// decodePath reverses encodePath. Tolerant of unescaped paths
// (typed directly into the address bar without %-encoding) — those
// round-trip unchanged through QueryUnescape.
func decodePath(p string) string {
	parts := strings.Split(p, "/")
	for i, seg := range parts {
		if decoded, err := url.PathUnescape(seg); err == nil {
			parts[i] = decoded
		}
	}
	return strings.Join(parts, "/")
}

// resolveRelativePath joins a relative target against the directory
// of currentFile, then cleans the result. Returns "" if cleaning
// escapes the repo root via "..". The cleaned path uses forward
// slashes (URL convention, matches our path-segment format) and is
// always non-leading-slash.
func resolveRelativePath(currentFile, target string) string {
	// Strip "./" prefixes — they're a no-op but Path.Clean keeps them.
	for strings.HasPrefix(target, "./") {
		target = target[2:]
	}

	dir := ""
	if slash := strings.LastIndex(currentFile, "/"); slash >= 0 {
		dir = currentFile[:slash]
	}

	// Walk "../" segments by popping dir.
	for strings.HasPrefix(target, "../") {
		if dir == "" {
			return "" // escapes the repo root
		}
		target = target[3:]
		if slash := strings.LastIndex(dir, "/"); slash >= 0 {
			dir = dir[:slash]
		} else {
			dir = ""
		}
	}

	if dir == "" {
		return target
	}
	if target == "" {
		return dir
	}
	return dir + "/" + target
}
