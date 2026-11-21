package review

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/livetemplate/prereview/csv"
)

// newCombinedController is a #114 combined session: a repo review that also
// fronts a live site, so the proxy fields are set and ExternalMode is not.
func newCombinedController(t *testing.T) *PrereviewController {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	csvPath := filepath.Join(dir, ".prereview", "comments.csv")
	if err := os.MkdirAll(filepath.Dir(csvPath), 0o755); err != nil {
		t.Fatal(err)
	}
	return &PrereviewController{
		RepoPath:     dir,
		NoGit:        true,
		ProxyBaseURL: "http://127.0.0.1:9999/",
		TargetURL:    "http://localhost:3000",
		CSVPath:      csvPath,
		CSVWriter:    csv.NewWriter(csvPath),
	}
}

// TestMount_CombinedSetsLiveSurface pins decision 10: Mount mirrors the live
// surface from the controller on every connect, keeps the repo path (files
// load), and clears a persisted ExternalMode from an earlier live-only launch.
func TestMount_CombinedSetsLiveSurface(t *testing.T) {
	c := newCombinedController(t)
	st, err := c.Mount(PrereviewState{ExternalMode: true, ProxyBaseURL: "http://127.0.0.1:1/", LiveView: true}, nil)
	if err != nil {
		t.Fatalf("Mount: %v", err)
	}
	if st.ExternalMode {
		t.Error("ExternalMode must stay false in a combined session (it means live-only)")
	}
	if !st.LiveAvailable {
		t.Error("LiveAvailable must be true when the controller has a proxy")
	}
	if st.ProxyBaseURL != c.ProxyBaseURL || st.TargetURL != c.TargetURL {
		t.Errorf("proxy fields = %q/%q, want the controller's %q/%q", st.ProxyBaseURL, st.TargetURL, c.ProxyBaseURL, c.TargetURL)
	}
	if !st.LiveView {
		t.Error("a persisted LiveView must survive a reconnect")
	}
	if len(st.Files) != 1 || st.SelectedFile != "a.md" {
		t.Errorf("the repo path must still run: files=%v selected=%q", st.Files, st.SelectedFile)
	}
}

// TestMount_RepoClearsStaleLive pins the other half of decision 10: a plain repo
// review drops a persisted live surface rather than rendering an iframe at a
// proxy that no longer exists.
func TestMount_RepoClearsStaleLive(t *testing.T) {
	c := newCombinedController(t)
	c.ProxyBaseURL, c.TargetURL = "", ""
	st, err := c.Mount(PrereviewState{ExternalMode: true, ProxyBaseURL: "http://127.0.0.1:1/", LiveView: true}, nil)
	if err != nil {
		t.Fatalf("Mount: %v", err)
	}
	if st.ExternalMode || st.LiveAvailable || st.LiveView || st.ProxyBaseURL != "" {
		t.Errorf("stale live state survived: external=%v available=%v view=%v proxy=%q",
			st.ExternalMode, st.LiveAvailable, st.LiveView, st.ProxyBaseURL)
	}
}
