//go:build browser

// End-to-end for #199, seen through a real browser.
//
// Reviewing a single file normalizes the review root to the file's PARENT directory,
// and the store — and the "one server per store" pid lock inside it — used to be keyed
// on that directory. So two reviews of DIFFERENT files that happened to sit in one
// directory were the same review: the second launch was refused, or with --replace it
// killed the first, and both fed off one comments.csv. It fired constantly on Claude
// Code plan files, which all live in ~/.claude/plans/.
//
// The contract now: a single-file review is keyed by its TARGET. Two of them in one
// directory coexist — two servers, two stores, no --replace — and neither can see the
// other's work.
//
// Run: go test -tags=browser -run TestE2E_ConcurrentSingleFileReviews ./e2e/...

package e2e

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// planDir is the reported setup: a directory of unrelated documents, each reviewed on
// its own, with a pre-#199 shared store holding one comment per document.
func planDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, f := range []struct{ name, body string }{
		{"plan-one.md", "# Plan one\n\nthe first plan's prose\n"},
		{"plan-two.md", "# Plan two\n\nthe second plan's prose\n"},
	} {
		if err := os.WriteFile(filepath.Join(dir, f.name), []byte(f.body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	pdir := filepath.Join(dir, ".prereview")
	if err := os.MkdirAll(pdir, 0o755); err != nil {
		t.Fatal(err)
	}
	seed := "id,file,from_line,to_line,side,body,created_at,resolved,anchor,anchor_status,kind,area,url\n" +
		"c1,plan-one.md,3,3,new,belongs to PLAN ONE,2026-07-20T10:00:00Z,false,,,line,,\n" +
		"c2,plan-two.md,3,3,new,belongs to PLAN TWO,2026-07-20T10:00:00Z,false,,,line,,\n"
	if err := os.WriteFile(filepath.Join(pdir, "comments.csv"), []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestE2E_ConcurrentSingleFileReviews(t *testing.T) {
	dir := planDir(t)

	// Review one. Its comment came from the shared store — the upgrade path: a
	// reviewer's existing work must not read as deleted just because the store moved.
	one := bootChromeAgainstRepo(t, filepath.Join(dir, "plan-one.md"), 1400, 1000, "--agent")
	one.waitReady()

	// Review two, of a SIBLING file, with NO --replace. This is the launch that used to
	// fail ("already running for this repo") or evict review one. startPrereview fails
	// the test with the server's stderr if it never comes up.
	urlTwo, storeTwo, srvTwo, stderrTwo := startPrereview(t, one.binary, filepath.Join(dir, "plan-two.md"), "--agent")
	t.Cleanup(func() {
		_ = srvTwo.Process.Kill()
		_, _ = srvTwo.Process.Wait()
	})

	// Two targets, two stores — both under the directory's own store dir, so one
	// .gitignore entry still covers them.
	if storeTwo == one.store {
		t.Fatalf("both reviews resolved to one store (%s) — this IS the bug", storeTwo)
	}
	for _, s := range []string{one.store, storeTwo} {
		if !strings.HasPrefix(s, filepath.Join(dir, ".prereview")+string(filepath.Separator)) {
			t.Errorf("store %q is not under the directory's .prereview/", s)
		}
	}

	bodyAt := func(url, who string) string {
		t.Helper()
		var body string
		if err := chromedp.Run(one.ctx,
			chromedp.Navigate(url),
			chromedp.WaitVisible(`#files-drawer button.file-btn`, chromedp.ByQuery),
			chromedp.Sleep(1*time.Second),
			chromedp.Text(`body`, &body, chromedp.ByQuery),
		); err != nil {
			t.Fatalf("%s (%s) did not serve a page: %v\n--- review one stderr ---\n%s\n--- review two stderr ---\n%s",
				who, url, err, one.stderr.String(), stderrTwo.String())
		}
		return body
	}

	// Review two shows its own document's comment and nothing of review one's.
	two := bodyAt(urlTwo, "review two")
	if !strings.Contains(two, "belongs to PLAN TWO") {
		t.Errorf("review two lost its own comment when the store moved — the reviewer's work "+
			"must survive the upgrade\n%s", two)
	}
	if strings.Contains(two, "belongs to PLAN ONE") {
		t.Errorf("review two is showing the OTHER plan's comment — the store bleed in #199\n%s", two)
	}

	// And review one is still serving: launching a review of an unrelated sibling file
	// must not have stopped it. (Before #199 this port was dead.)
	first := bodyAt(one.url, "review one")
	if !strings.Contains(first, "belongs to PLAN ONE") {
		t.Errorf("review one was evicted or lost its comment after an unrelated sibling file "+
			"was reviewed — the reported bug\n%s", first)
	}
	if strings.Contains(first, "belongs to PLAN TWO") {
		t.Errorf("review one is showing the OTHER plan's comment — the store bleed in #199\n%s", first)
	}

	// The agent's view of each queue must agree with its reviewer's, or the agent edits
	// a document nobody is reviewing.
	for _, tc := range []struct{ store, wantID, wantFile string }{
		{one.store, "c1", "plan-one.md"},
		{storeTwo, "c2", "plan-two.md"},
	} {
		out, err := exec.Command(one.binary, "comments", "--out", tc.store, "--json").Output()
		if err != nil {
			t.Fatalf("prereview comments --out %s: %v", tc.store, err)
		}
		var listed []struct {
			ID   string `json:"id"`
			File string `json:"file"`
		}
		if err := json.Unmarshal(out, &listed); err != nil {
			t.Fatalf("parse comments json: %v\n%s", err, out)
		}
		if len(listed) != 1 || listed[0].ID != tc.wantID || listed[0].File != tc.wantFile {
			t.Errorf("`prereview comments --out %s` = %s, want only %s on %s",
				tc.store, out, tc.wantID, tc.wantFile)
		}
	}

	// Carrying rows into the per-target stores is a COPY: the shared store may still be
	// a directory review's live store, so nothing may be removed from it.
	shared, err := os.ReadFile(filepath.Join(dir, ".prereview", "comments.csv"))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"c1", "c2"} {
		if !strings.Contains(string(shared), id) {
			t.Errorf("DATA LOSS: %s was removed from the shared store — the carry-over copies, "+
				"it never moves", id)
		}
	}
}
