package review

import (
	"testing"

	"github.com/livetemplate/prereview/gitdiff"
)

// findChild returns the direct child of nodes with the given name, or nil.
func findChild(nodes []*TreeNode, name string) *TreeNode {
	for _, n := range nodes {
		if n.Name == name {
			return n
		}
	}
	return nil
}

func TestFileTree_NestingAndDepth(t *testing.T) {
	s := PrereviewState{
		ShowAllFiles: true,
		Files: []gitdiff.FileEntry{
			{Path: "cmd/main.go", Status: "M"},
			{Path: "internal/review/state.go", Status: "M"},
			{Path: "README.md"},
		},
	}
	tree := s.FileTree()

	cmd := findChild(tree, "cmd")
	if cmd == nil || !cmd.IsDir || cmd.Path != "cmd" || cmd.Depth != 0 {
		t.Fatalf("cmd dir not built correctly: %+v", cmd)
	}
	main := findChild(cmd.Children, "main.go")
	if main == nil || main.IsDir || main.Depth != 1 || main.Path != "cmd/main.go" {
		t.Fatalf("cmd/main.go leaf not built correctly: %+v", main)
	}
	deep := findChild(findChild(findChild(tree, "internal").Children, "review").Children, "state.go")
	if deep == nil || deep.Depth != 2 {
		t.Fatalf("deep leaf depth wrong: %+v", deep)
	}
}

func TestFileTree_DirsFirstAlphabetical(t *testing.T) {
	s := PrereviewState{
		ShowAllFiles: true,
		Files: []gitdiff.FileEntry{
			{Path: "zeta.go"},
			{Path: "alpha/a.go"},
			{Path: "beta.go"},
			{Path: "Beta/b.go"},
		},
	}
	tree := s.FileTree()
	// Expect dirs first (alpha, Beta — case-insensitive), then files (beta.go, zeta.go).
	wantOrder := []struct {
		name  string
		isDir bool
	}{
		{"alpha", true},
		{"Beta", true},
		{"beta.go", false},
		{"zeta.go", false},
	}
	if len(tree) != len(wantOrder) {
		t.Fatalf("got %d top-level nodes, want %d: %+v", len(tree), len(wantOrder), tree)
	}
	for i, w := range wantOrder {
		if tree[i].Name != w.name || tree[i].IsDir != w.isDir {
			t.Errorf("position %d: got (%s, dir=%v), want (%s, dir=%v)",
				i, tree[i].Name, tree[i].IsDir, w.name, w.isDir)
		}
	}
}

func TestFileTree_RollUps(t *testing.T) {
	s := PrereviewState{
		ShowAllFiles: true,
		Files: []gitdiff.FileEntry{
			{Path: "pkg/a.go", Status: "M", Added: 10, Deleted: 2, CommentCount: 1},
			{Path: "pkg/sub/b.go", Status: "M", Added: 5, Deleted: 0, CommentCount: 2},
			{Path: "pkg/bin.dat", Status: "M", Added: -1, Deleted: -1, CommentCount: 0}, // binary
		},
	}
	tree := s.FileTree()
	pkg := findChild(tree, "pkg")
	if pkg == nil {
		t.Fatal("pkg dir missing")
	}
	if pkg.CommentCount != 3 {
		t.Errorf("pkg CommentCount = %d, want 3", pkg.CommentCount)
	}
	// Added/Deleted clamp the binary file's -1 to 0: 10+5+0 / 2+0+0.
	if pkg.Added != 15 || pkg.Deleted != 2 {
		t.Errorf("pkg Added/Deleted = %d/%d, want 15/2", pkg.Added, pkg.Deleted)
	}
	if !pkg.HasChanged {
		t.Error("pkg HasChanged = false, want true")
	}
}

