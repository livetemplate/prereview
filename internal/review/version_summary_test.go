package review

import "testing"

// TestVersionSummary: the "what changed" for a version is the selected file's diff
// between its blob at this checkpoint and the previous one (#155) — computed on
// demand from the content-addressed blobs, with the no-predecessor and tombstone
// edges handled.
func TestVersionSummary(t *testing.T) {
	s := newTestStore(t)
	work := t.TempDir()

	a := writeRef(t, work, "a.txt", "hello\n")
	cp0, _, err := s.Checkpoint([]FileRef{a}, VersionTriggerBaseline, "")
	if err != nil {
		t.Fatalf("baseline: %v", err)
	}
	a = writeRef(t, work, "a.txt", "hello\nworld\n")
	cp1, _, err := s.Checkpoint([]FileRef{a}, VersionTriggerLLMDone, "")
	if err != nil {
		t.Fatalf("edit: %v", err)
	}

	c := &PrereviewController{Versions: s}
	sha0, sha1 := cp0.Files[0].SHA, cp1.Files[0].SHA

	// No predecessor → Initial.
	if sum := c.versionSummary("a.txt", "", sha0); !sum.Initial {
		t.Errorf("baseline should be Initial; got %+v", sum)
	}
	// baseline → edit added one line ("world").
	sum := c.versionSummary("a.txt", sha0, sha1)
	if sum.Added != 1 || sum.Removed != 0 {
		t.Errorf("summary stat = +%d −%d, want +1 −0", sum.Added, sum.Removed)
	}
	// Empty newerSHA (tombstone) → Deleted.
	if sum := c.versionSummary("a.txt", sha1, ""); !sum.Deleted {
		t.Errorf("empty newerSHA should be Deleted; got %+v", sum)
	}
}

// TestChangelogCapture: the agent's done-message becomes the version's changelog —
// but ONLY when the checkpoint actually creates. A no-op done (unchanged scope) makes
// no version, and its message must NOT orphan onto the next real version (#155).
func TestChangelogCapture(t *testing.T) {
	s := newTestStore(t)
	work := t.TempDir()
	a := writeRef(t, work, "a.txt", "one\n")
	s.Checkpoint([]FileRef{a}, VersionTriggerBaseline, "")

	// A real edit + done message → the new version carries the changelog.
	a = writeRef(t, work, "a.txt", "two\n")
	cp1, created, _ := s.Checkpoint([]FileRef{a}, VersionTriggerLLMDone, "Renamed one to two")
	if !created || cp1.Changelog != "Renamed one to two" {
		t.Fatalf("edit checkpoint should carry the changelog; created=%v summary=%q", created, cp1.Changelog)
	}

	// A no-op done with a message → NO checkpoint; the message is discarded, not orphaned.
	if _, created, _ := s.Checkpoint([]FileRef{a}, VersionTriggerLLMDone, "Orphan message"); created {
		t.Fatal("an unchanged scope must not create a checkpoint")
	}

	// The next real edit carries its OWN message, not the orphaned one.
	a = writeRef(t, work, "a.txt", "three\n")
	cp2, _, _ := s.Checkpoint([]FileRef{a}, VersionTriggerLLMDone, "Renamed two to three")
	if cp2.Changelog != "Renamed two to three" {
		t.Fatalf("next edit should carry its own changelog, not the orphan; got %q", cp2.Changelog)
	}

	// FileHistory surfaces each version's changelog (baseline has none).
	hist, _ := s.FileHistory("a.txt")
	if len(hist) != 3 || hist[0].Changelog != "" || hist[1].Changelog != "Renamed one to two" || hist[2].Changelog != "Renamed two to three" {
		t.Errorf("FileHistory changelogs wrong: %+v", hist)
	}
}
