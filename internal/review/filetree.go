package review

import (
	"bytes"
	_ "embed"
	"fmt"
	"html/template"
	"path"
	"sort"
	"strings"

	"github.com/livetemplate/prereview/gitdiff"
)

//go:embed filetree.tmpl
var fileTreeTmplSrc string

// fileBrowserTmpl is a standalone html/template (NOT processed by livetemplate)
// that renders the drawer's file browser. It lives outside prereview.tmpl
// because the folder tree is self-recursive ({{template "treeNode"}} calls
// itself) and livetemplate flattens {{template}} calls at parse time, which
// overflows on recursion. Standard html/template executes recursion fine; the
// rendered markup is injected back into prereview.tmpl as template.HTML — the
// same native-<details> approach tinkerdown uses for its nested nav.
var fileBrowserTmpl = template.Must(template.New("filetree").Parse(fileTreeTmplSrc))

// FileBrowserHTML renders the drawer file browser (folder tree when unfiltered,
// flat path list while filtering) for injection into prereview.tmpl. Zero-arg so
// the template can emit `{{.FileBrowserHTML}}`; re-executed each render so
// selection / viewed / filter stay current.
func (s PrereviewState) FileBrowserHTML() template.HTML {
	var buf bytes.Buffer
	if err := fileBrowserTmpl.ExecuteTemplate(&buf, "fileBrowser", s); err != nil {
		return template.HTML(fmt.Sprintf(
			`<p class="empty"><small>file browser render error: %s</small></p>`,
			template.HTMLEscapeString(err.Error())))
	}
	return template.HTML(buf.String())
}

// TreeNode is one node in the file-browser tree: a directory (IsDir, with
// Children) or a file leaf (Entry set). The recursive template renders nested
// <details>/<summary> for directories and the existing file row for leaves.
//
// All display state (IsSelected, IsViewed, DefaultOpen) and roll-up counts are
// resolved here at build time so the recursive template needs no access to the
// root state ($) — Go templates drop $ when a template calls itself with a
// child node as the new dot.
type TreeNode struct {
	Name     string      // last path segment, the row label
	Path     string      // full path from repo root (the directory path for dirs)
	IsDir    bool        // directory vs file leaf
	Depth    int         // 0 for top-level entries, +1 per level — drives indentation
	Children []*TreeNode // directories first, then files, each alphabetical

	// Entry is the underlying file row for a leaf; nil for directories.
	Entry *gitdiff.FileEntry

	// Resolved per-node display state.
	IsSelected  bool // leaf path == SelectedFile
	IsViewed    bool // leaf path is in ViewedFiles
	DefaultOpen bool // dir initial expansion: contains a changed or the selected file

	// Roll-ups. A leaf mirrors its own file; a directory aggregates its whole
	// subtree. Added/Deleted clamp negative (binary/unknown) child values to 0.
	CommentCount int
	Added        int
	Deleted      int
	HasChanged   bool // leaf changed vs base, or any changed descendant for a dir
}

// Ext is the extension of a leaf's label (".go", ".tar.gz" → ".gz"), or "" for
// directories, extensionless names ("Makefile"), and dotfiles whose only dot is
// leading (".gitignore"). NameStem is the label minus Ext. The drawer renders
// them as separate spans so a long filename truncates within the stem while the
// extension stays visible (issue #56). Zero-arg so the template can call them.
func (n *TreeNode) Ext() string {
	if n.IsDir {
		return ""
	}
	base := path.Base(n.Name) // bare filename even when Name is a full path (flat list)
	if i := strings.LastIndex(base, "."); i > 0 {
		return base[i:]
	}
	return ""
}

// NameStem is the label (full path in the flat list, bare name in the tree)
// with Ext trimmed off.
func (n *TreeNode) NameStem() string {
	return strings.TrimSuffix(n.Name, n.Ext())
}

// DepthStyle is the inline style carrying the node's nesting depth as a CSS
// custom property the stylesheet multiplies into a left-indent. Typed
// template.CSS so html/template injects it verbatim (a bare {{.Depth}} in a
// style="" attribute trips the CSS-context filter).
func (n *TreeNode) DepthStyle() template.CSS {
	return template.CSS(fmt.Sprintf("--depth:%d", n.Depth))
}

