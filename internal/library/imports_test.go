package library

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/livetemplate/prereview/csv"
)

// TestLearner_SkipsImported: a comment imported from a remote copy (#209) was
// written by someone else, so it counts toward no learning and can't be cited.
func TestLearner_SkipsImported(t *testing.T) {
	store := seedStore(t, t.TempDir()) // c1, the reviewer's, plus one reply
	at := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	writeComments(t, store,
		csv.Row{ID: "c1", File: "README.md", FromLine: 3, ToLine: 3, Body: "Drop 'basically'", CreatedAt: at},
		csv.Row{ID: "i1", File: "README.md", FromLine: 4, ToLine: 4, Body: "a teammate's note", CreatedAt: at})
	before := ReviewerSignals(store)
	write(t, filepath.Join(store, ImportLedgerFile),
		`{"remote_id":"AAA1","comment_id":"i1","source":"Google Docs","author":"Alice","at":"2026-11-02T10:00:00Z"}`+"\n")
	if got := ReviewerSignals(store); got != before-1 {
		t.Errorf("signals with i1 imported = %d, want %d", got, before-1)
	}
	if _, err := Resolve(store + ":i1"); err == nil || !strings.Contains(err.Error(), "imported from Google Docs") {
		t.Errorf("citing an imported comment: %v", err)
	}
	if _, err := Resolve(store + ":c1"); err != nil {
		t.Errorf("the reviewer's own comment: %v", err)
	}
}

// TestLearner_SkipsImportedReplies: a reply imported from a remote copy is a
// reviewer entry in the thread, but someone else's: not counted, not citable.
func TestLearner_SkipsImportedReplies(t *testing.T) {
	store := seedStore(t, t.TempDir()) // one reviewer reply on c1 at 1762000000000000000
	before := ReviewerSignals(store)
	f, err := os.OpenFile(filepath.Join(store, ReviewerRepliesFile), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(`{"target_id":"c1","author":"reviewer","body":"from Bob","at":1763000000000000000,"name":"Bob","remote_id":"y1"}` + "\n")
	f.Close()
	if got := ReviewerSignals(store); got != before {
		t.Errorf("signals %d, want %d: the imported reply counted", got, before)
	}
	if _, err := Resolve(store + ":c1@1763000000000000000"); err == nil {
		t.Error("an imported reply was accepted as evidence")
	}
	if _, err := Resolve(store + ":c1@1762000000000000000"); err != nil {
		t.Errorf("the reviewer's own reply: %v", err)
	}
}
