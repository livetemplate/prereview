//go:build browser

package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// layoutProblemsJS lists what breaks the toolbar's and the file header's
// layout, as plain strings ("" entries never appear). It reads only rendered
// geometry, so it catches what DOM assertions can't: an unsized icon drawn at
// the browser's 300×150 default (#293), or a toolbar wrapping once its buttons
// carry counts. Rules:
//   - an icon in a control is at most 24px each way;
//   - a control is one line: at most 48px tall;
//   - no control extends past the viewport's right edge;
//   - no two file-header controls overlap;
//   - on a desktop (oneRow), the toolbar is a single row.
const layoutProblemsJS = `((oneRow) => {
  const out = [], vw = window.innerWidth;
  const seen = el => el.checkVisibility() && el.getBoundingClientRect().width > 0;
  const name = el => (el.getAttribute('name') || el.getAttribute('aria-label') || el.className || el.tagName).toString().slice(0, 40);
  const controls = [...document.querySelectorAll('header.bar button, header.bar a[role=button], header.bar a.tb-btn, header.file-head button, header.file-head a[role=button]')].filter(seen);
  for (const c of controls) {
    const r = c.getBoundingClientRect();
    if (r.height > 48) out.push(name(c) + ': ' + Math.round(r.height) + 'px tall');
    if (r.right > vw + 1) out.push(name(c) + ': past the right edge by ' + Math.round(r.right - vw) + 'px');
    for (const s of c.querySelectorAll('svg')) {
      if (!seen(s)) continue;
      const b = s.getBoundingClientRect();
      if (b.width > 24 || b.height > 24) out.push(name(c) + ': icon ' + Math.round(b.width) + 'x' + Math.round(b.height));
    }
  }
  const head = controls.filter(c => c.closest('header.file-head') && !c.closest('.tb-dropdown-panel, .versions-panel'));
  for (let i = 0; i < head.length; i++) for (let j = i + 1; j < head.length; j++) {
    if (head[i].contains(head[j]) || head[j].contains(head[i])) continue;
    const a = head[i].getBoundingClientRect(), b = head[j].getBoundingClientRect();
    if (a.left < b.right - 1 && b.left < a.right - 1 && a.top < b.bottom - 1 && b.top < a.bottom - 1)
      out.push(name(head[i]) + ' overlaps ' + name(head[j]));
  }
  const bar = document.querySelector('header.bar');
  if (oneRow && bar && bar.offsetHeight > 70) out.push('toolbar wrapped: ' + bar.offsetHeight + 'px tall');
  return out;
})`

// TestE2E_LayoutInvariants: the toolbar and file header keep their shape in
// the states that once broke them, at every width from phone to wide desktop:
// a file freshly opened, a short Markdown file read to the end (Mark unread
// and its icon, Checks), and buttons carrying counts (Annotations open and
// resolved, the Queue).
func TestE2E_LayoutInvariants(t *testing.T) {
	repo := setupFixtureRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "notes.md"), []byte("# Notes\n\nA very short note.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pdir := filepath.Join(repo, ".prereview")
	if err := os.MkdirAll(pdir, 0o755); err != nil {
		t.Fatal(err)
	}
	seed := "id,file,from_line,to_line,side,body,created_at,resolved,anchor,anchor_status,kind,area,url\n" +
		"c1,edited.go,0,0,,settled,2026-06-30T12:00:00Z,true,,,file,,\n"
	if err := os.WriteFile(filepath.Join(pdir, "comments.csv"), []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}
	p := bootChromeAgainstRepo(t, repo, 1440, 900, "--agent")
	diag := captureDiag(t, p)
	p.waitReadyAt(1440, 900)

	widths := []int64{1000, 1219, 1220, 1280, 1359, 1360, 1440, 1600, 1920}
	check := func(state string) {
		t.Helper()
		for _, w := range append([]int64{390}, widths...) {
			if err := chromedp.Run(p.ctx, chromedp.EmulateViewport(w, 900), chromedp.Sleep(150*time.Millisecond)); err != nil {
				t.Fatalf("resize to %d: %v%s", w, err, diag())
			}
			var problems []string
			if err := chromedp.Run(p.ctx, chromedp.Evaluate(fmt.Sprintf(`%s(%t)`, layoutProblemsJS, w >= 900), &problems)); err != nil {
				t.Fatalf("%s at %dpx: %v%s", state, w, err, diag())
			}
			if len(problems) > 0 {
				t.Errorf("%s at %dpx:\n  %s", state, w, strings.Join(problems, "\n  "))
			}
		}
		if err := chromedp.Run(p.ctx, chromedp.EmulateViewport(1440, 900), chromedp.Sleep(150*time.Millisecond)); err != nil {
			t.Fatalf("resize back: %v%s", err, diag())
		}
	}

	p.clickFile("edited.go")
	check("a file freshly opened")

	p.clickFile("notes.md")
	waitJSDiag(t, p, `!!document.querySelector('header.file-head button[name="markUnread"]')`, "a short file read to the end offers Mark unread", diag)
	check("a Markdown file read to the end")

	p.clickFile("edited.go")
	addLineComment(t, p, 0, 4, "a queued comment")
	waitJSDiag(t, p, `/\d/.test(document.querySelector('header.bar .queue-trigger').textContent)`, "the Queue shows a count", diag)
	check("buttons with counts")
}
