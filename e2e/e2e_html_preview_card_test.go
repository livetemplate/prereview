//go:build browser

package e2e

import (
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// TestE2E_HTMLPreviewCard: an HTML preview's comments open in a card over the
// page, by the box they belong to, instead of in a list under the preview.
//   - Drawing a box opens the card under it, with the comment box.
//   - Saving closes it and leaves a marker.
//   - Tapping the marker opens the card under the drawn box, with the thread
//     and Reply; × closes it.
//   - A jump from All comments opens the card too.
func TestE2E_HTMLPreviewCard(t *testing.T) {
	for _, vp := range []struct {
		name string
		w, h int
	}{{"desktop", 1200, 800}, {"phone", 390, 844}} {
		t.Run(vp.name, func(t *testing.T) {
			p := bootChromeAgainstRepo(t, setupFixtureHTMLRegionRepo(t), vp.w, vp.h, "--agent")
			ctx, cancel := within(p, 90*time.Second)
			t.Cleanup(cancel)
			p.ctx = ctx
			p.waitReadyAt(vp.w, vp.h)
			diag := captureDiag(t, p)
			p.clickFile("region.html")
			waitPreviewBridgeReady(t, p)

			// cardUnder: the card starts on screen, just under the box (pending
			// or the drawn one), and fits the screen's width.
			cardUnder := func(box string) string {
				return `(() => { const c = document.querySelector('.preview-card'), b = document.querySelector('` + box + `');
					if (!c || !b) return false;
					const r = c.getBoundingClientRect(), q = b.getBoundingClientRect();
					return r.top >= q.bottom && r.top - q.bottom < 20 && r.top < innerHeight && r.left >= 0 && r.right <= innerWidth; })()`
			}

			openHTMLComposer(t, p, previewBox{0.1, 12, 0.6, 90}, "the composer", diag)
			waitJSDiag(t, p, `!!document.querySelector('.preview-card .composer textarea') && !document.querySelector('.html-block')`, "the composer in the card, and no list under the preview", diag)
			waitJSDiag(t, p, cardUnder(`.preview-pins .area-overlay-pending`), "the card under the box being drawn", diag)

			if err := chromedp.Run(p.ctx,
				chromedp.SendKeys(`.preview-card .composer textarea`, "Make the heading bigger", chromedp.ByQuery),
				chromedp.Click(`.preview-card .composer button[name="addComment"]`, chromedp.ByQuery),
			); err != nil {
				t.Fatalf("save: %v%s", err, diag())
			}
			marker := `.preview-pins .area-overlay[data-id] .pin-btn`
			waitJSDiag(t, p, `!document.querySelector('.preview-card') && !!document.querySelector('`+marker+`')`, "saving closes the card and leaves a marker", diag)

			tapCentred(t, p, marker, "tap the marker", diag)
			waitJSDiag(t, p, `(() => { const c = document.querySelector('.preview-card'); return !!c && c.textContent.includes('Make the heading bigger') && !!c.querySelector('button[name="openReply"]'); })()`, "the card with the comment and Reply", diag)
			waitJSDiag(t, p, cardUnder(`.preview-pins .drawn-box.is-shown`), "the card under the drawn box", diag)

			tapCentred(t, p, `.preview-card-close`, "close the card", diag)
			waitJSDiag(t, p, `!document.querySelector('.preview-card')`, "× closes the card", diag)

			if err := chromedp.Run(p.ctx, chromedp.KeyEvent("a")); err != nil {
				t.Fatalf("open All comments: %v", err)
			}
			waitJSDiag(t, p, `!!document.querySelector('.ac-item .ac-jump-btn')`, "All comments lists the comment", diag)
			if err := chromedp.Run(p.ctx, chromedp.Evaluate(`document.querySelector('.ac-item .ac-jump-btn').click(), true`, nil)); err != nil {
				t.Fatalf("jump: %v%s", err, diag())
			}
			waitJSDiag(t, p, `!document.querySelector('.ac-list') && (() => { const c = document.querySelector('.preview-card'); return !!c && c.textContent.includes('Make the heading bigger'); })()`, "the jump opens the card", diag)
		})
	}
}
