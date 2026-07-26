package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/livetemplate/prereview/csv"
	"github.com/livetemplate/prereview/internal/review"
)

func TestResolveStoreRoot(t *testing.T) {
	// --out empty → default review root, verbatim.
	if got, err := resolveStoreRoot("", "/some/repo"); err != nil || got != "/some/repo" {
		t.Errorf("default: got %q err %v, want /some/repo", got, err)
	}
	// --out set → overrides, made absolute (available in every mode, not just
	// --external, so the flag is never silently ignored).
	got, err := resolveStoreRoot("rel/dir", "/some/repo")
	if err != nil {
		t.Fatalf("override: %v", err)
	}
	if !filepath.IsAbs(got) || filepath.Base(got) != "dir" {
		t.Errorf("override: got %q, want an absolute path ending in rel/dir", got)
	}
}

func TestOpenStoreLayout(t *testing.T) {
	root := t.TempDir()
	csvPath, w, err := openStore(storeDirFor(root, ""))
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	if w == nil {
		t.Fatal("openStore returned nil writer")
	}
	wantDir := filepath.Join(root, ".prereview")
	if _, err := os.Stat(wantDir); err != nil {
		t.Errorf(".prereview dir not created: %v", err)
	}
	if filepath.Dir(csvPath) != wantDir || filepath.Base(csvPath) != "comments.csv" {
		t.Errorf("csvPath = %q, want %s/comments.csv", csvPath, wantDir)
	}
}

// TestOpenStoreResetsStatusFile ensures a stale agent-status file from a
// previous session is cleared on launch, so the UI doesn't show a leftover
// "working"/"done" before the agent writes anything this session.
func TestOpenStoreResetsStatusFile(t *testing.T) {
	root := t.TempDir()
	csvPath, _, err := openStore(storeDirFor(root, ""))
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	statusPath := review.LLMStatusPath(csvPath)
	if err := os.WriteFile(statusPath, []byte(`{"state":"working"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := openStore(storeDirFor(root, "")); err != nil {
		t.Fatalf("re-openStore: %v", err)
	}
	if _, err := os.Stat(statusPath); !os.IsNotExist(err) {
		t.Errorf("stale llm-status.json not cleared (stat err = %v)", err)
	}
}

// #199. A single-file review's store must be keyed by the TARGET, not by the
// parent directory it is normalized to — otherwise two files in one directory are
// one review: one store, and one server.pid lock that evicts the other server.
func TestStoreDirFor(t *testing.T) {
	root := "/home/u/plans"

	// A repo / plain-directory / --external review is untouched: <root>/.prereview.
	if got, want := storeDirFor(root, ""), filepath.Join(root, ".prereview"); got != want {
		t.Errorf("storeDirFor(root, \"\") = %q, want %q", got, want)
	}

	// Two files in ONE directory get two stores...
	a := storeDirFor(root, filepath.Join(root, "a.md"))
	b := storeDirFor(root, filepath.Join(root, "b.md"))
	if a == b {
		t.Fatalf("a.md and b.md share the store %q — this IS the bug", a)
	}
	// ...nested inside the directory's own store, so .gitignore and gitdiff's
	// .prereview skip keep covering them.
	if !strings.HasPrefix(a, filepath.Join(root, ".prereview")+string(filepath.Separator)) {
		t.Errorf("target store %q is not under the directory's store", a)
	}
	// The store is stable across launches, or a relaunch loses the comments.
	if again := storeDirFor(root, filepath.Join(root, "a.md")); again != a {
		t.Errorf("storeDirFor is not stable: %q then %q", a, again)
	}
	// Same basename, different directories, one shared --out: still two stores
	// (the slug hashes the absolute target path, not the basename).
	x := storeDirFor("/tmp/out", "/home/u/one/plan.md")
	y := storeDirFor("/tmp/out", "/home/u/two/plan.md")
	if x == y {
		t.Errorf("same-named files in different dirs collided on %q", x)
	}
}

func TestTargetSlug(t *testing.T) {
	// The basename is carried through so `ls .prereview/files/` is readable.
	if got := targetSlug("/home/u/plans/my-plan.md"); !strings.HasPrefix(got, "my-plan.md-") {
		t.Errorf("targetSlug = %q, want the basename as a prefix", got)
	}
	// Names that would break out of the store, hide it, or be rejected by the
	// filesystem are sanitized — but stay unique via the digest.
	for _, name := range []string{"a/b.md", ".env", "..", "wei rd:*?.md", strings.Repeat("x", 300) + ".md"} {
		got := targetSlug(filepath.Join("/root", name))
		if got == "" || strings.ContainsAny(got, `/\:*?"<>| `) || strings.HasPrefix(got, ".") {
			t.Errorf("targetSlug(%q) = %q — not a safe directory name", name, got)
		}
		if len(got) > 64 {
			t.Errorf("targetSlug(%q) = %q — too long for a path segment", name, got)
		}
	}
}