// FileTree builds the directory tree from the scoped file list (so the
// Changed/All scope toggle applies exactly as it does to FilteredFiles). The
// search filter is intentionally NOT applied here: the template shows the flat
// FilteredFiles list while a filter is active and the tree only when it's empty.
//
// Zero-arg so the framework pre-computes it once per render and the template can
// range `$.FileTree`. Returns the top-level nodes (depth 0).
func (s PrereviewState) FileTree() []*TreeNode {
	files := s.scopedFiles()

	root := &TreeNode{IsDir: true}
	// dirIndex maps a directory's full path to its node so repeated path
	// prefixes reuse the same directory instead of duplicating it.
	dirIndex := map[string]*TreeNode{"": root}

	for i := range files {
		f := files[i]
		segments := strings.Split(f.Path, "/")

		// Walk/create the directory chain for every segment but the last.
		parent := root
		prefix := ""
		for _, seg := range segments[:len(segments)-1] {
			if prefix == "" {
				prefix = seg
			} else {
				prefix += "/" + seg
			}
			node, ok := dirIndex[prefix]
			if !ok {
				node = &TreeNode{Name: seg, Path: prefix, IsDir: true}
				dirIndex[prefix] = node
				parent.Children = append(parent.Children, node)
			}
			parent = node
		}

		// Attach the file leaf. Copy the entry so &entry is stable per leaf.
		entry := files[i]
		parent.Children = append(parent.Children, &TreeNode{
			Name:         segments[len(segments)-1],
			Path:         f.Path,
			Entry:        &entry,
			IsSelected:   f.Path == s.SelectedFile,
			IsViewed:     s.ViewedFiles[f.Path],
			HasChanged:   f.Status != "",
			CommentCount: f.CommentCount,
			Added:        f.Added,
			Deleted:      f.Deleted,
		})
	}

	// Sort, assign depth, and roll up counts. The sentinel root is passed
	// depth -1 so its children (the top-level entries) land at depth 0; root's
	// own aggregate fields are computed but unused (only Children is returned).
	finalizeTreeNode(root, -1, s.SelectedFile)
	return root.Children
}

// FilteredFileNodes wraps FilteredFiles as flat leaf TreeNodes so the flat
// (filter-active) list and the tree can share one `fileRow` template. Name is
// the full path here — the flat list shows full paths so a search reveals where
// each match lives; the tree sets Name to just the final segment.
//
// Zero-arg so the template can range `$.FilteredFileNodes`.
func (s PrereviewState) FilteredFileNodes() []*TreeNode {
	files := s.FilteredFiles()
	out := make([]*TreeNode, 0, len(files))
	for i := range files {
		f := files[i]
		entry := files[i]
		out = append(out, &TreeNode{
			Name:       f.Path, // full path label for the flat list
			Path:       f.Path,
			Entry:      &entry,
			IsSelected: f.Path == s.SelectedFile,
			IsViewed:   s.ViewedFiles[f.Path],
		})
	}
	return out
}

// finalizeTreeNode sorts a node's children (directories first, then files, each
// case-insensitive alphabetical), recurses to assign depth, and rolls up the
// subtree counts post-order.
func finalizeTreeNode(node *TreeNode, depth int, selected string) {
	node.Depth = depth

	sort.SliceStable(node.Children, func(i, j int) bool {
		a, b := node.Children[i], node.Children[j]
		if a.IsDir != b.IsDir {
			return a.IsDir // directories before files
		}
		la, lb := strings.ToLower(a.Name), strings.ToLower(b.Name)
		if la != lb {
			return la < lb
		}
		return a.Name < b.Name // stable tiebreak for case-only differences
	})

	for _, c := range node.Children {
		finalizeTreeNode(c, depth+1, selected)
		node.CommentCount += c.CommentCount
		if c.Added > 0 {
			node.Added += c.Added
		}
		if c.Deleted > 0 {
			node.Deleted += c.Deleted
		}
		node.HasChanged = node.HasChanged || c.HasChanged
	}

	if node.IsDir && node.Path != "" {
		// Auto-expand directories that contain a changed file, plus the chain
		// down to the selected file (which may be unchanged in All-files scope).
		node.DefaultOpen = node.HasChanged ||
			(selected != "" && strings.HasPrefix(selected, node.Path+"/"))
	}
}
