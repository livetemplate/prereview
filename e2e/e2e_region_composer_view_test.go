//go:build browser

package e2e

import (
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// composerRect is where an open composer and the box it is for sit in the
// viewport.
type composerRect struct{ Top, Bottom, BoxTop, BoxBottom, VH float64 }

func readComposerRect(t *testing.T, p *runningPrereview, composer, box string, diag func() string) composerRect {
	t.Helper()
	var r composerRect
	if err := chromedp.Run(p.ctx, chromedp.Evaluate(`(() => {
		const c = document.querySelector('`+composer+`').getBoundingClientRect();
		const b = document.querySelector('`+box+`').getBoundingClientRect();
		return {Top: c.top, Bottom: c.bottom, BoxTop: b.top, BoxBottom: b.bottom, VH: innerHeight};
	})()`, &r)); err != nil {
		t.Fatalf("read the composer's rect: %v%s", err, diag())
	}
	return r
}

// TestE2E_HTMLComposerInView: drawing a box on an HTML preview opens its
// composer on screen, with the box still in sight above it. The preview is as
// tall as its page, and the composer once opened in a list below it, out of
// sight; it now opens in the card under the box (TestE2E_HTMLPreviewCard).
func TestE2E_HTMLComposerInView(t *testing.T) {
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
			openHTMLComposer(t, p, previewBox{0.1, 12, 0.8, 100}, "the composer", diag)
			// Let the autofocus and any scroll settle before measuring.
			time.Sleep(400 * time.Millisecond)

			r := readComposerRect(t, p, `.composer`, `.preview-pins .area-overlay-pending`, diag)
			if r.Top < 0 || r.Bottom > r.VH {
				t.Errorf("the composer is off screen: top %.0f, bottom %.0f, viewport %.0f%s", r.Top, r.Bottom, r.VH, diag())
			}
			if r.BoxTop < 0 || r.BoxBottom > r.Top {
				t.Errorf("the drawn box (%.0f–%.0f) is not in sight above the composer (top %.0f)%s", r.BoxTop, r.BoxBottom, r.Top, diag())
			}
		})
	}
}

// TestE2E_RegionComposerInView: drawing a box on the live site opens its
// composer on screen, in a live-only and a combined session.
func TestE2E_RegionComposerInView(t *testing.T) {
	for _, mode := range []string{"live", "combined"} {
		for _, vp := range []struct {
			name string
			w, h int
		}{{"desktop", 1200, 900}, {"phone", 390, 844}} {
			t.Run(mode+"/"+vp.name, func(t *testing.T) {
				app := newLiveApp(t)
				var p *runningPrereview
				if mode == "live" {
					p = bootChromeExternal(t, app.srv.URL, vp.w, vp.h)
				} else {
					p = bootChromeCombined(t, setupImportRepo(t), app.srv.URL, vp.w, vp.h, "--agent")
				}
				diag := captureDiag(t, p)
				if mode == "live" {
					if err := chromedp.Run(p.ctx,
						chromedp.Navigate(p.url),
						chromedp.WaitVisible(`iframe.ext-frame`, chromedp.ByQuery),
						chromedp.WaitVisible(`.bar-external .ext-page`, chromedp.ByQuery),
						chromedp.Sleep(500*time.Millisecond),
					); err != nil {
						t.Fatalf("frame the live site: %v%s", err, diag())
					}
				} else {
					// Past the beacon's last start-up report, as in
					// TestE2E_CombinedStageSurvivesToggle.
					time.Sleep(3500 * time.Millisecond)
					pickSurface(t, p, true, diag)
					waitFramedPage(t, p, "/", diag)
				}
				openRegionComposer(t, p, 0.30, 0.30, 0.60, 0.55, false, diag)
				time.Sleep(400 * time.Millisecond)

				r := readComposerRect(t, p, `.ext-composer`, `.pin-layer .area-overlay-pending`, diag)
				if r.Top < 0 || r.Bottom > r.VH {
					t.Errorf("the composer is off screen: top %.0f, bottom %.0f, viewport %.0f%s", r.Top, r.Bottom, r.VH, diag())
				}
			})
		}
	}
}
