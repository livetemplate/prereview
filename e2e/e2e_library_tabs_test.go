//go:build browser

package e2e

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	cdpinput "github.com/chromedp/cdproto/input"
	"github.com/chromedp/chromedp"
	"github.com/livetemplate/prereview/csv"
)

// TestE2E_LibraryTabsUnderLoad: the Library's tabs switch on every click,
// held like a person's, while background work keeps re-rendering the page: the
// agent going working and done (its toast shows and hides) and proposing
// learnings. Reported from v0.35.2: a click on a tab was lost now and then. A
// banner appearing as a bare sibling of .layout made morphdom rebuild .layout
// and carry the Library over into it, detaching it between press and release;
// the test also checks the Library section is never detached at all.
func TestE2E_LibraryTabsUnderLoad(t *testing.T) {
	for _, vp := range []struct {
		name string
		w, h int
	}{{"desktop", 1200, 800}, {"phone", 390, 844}} {
		t.Run(vp.name, func(t *testing.T) {
			repo := setupFixtureRepoChecks(t, "notes.md")
			past := seedReviewStore(t, filepath.Join(t.TempDir(), "notes"),
				csv.Row{ID: "c1", File: "plan.md", FromLine: 4, ToLine: 4, Body: "Drop “basically”: it adds nothing.", CreatedAt: time.Now().Add(-48 * time.Hour)})
			p := bootChromeAgainstRepo(t, repo, vp.w, vp.h, "--agent")
			diag := captureDiag(t, p)
			p.waitReadyAt(vp.w, vp.h)
			ctx, cancel := within(p, 120*time.Second)
			defer cancel()
			p.ctx = ctx

			if err := chromedp.Run(ctx,
				chromedp.Click(`.library-trigger`, chromedp.ByQuery),
				chromedp.WaitVisible(libTabOn("learnings"), chromedp.ByQuery),
			); err != nil {
				t.Fatalf("open the Library: %v%s", err, diag())
			}

			// Count every time the Library section leaves the page.
			if err := chromedp.Run(ctx, chromedp.Evaluate(`(() => {
				window.__libDetached = 0;
				new MutationObserver(ms => { for (const m of ms) for (const n of m.removedNodes) if (n.nodeType === 1 && n.matches('section.library')) window.__libDetached++; })
					.observe(document.body, { childList: true, subtree: true });
				return true;
			})()`, nil)); err != nil {
				t.Fatalf("watch the Library: %v", err)
			}

			// The agent goes working and done, and proposes learnings, while
			// the reviewer clicks.
			stop := make(chan struct{})
			var wg sync.WaitGroup
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 0; ; i++ {
					select {
					case <-stop:
						return
					case <-time.After(150 * time.Millisecond):
					}
					switch i % 3 {
					case 0:
						_ = exec.Command(p.binary, "status", "--out", p.store, "working", "Applying your review").Run()
					case 1:
						_ = exec.Command(p.binary, "status", "--out", p.store, "done", "Tightened the intro").Run()
					default:
						_, _ = learnProposeErr(p, "--text", fmt.Sprintf("Rule %d: cut filler.", i), "--scope", "Technical docs", "--evidence", past+":c1")
					}
				}
			}()
			defer func() { close(stop); wg.Wait() }()

			tabs := []string{"shortcuts", "skills", "checks", "benchmarks", "learnings"}
			for round := 0; round < 4; round++ {
				for _, tab := range tabs {
					humanClick(t, p, libTabButton(tab), diag)
					waitJSDiag(t, p, `!!document.querySelector('`+libTabOn(tab)+`')`, fmt.Sprintf("round %d: the %s tab shows after its click", round, tab), diag)
				}
			}
			if n := evalInt(t, p, `window.__libDetached`); n != 0 {
				t.Errorf("the Library section left the page %d times while banners came and went%s", n, diag())
			}
		})
	}
}

func libTabButton(tab string) string {
	return `.lib-tabs form:has(input[value="` + tab + `"]) button`
}

func libTabOn(tab string) string { return libTabButton(tab) + `.is-on` }

// humanClick clicks the centre of sel the way a person does: the button is
// held for 150ms between press and release. chromedp.Click presses and
// releases at once, so a re-render between the two can't swallow its click.
func humanClick(t *testing.T, p *runningPrereview, sel string, diag func() string) {
	t.Helper()
	var r struct{ X, Y float64 }
	if err := chromedp.Run(p.ctx, chromedp.Evaluate(`(() => { const b = document.querySelector('`+sel+`'); b.scrollIntoView({block: 'center', inline: 'center'}); const r = b.getBoundingClientRect(); return { X: r.left + r.width / 2, Y: r.top + r.height / 2 }; })()`, &r)); err != nil {
		t.Fatalf("find %s: %v%s", sel, err, diag())
	}
	if err := chromedp.Run(p.ctx,
		cdpinput.DispatchMouseEvent(cdpinput.MouseMoved, r.X, r.Y),
		cdpinput.DispatchMouseEvent(cdpinput.MousePressed, r.X, r.Y).WithButton(cdpinput.Left).WithClickCount(1),
		chromedp.Sleep(150*time.Millisecond),
		cdpinput.DispatchMouseEvent(cdpinput.MouseReleased, r.X, r.Y).WithButton(cdpinput.Left).WithClickCount(1),
	); err != nil {
		t.Fatalf("click %s: %v%s", sel, err, diag())
	}
}
