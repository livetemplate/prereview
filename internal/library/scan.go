package library

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/livetemplate/prereview/csv"
)

// StoreInfo is one review store found on this machine: where it is and how much
// it holds, so the first-launch list can show what reading it would cover.
type StoreInfo struct {
	Store       string // the store directory (…/.prereview or a per-target subdirectory)
	Root        string // what was reviewed: the directory above the .prereview segment
	Label       string // Root as the list shows it: "~/code/app", plus the per-target store's name
	Comments    int
	Suggestions int
	Versions    int
}

// Signals is how many reviewer signals the store holds (comments + suggestions):
// what an extraction reads.
func (s StoreInfo) Signals() int { return s.Comments + s.Suggestions }

// TargetsDir is where a directory's per-target (single-file review) stores live
// inside its .prereview (kept in step with the main package's targetsDirName).
const TargetsDir = "files"

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
		if d.Name() != ".prereview" {
			return nil
		}
		root := filepath.Dir(p)
		label := "~"
		if rel, err := filepath.Rel(home, root); err == nil && rel != "." {
			label = "~/" + filepath.ToSlash(rel)
		}
		if info, ok := inspect(p, root); ok {
			info.Label = label
			out = append(out, info)
		}
		// Per-target stores (#199): .prereview/files/<target slug>.
		if entries, err := os.ReadDir(filepath.Join(p, TargetsDir)); err == nil {
			for _, e := range entries {
				if e.IsDir() {
					if info, ok := inspect(filepath.Join(p, TargetsDir, e.Name()), root); ok {
						info.Label = label + " · " + e.Name()
						out = append(out, info)
					}
				}
			}
		}
		return filepath.SkipDir
	})
	sort.Slice(out, func(i, j int) bool {
		if out[i].Signals() != out[j].Signals() {
			return out[i].Signals() > out[j].Signals()
		}
		return out[i].Store < out[j].Store
	})
	return out
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
func inspect(store, root string) (StoreInfo, bool) {
	if !safePath(store) {
		return StoreInfo{}, false
	}
	info := StoreInfo{Store: store, Root: root}
	if rows, err := csv.Read(filepath.Join(store, commentsFile)); err == nil {
		info.Comments = len(rows)
	}
	eachLine(filepath.Join(store, suggestionsFile), func([]byte) { info.Suggestions++ })
	eachLine(filepath.Join(store, "versions", "timeline.jsonl"), func([]byte) { info.Versions++ })
	return info, info.Signals() > 0
}

// Inspect counts one store's records (for a store named directly, e.g. the
// current review's when it goes idle).
func Inspect(store string) StoreInfo {
	info, _ := inspect(store, filepath.Dir(store))
	return info
}