func TestFileTree_DefaultOpen(t *testing.T) {
	s := PrereviewState{
		ShowAllFiles: true,
		SelectedFile: "docs/guide/intro.md",
		Files: []gitdiff.FileEntry{
			{Path: "changed/x.go", Status: "M"},      // changed → dir auto-open
			{Path: "quiet/y.go"},                       // unchanged, not selected → closed
			{Path: "docs/guide/intro.md"},              // unchanged but selected → path opens
			{Path: "docs/other.md"},                    // sibling, unchanged → closed
		},
	}
	tree := s.FileTree()

	if c := findChild(tree, "changed"); c == nil || !c.DefaultOpen {
		t.Errorf("changed dir DefaultOpen = %v, want true", c.DefaultOpen)
	}
	if c := findChild(tree, "quiet"); c == nil || c.DefaultOpen {
		t.Errorf("quiet dir DefaultOpen = %v, want false", c.DefaultOpen)
	}
	docs := findChild(tree, "docs")
	if docs == nil || !docs.DefaultOpen {
		t.Errorf("docs dir DefaultOpen = %v, want true (path to selected)", docs.DefaultOpen)
	}
	guide := findChild(docs.Children, "guide")
	if guide == nil || !guide.DefaultOpen {
		t.Errorf("docs/guide DefaultOpen = %v, want true (contains selected)", guide.DefaultOpen)
	}
}

func TestFileTree_SelectedAndViewed(t *testing.T) {
	s := PrereviewState{
		ShowAllFiles: true,
		SelectedFile: "a/sel.go",
		ViewedFiles:  map[string]bool{"a/seen.go": true},
		Files: []gitdiff.FileEntry{
			{Path: "a/sel.go", Status: "M"},
			{Path: "a/seen.go", Status: "M"},
		},
	}
	a := findChild(s.FileTree(), "a")
	sel := findChild(a.Children, "sel.go")
	seen := findChild(a.Children, "seen.go")
	if !sel.IsSelected || sel.IsViewed {
		t.Errorf("sel.go: IsSelected=%v IsViewed=%v, want true/false", sel.IsSelected, sel.IsViewed)
	}
	if seen.IsSelected || !seen.IsViewed {
		t.Errorf("seen.go: IsSelected=%v IsViewed=%v, want false/true", seen.IsSelected, seen.IsViewed)
	}
}

func TestFileTree_FlatRepoNoFolders(t *testing.T) {
	s := PrereviewState{
		ShowAllFiles: true,
		Files: []gitdiff.FileEntry{
			{Path: "main.go", Status: "M"},
			{Path: "go.mod"},
		},
	}
	tree := s.FileTree()
	if len(tree) != 2 {
		t.Fatalf("flat repo: got %d nodes, want 2", len(tree))
	}
	for _, n := range tree {
		if n.IsDir || n.Depth != 0 {
			t.Errorf("flat node %s: IsDir=%v Depth=%d, want file at depth 0", n.Name, n.IsDir, n.Depth)
		}
	}
}

func TestFileTree_SingleFile(t *testing.T) {
	s := PrereviewState{
		ShowAllFiles: true,
		Files:        []gitdiff.FileEntry{{Path: "only.txt", Status: "A"}},
	}
	tree := s.FileTree()
	if len(tree) != 1 || tree[0].Name != "only.txt" || tree[0].IsDir {
		t.Fatalf("single file tree wrong: %+v", tree)
	}
}

func TestFileTree_Empty(t *testing.T) {
	s := PrereviewState{ShowAllFiles: true}
	if got := s.FileTree(); len(got) != 0 {
		t.Fatalf("empty repo: got %d nodes, want 0", len(got))
	}
}

func TestFileTree_ChangedScopeExcludesUnchanged(t *testing.T) {
	// Default scope (ShowAllFiles=false) should drop unchanged files, so a dir
	// that only holds unchanged files disappears from the tree entirely.
	s := PrereviewState{
		Files: []gitdiff.FileEntry{
			{Path: "src/changed.go", Status: "M"},
			{Path: "vendor/lib.go"}, // unchanged
		},
	}
	tree := s.FileTree()
	if findChild(tree, "vendor") != nil {
		t.Error("vendor dir should be excluded in changed-only scope")
	}
	if findChild(tree, "src") == nil {
		t.Error("src dir (has changed file) should be present")
	}
}
