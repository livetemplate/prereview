package review

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// newTestStore returns a store rooted in a temp dir with a deterministic clock
// so checkpoint timestamps are reproducible.
func newTestStore(t *testing.T) *VersionStore {
	t.Helper()
	s, err := NewVersionStore(filepath.Join(t.TempDir(), ".prereview", "versions"))
	if err != nil {
		t.Fatalf("NewVersionStore: %v", err)
	}
	var tick int
	s.now = func() time.Time {
		tick++
		return time.Date(2026, 1, 1, 0, 0, tick, 0, time.UTC)
	}
	return s
}

// writeFile writes content to dir/name and returns a FileRef keyed by name.
func writeRef(t *testing.T, dir, name, content string) FileRef {
	t.Helper()
	abs := filepath.Join(dir, name)
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return FileRef{Path: name, AbsPath: abs}
}

func TestCheckpoint_BaselineAndDedup(t *testing.T) {
	s := newTestStore(t)
	work := t.TempDir()
	a := writeRef(t, work, "a.txt", "hello")

	// Baseline always lands even though it's the first (nothing to compare).
	cp, created, err := s.Checkpoint([]FileRef{a}, VersionTriggerBaseline)
	if err != nil || !created {
		t.Fatalf("baseline: created=%v err=%v", created, err)
	}
	if cp.Seq != 0 || cp.Trigger != VersionTriggerBaseline || len(cp.Files) != 1 {
		t.Fatalf("unexpected baseline checkpoint: %+v", cp)
	}
	firstSHA := cp.Files[0].SHA
	if firstSHA == "" {
		t.Fatal("expected a content sha, got tombstone")
	}

	// Re-checkpoint with no change → skipped (created=false), no new blob, no
	// new timeline entry.
	_, created, err = s.Checkpoint([]FileRef{a}, VersionTriggerLLMDone)
	if err != nil || created {
		t.Fatalf("no-op checkpoint should be skipped: created=%v err=%v", created, err)
	}
	cps, _ := s.Checkpoints()
	if len(cps) != 1 {
		t.Fatalf("expected 1 checkpoint after no-op, got %d", len(cps))
	}
	blobs, _ := os.ReadDir(filepath.Join(s.root, "blobs"))
	if len(blobs) != 1 {
		t.Fatalf("dedup failed: expected 1 blob, got %d", len(blobs))
	}

	// Change the content → new checkpoint, new blob, prev_sha threaded.
	if err := os.WriteFile(a.AbsPath, []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}
	cp2, created, err := s.Checkpoint([]FileRef{a}, VersionTriggerLLMDone)
	if err != nil || !created {
		t.Fatalf("changed checkpoint: created=%v err=%v", created, err)
	}
	if cp2.Seq != 1 || cp2.Files[0].SHA == firstSHA {
		t.Fatalf("expected a new sha distinct from v0: %+v (v0 sha %s)", cp2.Files[0], firstSHA)
	}
	blobs, _ = os.ReadDir(filepath.Join(s.root, "blobs"))
	if len(blobs) != 2 {
		t.Fatalf("expected 2 blobs after edit, got %d", len(blobs))
	}
}

func TestCheckpoint_DedupAcrossFiles(t *testing.T) {
	s := newTestStore(t)
	work := t.TempDir()
	a := writeRef(t, work, "a.txt", "same")
	b := writeRef(t, work, "b.txt", "same") // identical content → shares one blob

	if _, created, err := s.Checkpoint([]FileRef{a, b}, VersionTriggerBaseline); err != nil || !created {
		t.Fatalf("checkpoint: created=%v err=%v", created, err)
	}
	blobs, _ := os.ReadDir(filepath.Join(s.root, "blobs"))
	if len(blobs) != 1 {
		t.Fatalf("identical files should share one blob, got %d", len(blobs))
	}
}