// #199 upgrade path: a reviewer whose comments live in the pre-#199 shared store
// must still see them when the same file is reopened on the new layout.
func TestSeedFromLegacyStore(t *testing.T) {
	root := t.TempDir()
	legacy := filepath.Join(root, ".prereview", "comments.csv")
	mustWriteFile(t, legacy, []byte(
		"id,file,from_line,to_line,side,body,created_at,resolved,anchor,anchor_status,kind,area,url\n"+
			"ca1,a.md,3,3,new,note on a,2026-07-01T12:00:00Z,false,,,line,,\n"+
			"cb1,b.md,3,3,new,note on b,2026-07-01T12:00:00Z,false,,,line,,\n"))

	// The state of those comments lives in id-keyed sidecars. Carrying the comment
	// without them would re-queue work the agent already finished.
	mustWriteFile(t, filepath.Join(root, ".prereview", "processed.jsonl"),
		[]byte("{\"id\":\"ca1\",\"at\":\"2026-07-01T13:00:00Z\"}\n{\"id\":\"cb1\"}\n"))
	mustWriteFile(t, filepath.Join(root, ".prereview", "agent-replies.jsonl"),
		[]byte("{\"target_id\":\"ca1\",\"author\":\"agent\",\"body\":\"renamed it\",\"at\":1}\n"+
			"{\"target_id\":\"cb1\",\"author\":\"agent\",\"body\":\"other file\",\"at\":2}\n"))

	target := filepath.Join(storeDirFor(root, filepath.Join(root, "a.md")), "comments.csv")
	if err := seedFromLegacyStore(target, root, "a.md"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	rows, err := csv.Read(target)
	if err != nil {
		t.Fatalf("read seeded store: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != "ca1" {
		t.Fatalf("seeded %+v, want only a.md's row (the reviewed file's)", rows)
	}
	// A COPY: the shared store may still be a directory review's live store.
	if legacyRows, err := csv.Read(legacy); err != nil || len(legacyRows) != 2 {
		t.Errorf("legacy store mutated: %+v (err %v) — seeding must never delete rows", legacyRows, err)
	}


	// The done marker and the thread came along — filtered to this file's ids.
	store := filepath.Dir(target)
	for _, tc := range []struct{ name, want, exclude string }{
		{"processed.jsonl", "ca1", "cb1"},
		{"agent-replies.jsonl", "renamed it", "other file"},
	} {
		b, err := os.ReadFile(filepath.Join(store, tc.name))
		if err != nil {
			t.Errorf("%s not carried over: %v — a comment that was already done would "+
				"come back as fresh work", tc.name, err)
			continue
		}
		if !strings.Contains(string(b), tc.want) {
			t.Errorf("%s = %q, want it to carry %q", tc.name, b, tc.want)
		}
		if strings.Contains(string(b), tc.exclude) {
			t.Errorf("%s leaked the other file's row %q", tc.name, tc.exclude)
		}
	}
	// Never re-seeds over a store that already has its own rows: the reviewer may
	// have deleted a carried-over comment, and it must not come back.
	if err := csv.NewWriter(target).Write(nil); err != nil {
		t.Fatal(err)
	}
	if err := seedFromLegacyStore(target, root, "a.md"); err != nil {
		t.Fatalf("re-seed: %v", err)
	}
	if rows, _ := csv.Read(target); len(rows) != 0 {
		t.Errorf("re-seeded %d row(s) into a store that already exists", len(rows))
	}
}

func TestSeedFromLegacyStore_NoOps(t *testing.T) {
	root := t.TempDir()
	// A directory / external review IS the shared store — nothing to carry over.
	dirStore := filepath.Join(storeDirFor(root, ""), "comments.csv")
	if err := seedFromLegacyStore(dirStore, root, ""); err != nil {
		t.Fatalf("directory review: %v", err)
	}
	if _, err := os.Stat(dirStore); !os.IsNotExist(err) {
		t.Errorf("seeding created a store for a directory review (stat err = %v)", err)
	}
	// No legacy store at all (the common case from here on): silent no-op.
	target := filepath.Join(storeDirFor(root, filepath.Join(root, "a.md")), "comments.csv")
	if err := seedFromLegacyStore(target, root, "a.md"); err != nil {
		t.Fatalf("no legacy store: %v", err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Errorf("seeding created an empty store (stat err = %v)", err)
	}
}
