//go:build browser

package e2e

import (
	"math"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// TestE2E_HTMLPreviewViewportUnits: the HTML preview is as tall as its page,
// so a page sized in vh used to grow with it on every resize, up to the
// browser's 33M px limit. The preview must settle at a finite height:
//   - vh in a <style> or style attribute is pinned to a fixed basis, and a box
//     drawn on that page still lands where it was drawn;
//   - vh set by a script, which the rewrite can't see, is stopped by the
//     beacon's guard;
//   - a page without vh keeps its exact height.
func TestE2E_HTMLPreviewViewportUnits(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		exact      float64 // the page's own height, when it has one
		marker     string  // a block to draw on and check the box, when set
	}{
		{
			name: "css",
			body: "<style>.hero{min-height:100vh}</style>\n" +
				"<section class=\"hero\" style=\"background:#eef\">Hero</section>\n" +
				"<section style=\"min-height:70vh;background:#fee\">Next</section>\n",
			marker: "MB-5-5",
		},
		{
			name: "script",
			body: "<section id=\"s\" style=\"background:#eef\">Grown by a script</section>\n" +
				"<section style=\"background:#fee\">Below it</section>\n" +
				"<script>document.getElementById('s').style.minHeight = '100vh'</script>\n",
		},
		{
			name: "plain",
			body: "<section style=\"background:#eef\">Top block</section>\n" +
				"<section style=\"background:#fee\">Bottom block</section>\n",
			exact: 480,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, diag := bootHTMLPreview(t, setupFixtureHTMLRegionRepoBody(t, tc.body), 1200, 800)

			// Past the beacon's last start-up post (3s), then two samples 1s apart.
			frameH := func() float64 {
				var h float64
				if err := chromedp.Run(p.ctx, chromedp.Evaluate(`document.querySelector('iframe.html-preview').getBoundingClientRect().height`, &h)); err != nil {
					t.Fatalf("read the preview's height: %v%s", err, diag())
				}
				return h
			}
			time.Sleep(3200 * time.Millisecond)
			h1 := frameH()
			time.Sleep(time.Second)
			h2 := frameH()
			if h1 != h2 || h2 > 20000 {
				t.Fatalf("the preview did not settle: %.0fpx, then %.0fpx a second later%s", h1, h2, diag())
			}
			if tc.exact > 0 && math.Abs(h2-tc.exact) > 1 {
				t.Errorf("the preview is %.0fpx, want the page's own %.0fpx%s", h2, tc.exact, diag())
			}

			if tc.marker != "" {
				box := previewBox{0.1, 12, 0.8, 100}
				drawHTMLRegionAt(t, p, "Hero note", box, diag)
				tapCentred(t, p, `.preview-pins .area-overlay[data-id="`+tc.marker+`"] .pin-btn`, "the hero's marker", diag)
				waitJSDiag(t, p, `document.querySelectorAll('.preview-pins .drawn-box.is-shown').length === 1`, "the drawn box shown", diag)
				checkDrawnBoxes(t, p, []previewBox{box}, diag)
			}
		})
	}
}
