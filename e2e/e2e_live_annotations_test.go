//go:build browser

package e2e

import (
	"strings"
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
			run("tap the marker", chromedp.Click(`.pin-layer .area-overlay[data-id="`+id+`"] .pin-btn`, chromedp.ByQuery))
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

// TestE2E_LiveMarkers: a saved live annotation stays out of the way. Its box
// is invisible and lets taps and drags through to the page; only a small
// numbered marker at its corner remains. Show boxes (b) outlines every box
// without making them catch taps, and a new box can be drawn starting inside
// an existing one, by mouse on desktop and by touch on a phone.
func TestE2E_LiveMarkers(t *testing.T) {
	for _, vp := range []struct {
		name  string
		w, h  int
		touch bool
	}{{"desktop", 1280, 800, false}, {"phone", 390, 844, true}} {
		t.Run(vp.name, func(t *testing.T) {
			app := newLiveApp(t)
			p := bootChromeCombined(t, setupFixtureRepo(t), app.srv.URL, vp.w, vp.h, "--agent")
			diag := captureDiag(t, p)
			time.Sleep(3500 * time.Millisecond)
			pickSurface(t, p, true, diag)
			drawRegionAt(t, p, "Hero heading is too close to the top", 0.20, 0.20, 0.80, 0.60, false, diag)

			type probe struct {
				HitsPin     bool
				BorderClear bool
				MarkerW     float64
				MarkerH     float64
			}
			look := func() probe {
				t.Helper()
				var pr probe
				if err := chromedp.Run(p.ctx, chromedp.Evaluate(`(() => {
					const box = document.querySelector('.pin-layer .area-overlay[data-id]');
					const r = box.getBoundingClientRect();
					const hit = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
					const m = box.querySelector('.pin-btn').getBoundingClientRect();
					const cs = getComputedStyle(box);
					return {
						HitsPin: !!(hit && hit.closest('.pin-layer')),
						BorderClear: cs.borderTopColor === 'rgba(0, 0, 0, 0)',
						MarkerW: m.width, MarkerH: m.height,
					};
				})()`, &pr)); err != nil {
					t.Fatalf("probe the saved box: %v%s", err, diag())
				}
				return pr
			}

			// Annotate sits in the header, and the header keeps the annotation
			// count whole (on a phone it has to share the bar with Annotate).
			var header struct{ AnnotateInHeader, CountWhole bool }
			if err := chromedp.Run(p.ctx, chromedp.Evaluate(`(() => {
				const c = document.querySelector('header.bar .live-count');
				const title = c && c.closest('.title');
				return {
					AnnotateInHeader: !!document.querySelector('header.bar button[name="toggleRegionSelect"]'),
					CountWhole: !!c && title.getBoundingClientRect().right >= c.getBoundingClientRect().right - 1 && c.getBoundingClientRect().width >= 30,
				};
			})()`, &header)); err != nil {
				t.Fatalf("read the header: %v", err)
			}
			if !header.AnnotateInHeader || !header.CountWhole {
				t.Errorf("header: Annotate in it = %v, count shown whole = %v%s", header.AnnotateInHeader, header.CountWhole, diag())
			}

			got := look()
			if got.HitsPin {
				t.Errorf("a tap at the box's centre lands on the annotation, not the page%s", diag())
			}
			if !got.BorderClear {
				t.Errorf("the saved box is drawn with Show boxes off%s", diag())
			}
			if got.MarkerH > 28 || got.MarkerW > 40 {
				t.Errorf("marker is %vx%v px, want at most 40x28", got.MarkerW, got.MarkerH)
			}

			// Show boxes (b): outlined, still not catching taps; b again hides them.
			if err := chromedp.Run(p.ctx, chromedp.KeyEvent("b")); err != nil {
				t.Fatalf("press b: %v", err)
			}
			waitJSDiag(t, p, `!!document.querySelector('.ext-stage.show-boxes')`, "Show boxes on", diag)
			if got := look(); got.BorderClear || got.HitsPin {
				t.Errorf("with Show boxes on, want an outline that lets taps through; got outline=%v hitsPin=%v%s", !got.BorderClear, got.HitsPin, diag())
			}
			if err := chromedp.Run(p.ctx, chromedp.KeyEvent("b")); err != nil {
				t.Fatalf("press b again: %v", err)
			}
			waitJSDiag(t, p, `!document.querySelector('.ext-stage.show-boxes')`, "Show boxes off", diag)

			// The marker's label stays readable on every annotation colour, open
			// and worked on. The colours sit on the reviewed page, not the theme,
			// and don't depend on the viewport, so desktop checks them once.
			if !vp.touch {
				markerContrast := func(label string) {
					t.Helper()
					var ratios []float64
					if err := chromedp.Run(p.ctx, chromedp.Evaluate(`(() => {
						`+wcagLumJS+`
						const box = document.querySelector('.pin-layer .area-overlay[data-id]');
						const saved = box.className;
						const out = [];
						for (let n = 1; n <= 6; n++) {
							box.className = saved.replace(/\bann-c\d\b/, 'ann-c' + n);
							const cs = getComputedStyle(box.querySelector('.pin-btn'));
							const a = lum(cs.color), b = lum(cs.backgroundColor);
							out.push((Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05));
						}
						box.className = saved;
						return out;
					})()`, &ratios)); err != nil {
						t.Fatalf("marker contrast (%s): %v", label, err)
					}
					for n, ratio := range ratios {
						if ratio < 4.5 {
							t.Errorf("%s marker label on ann-c%d: contrast %.2f:1, want at least 4.5:1", label, n+1, ratio)
						}
					}
				}
				markerContrast("open")
				var id string
				if err := chromedp.Run(p.ctx, chromedp.AttributeValue(`.pin-layer .area-overlay[data-id]`, "data-id", &id, nil, chromedp.ByQuery)); err != nil {
					t.Fatalf("read the id: %v", err)
				}
				mustCLI(t, p, "done", "--out", p.store, id)
				waitJSDiag(t, p, `!!document.querySelector('.pin-layer .area-overlay.is-acted')`, "the marker shows worked on", diag)
				markerContrast("worked-on")
			}

			// Overlap: a box drawn starting inside the saved one.
			drawRegionAt(t, p, "This word inside the first box", 0.40, 0.35, 0.60, 0.45, vp.touch, diag)

			// Markers are numbered per page in the order the annotations were made,
			// and the card in All comments carries the same number.
			waitJSDiag(t, p, `[...document.querySelectorAll('.pin-layer .pin-btn')].map(b => b.textContent.trim()).join(',') === '1,2'`, "markers numbered 1 and 2", diag)
			if err := chromedp.Run(p.ctx, chromedp.Click(`.pin-layer .area-overlay[data-id]:first-child .pin-btn`, chromedp.ByQuery)); err != nil {
				t.Fatalf("tap marker 1: %v%s", err, diag())
			}
			waitJSDiag(t, p, `((document.querySelector('.ac-item.is-target .anno-label')||{}).textContent||'').trim() === 'annotation 1'`, "the card labelled annotation 1", diag)

			// The two overlapping boxes have different colours, and each card in
			// All comments has its box's colour.
			var colours struct{ Markers, Cards []string }
			if err := chromedp.Run(p.ctx, chromedp.Evaluate(`(() => {
				const colour = el => [...el.classList].find(c => c.startsWith('ann-c')) || '';
				const boxes = [...document.querySelectorAll('.pin-layer .area-overlay[data-id]')];
				return {
					Markers: boxes.map(b => getComputedStyle(b.querySelector('.pin-btn')).backgroundColor),
					Cards: boxes.map(b => { const c = document.querySelector('.ac-item[data-key="' + b.dataset.id + '"]'); return colour(b) + '=' + (c ? colour(c) : 'missing'); }),
				};
			})()`, &colours)); err != nil {
				t.Fatalf("read the colours: %v", err)
			}
			if len(colours.Markers) != 2 || colours.Markers[0] == colours.Markers[1] {
				t.Errorf("the two markers' colours = %v, want two different%s", colours.Markers, diag())
			}
			for _, pair := range colours.Cards {
				if l, r, _ := strings.Cut(pair, "="); l == "" || l != r {
					t.Errorf("a box and its All comments card differ: %s%s", pair, diag())
				}
			}
		})
	}
}
