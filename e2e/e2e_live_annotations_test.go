//go:build browser

package e2e

import (
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// TestE2E_LiveAnnotationsList: in a combined session, a live-site annotation is
// read, answered and resolved from All comments. The live page's count opens
// the list filtered to annotations; a box tap opens it on that box's card; the
// card shows the agent's reply, takes a reply and resolves; "Show on page" goes
// back to the live site with the box focused, without reloading the site.
func TestE2E_LiveAnnotationsList(t *testing.T) {
	for _, vp := range []struct {
		name string
		w, h int
	}{{"desktop", 1280, 800}, {"phone", 390, 844}} {
		t.Run(vp.name, func(t *testing.T) {
			app := newLiveApp(t)
			p := bootChromeCombined(t, setupFixtureRepo(t), app.srv.URL, vp.w, vp.h, "--agent")
			diag := captureDiag(t, p)
			run := func(what string, actions ...chromedp.Action) {
				t.Helper()
				ctx, cancel := within(p, 20*time.Second)
				defer cancel()
				if err := chromedp.Run(ctx, actions...); err != nil {
					t.Fatalf("%s: %v%s", what, err, diag())
				}
			}

			// Past the beacon's start-up reports (see TestE2E_CombinedStageSurvivesToggle).
			time.Sleep(3500 * time.Millisecond)
			pickSurface(t, p, true, diag)
			drawRegion(t, p, "Hero heading is too close to the top", diag)
			var id string
			run("read the pin id", chromedp.AttributeValue(`.pin-layer .area-overlay[data-id]`, "data-id", &id, nil, chromedp.ByQuery))
			card := `.all-comments .ac-item[data-key="` + id + `"]`

			mustCLI(t, p, "done", "--out", p.store, id)
			mustCLI(t, p, "reply", "--out", p.store, id, "--body", "Moved the heading down 24px.")

			// The count opens All comments on the annotations: labelled, worked on,
			// with the agent's reply.
			run("tap the count", chromedp.Click(`header.bar button.live-count`, chromedp.ByQuery))
			waitJSDiag(t, p, `!!document.querySelector('`+card+` .processed-badge')`, "the card shows worked on", diag)
			waitJSDiag(t, p, `((document.querySelector('`+card+` .thread')||{}).textContent||'').includes('Moved the heading down 24px.')`, "the card shows the agent's reply", diag)
			waitJSDiag(t, p, `!!document.querySelector('`+card+` .anno-label') && document.querySelector('.ac-filter button[aria-pressed="true"]').textContent.startsWith('Annotations')`, "the annotation label and the Annotations filter", diag)

			// Reply from the card.
			run("open the reply", chromedp.Click(card+` button[name="openReply"]`, chromedp.ByQuery))
			run("type and send the reply",
				chromedp.WaitVisible(card+` .reply-form textarea`, chromedp.ByQuery),
				chromedp.SendKeys(card+` .reply-form textarea`, "Still too close on mobile", chromedp.ByQuery),
				chromedp.Click(card+` .reply-form button[name="postReply"]`, chromedp.ByQuery),
			)
			waitJSDiag(t, p, `((document.querySelector('`+card+` .thread')||{}).textContent||'').includes('Still too close on mobile') && !!document.querySelector('`+card+` .awaiting-badge')`, "the reviewer's reply posted, awaiting the agent", diag)

			// Back to the live page, then a box tap opens the list on its card.
			run("back", chromedp.Click(`.all-comments button[name="toggleCommentList"]`, chromedp.ByQuery))
			waitJSDiag(t, p, `!document.querySelector('.all-comments') && !!document.querySelector('.live-wrap:not(.live-hidden) .pin-layer .area-overlay[data-id="`+id+`"]')`, "the live page with the box", diag)
			rootHits := app.hitCount("/")
			run("tap the box", chromedp.Evaluate(`document.querySelector('.pin-layer .area-overlay[data-id="`+id+`"]').click(), true`, nil))
			waitJSDiag(t, p, `!!document.querySelector('`+card+`.is-target')`, "the box's card, marked as the target", diag)

			// Show on page: the live site, the box focused, the site not reloaded.
			run("show on page", chromedp.Click(card+` button.ac-show-on-page`, chromedp.ByQuery))
			waitJSDiag(t, p, `!document.querySelector('.all-comments') && !!document.querySelector('.pin-layer .area-overlay.is-focused[data-id="`+id+`"]')`, "the live page with the box focused", diag)
			if got := app.hitCount("/"); got != rootHits {
				t.Errorf("the live site reloaded going to the list and back (%d → %d hits on /)%s", rootHits, got, diag())
			}

			// Resolve from the card: the box leaves the page.
			run("tap the count again", chromedp.Click(`header.bar button.live-count`, chromedp.ByQuery))
			run("resolve", chromedp.Click(card+` button[name="toggleResolved"]`, chromedp.ByQuery))
			waitJSDiag(t, p, `!document.querySelector('`+card+`')`, "the resolved card leaves the list", diag)
			run("back after resolving", chromedp.Click(`.all-comments button[name="toggleCommentList"]`, chromedp.ByQuery))
			waitJSDiag(t, p, `!document.querySelector('.all-comments') && !document.querySelector('.pin-layer .area-overlay[data-id="`+id+`"]')`, "the resolved box leaves the page", diag)
		})
	}
}

// TestE2E_AllCommentsThreads: All comments shows every comment's thread and a
// Reply, a line comment's too. Before, a reply was only visible on its inline
// card.
func TestE2E_AllCommentsThreads(t *testing.T) {
	p := bootChromeAgainstRepo(t, setupFixtureRepo(t), 1280, 800, "--agent")
	diag := captureDiag(t, p)
	p.waitReady()
	p.clickFile("edited.go")
	addLineComment(t, p, 0, 4, "Rename this")
	rows := p.readCSV()
	if len(rows) < 2 {
		t.Fatalf("no comment saved%s", diag())
	}
	id := rows[1][0]
	mustCLI(t, p, "reply", "--out", p.store, id, "--body", "Renamed to userToken.")
	ctx, cancel := within(p, 20*time.Second)
	defer cancel()
	if err := chromedp.Run(ctx, chromedp.Evaluate(`document.querySelector('button[name="toggleCommentList"]').click(), true`, nil)); err != nil {
		t.Fatalf("open All comments: %v%s", err, diag())
	}
	card := `.all-comments .ac-item[data-key="` + id + `"]`
	waitJSDiag(t, p, `((document.querySelector('`+card+` .thread')||{}).textContent||'').includes('Renamed to userToken.') && !!document.querySelector('`+card+` button[name="openReply"]')`, "the line comment's reply and a Reply button", diag)
	var filter bool
	if err := chromedp.Run(ctx, chromedp.Evaluate(`!!document.querySelector('.ac-filter')`, &filter)); err != nil || filter {
		t.Errorf("with no annotations, the All/Annotations filter should not show (err %v)%s", err, diag())
	}
}