func TestRestore_ReturnsExactBytes(t *testing.T) {
	s := newTestStore(t)
	work := t.TempDir()
	a := writeRef(t, work, "a.txt", "v0-content")
	s.Checkpoint([]FileRef{a}, VersionTriggerBaseline)

	os.WriteFile(a.AbsPath, []byte("v1-content"), 0o644)
	s.Checkpoint([]FileRef{a}, VersionTriggerLLMDone)

	got, err := s.Restore("a.txt", 0)
	if err != nil {
		t.Fatalf("restore v0: %v", err)
	}
	if string(got) != "v0-content" {
		t.Fatalf("restore v0 = %q, want v0-content", got)
	}
	got, err = s.Restore("a.txt", 1)
	if err != nil || string(got) != "v1-content" {
		t.Fatalf("restore v1 = %q err=%v", got, err)
	}

	if _, err := s.Restore("a.txt", 5); err == nil {
		t.Fatal("restore out-of-range seq should error")
	}
	if _, err := s.Restore("missing.txt", 0); err == nil {
		t.Fatal("restore unknown path should error")
	}
}

func TestCheckpoint_Tombstone(t *testing.T) {
	s := newTestStore(t)
	work := t.TempDir()
	a := writeRef(t, work, "a.txt", "will be deleted")
	b := writeRef(t, work, "b.txt", "survives")
	s.Checkpoint([]FileRef{a, b}, VersionTriggerBaseline)

	// LLM deletes a.txt. A missing ref must NOT fail the whole checkpoint — it's
	// recorded as a tombstone, and b's change still lands.
	if err := os.Remove(a.AbsPath); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(b.AbsPath, []byte("edited"), 0o644)
	cp, created, err := s.Checkpoint([]FileRef{a, b}, VersionTriggerLLMDone)
	if err != nil || !created {
		t.Fatalf("checkpoint after delete: created=%v err=%v", created, err)
	}
	var aFV FileVersion
	for _, fv := range cp.Files {
		if fv.Path == "a.txt" {
			aFV = fv
		}
	}
	if aFV.SHA != "" {
		t.Fatalf("deleted file should be a tombstone (empty sha), got %q", aFV.SHA)
	}
	// Restoring the tombstone version signals deletion, not bytes.
	if _, err := s.Restore("a.txt", 1); err != ErrVersionTombstone {
		t.Fatalf("restore tombstone: want ErrVersionTombstone, got %v", err)
	}
	// The pre-deletion version is still restorable.
	if got, err := s.Restore("a.txt", 0); err != nil || string(got) != "will be deleted" {
		t.Fatalf("restore pre-delete = %q err=%v", got, err)
	}
}

func TestFileHistory_SubsequenceOfChanges(t *testing.T) {
	s := newTestStore(t)
	work := t.TempDir()
	a := writeRef(t, work, "a.txt", "one")
	b := writeRef(t, work, "b.txt", "static")
	s.Checkpoint([]FileRef{a, b}, VersionTriggerBaseline) // seq 0: a=one

	os.WriteFile(a.AbsPath, []byte("two"), 0o644)
	s.Checkpoint([]FileRef{a, b}, VersionTriggerLLMDone) // seq 1: a=two (b unchanged)

	os.WriteFile(a.AbsPath, []byte("three"), 0o644)
	s.Checkpoint([]FileRef{a, b}, VersionTriggerLLMDone) // seq 2: a=three

	hist, err := s.FileHistory("a.txt")
	if err != nil {
		t.Fatalf("FileHistory: %v", err)
	}
	if len(hist) != 3 {
		t.Fatalf("a.txt should have 3 versions, got %d: %+v", len(hist), hist)
	}
	if hist[0].Seq != 0 || hist[2].Seq != 2 {
		t.Fatalf("history seqs off: %+v", hist)
	}

	// b.txt never changed after baseline → exactly one version.
	histB, _ := s.FileHistory("b.txt")
	if len(histB) != 1 || histB[0].Seq != 0 {
		t.Fatalf("b.txt should have 1 version at seq 0, got %+v", histB)
	}
}

func TestVersionStore_PersistsAcrossReopen(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".prereview", "versions")
	work := t.TempDir()
	a := writeRef(t, work, "a.txt", "durable")

	s1, _ := NewVersionStore(root)
	s1.Checkpoint([]FileRef{a}, VersionTriggerBaseline)

	// Reopen (simulating a server restart): history must survive.
	s2, err := NewVersionStore(root)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	cps, _ := s2.Checkpoints()
	if len(cps) != 1 {
		t.Fatalf("history lost across reopen: %d checkpoints", len(cps))
	}
	if got, err := s2.Restore("a.txt", 0); err != nil || string(got) != "durable" {
		t.Fatalf("restore after reopen = %q err=%v", got, err)
	}
}
