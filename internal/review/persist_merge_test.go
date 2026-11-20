package review

import (
	"os"
	"slices"
	"testing"
	"time"
)

// commentIDs is the ids on disk, in file order.
func diskIDs(c *PrereviewController) []string {
	var ids []string
	for _, cm := range c.loadCommentsFromDisk() {
		ids = append(ids, cm.ID)
	}
	return ids
}

func testComment(id string) Comment {
	return Comment{ID: id, File: "a.md", FromLine: 1, ToLine: 1, Side: "new", Body: id, Kind: commentKindLine,
		Created: time.Now().UTC().Truncate(time.Second)}
}

// TestPersist_KeepsUnknownRows: a save from a tab's copy keeps the rows that
// copy never loaded (another device's tab, an import), in file order; only a
// comment deleted on this server goes, and Undo brings it back.
func TestPersist_KeepsUnknownRows(t *testing.T) {
	c := checksController(t)
	phone := PrereviewState{Comments: c.loadCommentsFromDisk()} // both tabs open on an empty review
	desktop := PrereviewState{Comments: c.loadCommentsFromDisk()}

	phone.Comments = append(phone.Comments, testComment("p1"))
	if err := c.persist(phone.Comments); err != nil {
		t.Fatal(err)
	}
	desktop.Comments = append(desktop.Comments, testComment("d1"))
	if err := c.persist(desktop.Comments); err != nil {
		t.Fatal(err)
	}
	if got := diskIDs(c); !slices.Equal(got, []string{"p1", "d1"}) {
		t.Fatalf("on disk %v, want [p1 d1]: the desktop's save deleted the phone's comment", got)
	}

	// A delete on the desktop removes p1 for good, though the phone still holds it.
	desktop.Comments = c.loadCommentsFromDisk()
	desktop, err := c.DeleteComment(desktop, checksCtx("deleteComment", map[string]interface{}{"id": "p1"}))
	if err != nil {
		t.Fatal(err)
	}
	phone.Comments[0].Resolved = true // the phone's stale copy still has p1
	if err := c.persist(phone.Comments); err != nil {
		t.Fatal(err)
	}
	if got := diskIDs(c); !slices.Equal(got, []string{"d1"}) {
		t.Fatalf("after the delete and a stale save: %v, want [d1]", got)
	}

	// Undo brings it back, last.
	desktop, err = c.UndoDelete(desktop, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := diskIDs(c); !slices.Equal(got, []string{"d1", "p1"}) {
		t.Errorf("after Undo: %v, want [d1 p1]", got)
	}
}

// TestPersist_UnreadableFileFails: a save can't merge with a file it can't read,
// so it fails and leaves the file alone instead of writing the tab's rows only.
func TestPersist_UnreadableFileFails(t *testing.T) {
	c := checksController(t)
	bad := []byte("id,file\n\"unterminated\n")
	if err := os.WriteFile(c.CSVPath, bad, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := c.persist([]Comment{testComment("mine")}); err == nil {
		t.Fatal("persist over an unreadable file succeeded")
	}
	if got, _ := os.ReadFile(c.CSVPath); string(got) != string(bad) {
		t.Errorf("the file changed: %q", got)
	}
}
