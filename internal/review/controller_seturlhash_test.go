package review

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/livetemplate/livetemplate"
)

// TestSetURLHash_UnrecognisedPath_NoOp pins the load-bearing
// assumption the client directive's looksLikeDeepLinkHash heuristic
// relies on: any hash whose path doesn't resolve to a known file
// must leave state untouched. The directive dispatches setURLHash
// for any hash containing `:`, `/`, or `.` — so an HTML heading id
// like `#v1.0.0` or `#api/users` (false positives by design) will
// reach this action. Without the no-op guarantee, those would
// silently mutate user state.
func TestSetURLHash_UnrecognisedPath_NoOp(t *testing.T) {
	c := &PrereviewController{RepoPath: t.TempDir(), Base: "HEAD"}
	initial := PrereviewState{
		SelectedFile:    "real-file.go",
		SelectionAnchor: 5,
		SelectionEnd:    5,
		SelectionSide:   "new",
	}

	for _, hash := range []string{
		"v1.0.0",       // heading id resembling a version
		"api/users",    // nested heading id
		"some-file.go", // file path that doesn't exist in the (empty) repo
	} {
		t.Run(hash, func(t *testing.T) {
			ctx := livetemplate.NewContext(context.TODO(), "setURLHash",
				map[string]interface{}{"hash": hash})
			out, err := c.SetURLHash(initial, ctx)
			if err != nil {
				t.Fatalf("SetURLHash(%q) returned err: %v", hash, err)
			}
			// State must be byte-equal to the input — no SelectedFile
			// change, no selection reset.
			if out.SelectedFile != initial.SelectedFile {
				t.Errorf("SelectedFile changed: %q → %q", initial.SelectedFile, out.SelectedFile)
			}
			if out.SelectionAnchor != initial.SelectionAnchor || out.SelectionEnd != initial.SelectionEnd {
				t.Errorf("selection range changed: (%d,%d) → (%d,%d)",
					initial.SelectionAnchor, initial.SelectionEnd, out.SelectionAnchor, out.SelectionEnd)
			}
		})
	}
}

// TestSetURLHash_EmptyHash_NoOp pins that the initial-load case
// (directive fires with an empty hash because location.hash is
// empty) doesn't mutate state.
func TestSetURLHash_EmptyHash_NoOp(t *testing.T) {
	c := &PrereviewController{RepoPath: t.TempDir(), Base: "HEAD"}
	initial := PrereviewState{SelectedFile: "kept.go"}
	ctx := livetemplate.NewContext(context.TODO(), "setURLHash",
		map[string]interface{}{"hash": ""})
	out, err := c.SetURLHash(initial, ctx)
	if err != nil {
		t.Fatalf("SetURLHash(\"\") returned err: %v", err)
	}
	if out.SelectedFile != "kept.go" {
		t.Errorf("SelectedFile mutated on empty hash: %q", out.SelectedFile)
	}
}

// TestSetURLHash_RefreshesVersionList pins that navigating to a file via a
// deep link repopulates the Versions panel with THAT file's timeline, not the
// previously-selected file's. Regression guard for the bug where the deep-link
// handler set SelectedFile/CurrentDiff but skipped applyVersionList, so a
// permalink-opened file showed the mount-default file's version history.
//
// The two files MUST have different history lengths, because the bug hides
// whenever every file has one version — the stale and correct lists coincide.
// mount-default (aaa.txt) has 1 version; the linked file (zzz.txt) has 2.
func TestSetURLHash_RefreshesVersionList(t *testing.T) {
	work := t.TempDir()
	store, err := NewVersionStore(filepath.Join(work, ".prereview", "versions"))
	if err != nil {
		t.Fatalf("NewVersionStore: %v", err)
	}
	def := writeRef(t, work, "aaa.txt", "default file, one version")
	tgt := writeRef(t, work, "zzz.txt", "linked file, v0")

	// seq 0: baseline — both files at v0.
	if _, _, err := store.Checkpoint([]FileRef{def, tgt}, VersionTriggerBaseline, ""); err != nil {
		t.Fatalf("baseline checkpoint: %v", err)
	}
	// seq 1: only zzz.txt changes → aaa.txt dedups to 1 entry, zzz.txt gets 2.
	if err := os.WriteFile(tgt.AbsPath, []byte("linked file, v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Checkpoint([]FileRef{def, tgt}, VersionTriggerLLMDone, "reworked"); err != nil {
		t.Fatalf("edit checkpoint: %v", err)
	}

	c := &PrereviewController{RepoPath: work, NoGit: true, Versions: store}

	// Start as Mount leaves it: mount-default file selected, its 1-entry timeline loaded.
	state := PrereviewState{SelectedFile: "aaa.txt"}
	c.applyVersionList(&state)
	if len(state.Versions) != 1 {
		t.Fatalf("precondition: aaa.txt should have 1 version, got %d", len(state.Versions))
	}

	// Deep-link to zzz.txt.
	ctx := livetemplate.NewContext(context.TODO(), "setURLHash",
		map[string]interface{}{"hash": "zzz.txt"})
	out, err := c.SetURLHash(state, ctx)
	if err != nil {
		t.Fatalf("SetURLHash(zzz.txt): %v", err)
	}
	if out.SelectedFile != "zzz.txt" {
		t.Fatalf("SelectedFile = %q, want zzz.txt", out.SelectedFile)
	}
	// The discriminating assertion: the panel now reflects zzz.txt (2 versions),
	// not the stale aaa.txt list (1). Fails on the pre-fix code.
	if len(out.Versions) != 2 {
		t.Errorf("Versions has %d entries after deep-link, want 2 (zzz.txt's timeline); stale aaa.txt list not refreshed", len(out.Versions))
	}
}
