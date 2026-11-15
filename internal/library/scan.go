package library

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// StoreInfo is one review store found on this machine: where it is and how much
// it holds, so the first-launch list can show what reading it would cover.
type StoreInfo struct {
	Store       string // the store directory (…/.prereview or a per-target subdirectory)
	Label       string // what was reviewed, as the list shows it: "~/code/app", plus a per-target store's name
	Comments    int    // the reviewer's comments (requests to the agent excluded)
	Suggestions int
	Versions    int
	Signals     int // reviewer signals (ReviewerSignals): what an extraction reads
}

// ScanMaxDepth bounds the first-launch walk of $HOME.
const ScanMaxDepth = 6

// scanSkip are directory names never descended into: dependencies, VCS
// internals and caches hold no reviews and can be huge.
var scanSkip = map[string]bool{
	"node_modules": true, ".git": true, ".hg": true, ".svn": true, ".cache": true, "Caches": true,
	".npm": true, ".cargo": true, ".rustup": true, ".gradle": true, ".m2": true, "vendor": true,
	".venv": true, "venv": true, "__pycache__": true, ".Trash": true, "Library": true, "tmp": true,
	".worktrees": true,
}

// Scan walks home (to maxDepth) for review stores and returns them, biggest
// first. Read-only; unreadable directories are skipped.
func Scan(home string, maxDepth int) []StoreInfo {
	var out []StoreInfo
	home = filepath.Clean(home)
	base := strings.Count(home, string(filepath.Separator))
	_ = filepath.WalkDir(home, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() && p != home {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		if p != home && (scanSkip[d.Name()] || strings.Count(p, string(filepath.Separator))-base > maxDepth) {
			return filepath.SkipDir
		}
		if d.Name() != StoreDir {
			return nil
		}
		label := labelFor(home, filepath.Dir(p))
		if info, ok := inspect(p); ok {
			info.Label = label
			out = append(out, info)
		}
		// Per-target stores (#199): .prereview/files/<target slug>.
		if entries, err := os.ReadDir(filepath.Join(p, TargetsDir)); err == nil {
			for _, e := range entries {
				if e.IsDir() {
					if info, ok := inspect(filepath.Join(p, TargetsDir, e.Name())); ok {
						info.Label = label + " · " + e.Name()
						out = append(out, info)
					}
				}
			}
		}
		return filepath.SkipDir
	})
	sortStores(out)
	return out
}

func sortStores(out []StoreInfo) {
	sort.Slice(out, func(i, j int) bool {
		if out[i].Signals != out[j].Signals {
			return out[i].Signals > out[j].Signals
		}
		return out[i].Store < out[j].Store
	})
}

// PastReviews is the first-launch list: the stores Scan finds under home, plus
// any recorded in sessions.jsonl that the walk can't reach (outside home, or
// deeper than maxDepth), biggest first.
func PastReviews(dir, home string, maxDepth int) []StoreInfo {
	out := Scan(home, maxDepth)
	seen := map[string]bool{}
	for _, s := range out {
		seen[s.Store] = true
	}
	for _, sess := range LoadSessions(dir) {
		if seen[sess.Store] {
			continue
		}
		seen[sess.Store] = true
		if info, ok := inspect(sess.Store); ok {
			info.Label = labelFor(filepath.Clean(home), sess.Repo)
			out = append(out, info)
		}
	}
	sortStores(out)
	return out
}

// labelFor shows a reviewed directory relative to home ("~/code/app").
func labelFor(home, root string) string {
	rel, err := filepath.Rel(home, root)
	switch {
	case err != nil || strings.HasPrefix(rel, ".."):
		return root
	case rel == ".":
		return "~"
	}
	return "~/" + filepath.ToSlash(rel)
}

// safePath reports a path that can go verbatim into the extraction request: no
// control characters (C0/C1 — a newline would start a new prompt line), no line
// or paragraph separators, no format characters (bidi overrides that make the
// text read differently than it is) and no backticks (which would close its code
// span). A store under any other name is skipped.
func safePath(p string) bool {
	return utf8.ValidString(p) && !strings.ContainsFunc(p, func(r rune) bool {
		return r == '`' || unicode.IsControl(r) || unicode.In(r, unicode.Zl, unicode.Zp, unicode.Cf)
	})
}

// inspect counts a store's records; ok is false when it holds no reviewer signal
// (or its path isn't safe to name in a request).
func inspect(store string) (StoreInfo, bool) {
	if !safePath(store) {
		return StoreInfo{}, false
	}
	c := countStore(store)
	info := StoreInfo{Store: store, Comments: c.comments, Suggestions: c.suggestions, Signals: c.signals()}
	eachLine(filepath.Join(store, VersionsDirName, TimelineFileName), func([]byte) { info.Versions++ })
	return info, info.Signals > 0
}
