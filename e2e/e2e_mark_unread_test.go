//go:build browser

package e2e

import (
	"strconv"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// readPercent is the width of the file header's reading-progress bar, 0 when
// there is none.
const readPercent = `(()=>{const f=document.querySelector('.read-progress-fill');return f?parseFloat(f.style.width):0})()`

// TestE2E_MarkUnread: reading a file to the end and marking it Viewed, then
// Mark unread (the file header, the u key, or the ⋯ menu on a phone) forgets
// both: the bar falls back to the first screen, the Viewed tick goes and the
// view is at the top again.
func TestE2E_MarkUnread(t *testing.T) {
	for _, way := range []string{"header", "key", "phone menu"} {
		t.Run(way, func(t *testing.T) {
			w, h := 1280, 900
			if way == "phone menu" {
				w, h = 390, 844
			}
			p := bootChromeAgainstRepo(t, longDocRepo(t), w, h)
			diag := captureDiag(t, p)
			ctx, cancel := within(p, 30*time.Second)
			defer cancel()
			if err := chromedp.Run(ctx,
				chromedp.EmulateViewport(int64(w), int64(h)),
				chromedp.Navigate(p.url),
				chromedp.WaitVisible(`.md-view`, chromedp.ByQuery),
				chromedp.Sleep(time.Second), // the client attaches its listeners
			); err != nil {
				t.Fatalf("open guide.md: %v%s", err, diag())
			}

			// Read to the end, then mark it Viewed in the file list.
			_ = chromedp.Run(ctx, chromedp.Evaluate(`(()=>{const v=document.querySelector('main.viewer');v.scrollTo(0,v.scrollHeight);window.scrollTo(0,document.body.scrollHeight);return true})()`, nil))
			waitJSDiag(t, p, readPercent+` >= 99`, "the bar fills after reading to the end", diag)
			_ = chromedp.Run(ctx, chromedp.Evaluate(`document.querySelector('.file-viewed-form input[value="guide.md"]').form.querySelector('button').click(), true`, nil))
			waitJSDiag(t, p, `document.querySelector('.file-viewed-form input[value="guide.md"]').form.querySelector('button').getAttribute('aria-label') === 'Mark unread'`, "the file is Viewed", diag)

			switch way {
			case "header":
				if err := chromedp.Run(ctx, chromedp.Click(`.file-head-unread button[name="markUnread"]`, chromedp.ByQuery)); err != nil {
					t.Fatalf("tap Mark unread: %v%s", err, diag())
				}
			case "key":
				if err := chromedp.Run(ctx, chromedp.KeyEvent("u")); err != nil {
					t.Fatalf("press u: %v%s", err, diag())
				}
			case "phone menu":
				if err := chromedp.Run(ctx,
					chromedp.Click(`.more-trigger`, chromedp.ByQuery),
					chromedp.WaitVisible(`.more-menu.is-open button[name="markUnread"]`, chromedp.ByQuery),
					chromedp.Click(`.more-menu button[name="markUnread"]`, chromedp.ByQuery),
				); err != nil {
					t.Fatalf("Mark unread from the ⋯ menu: %v%s", err, diag())
				}
			}
			waitJSDiag(t, p, `document.querySelector('.file-viewed-form input[value="guide.md"]').form.querySelector('button').getAttribute('aria-label') === 'Mark as viewed'`, "the Viewed mark is gone", diag)
			waitJSDiag(t, p, readPercent+` < 50`, "the bar starts over", diag)
			if top := evalStr(t, p, `String(Math.round(document.querySelector('.md-view .md-block').getBoundingClientRect().top))`); mustAtoi(top) > h {
				t.Errorf("the view should be back at the top; the first block is at %s px%s", top, diag())
			}
		})
	}
}

func mustAtoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}
