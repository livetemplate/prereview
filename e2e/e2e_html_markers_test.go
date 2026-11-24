//go:build browser

package e2e

import (
	"math"
	"testing"
	"time"

	cdpinput "github.com/chromedp/cdproto/input"
	"github.com/chromedp/chromedp"
)

// TestE2E_HTMLMarkers: comments on an HTML file's rendered preview show as one
// marker per commented block, with the block's count, placed over the block
// from the rects the preview posts. The block stays readable and clickable;
// the marker ticks once the agent has worked on every open comment; tapping it
// scrolls the block's cards into view and outlines the block; Show boxes and
// Hide annotations apply; and the marker follows the block when the preview
// resizes.
func TestE2E_HTMLMarkers(t *testing.T) {
	for _, vp := range []struct {
		name string
		w, h int
	}{{"desktop", 1200, 800}, {"phone", 390, 844}} {
		t.Run(vp.name, func(t *testing.T) {
			p := bootChromeAgainstRepo(t, setupFixtureHTMLRegionRepo(t), vp.w, vp.h, "--agent")
			// Bound the browser work so a stuck step fails with the diag dump.
			ctx, cancel := within(p, 90*time.Second)
			t.Cleanup(cancel)
			p.ctx = ctx
			p.waitReadyAt(vp.w, vp.h)
			diag := captureDiag(t, p)
			p.clickFile("region.html")
			waitPreviewBridgeReady(t, p)

			// Two comments on the top section (line 4): drag over its top part.
			for _, body := range []string{"Heading too small", "Needs more contrast"} {
				var ir struct{ Left, Top, Width float64 }
				if err := chromedp.Run(p.ctx,
					chromedp.Click(`button[name="toggleRegionSelect"]`, chromedp.ByQuery),
					chromedp.WaitVisible(`.region-overlay[data-surface="html"]`, chromedp.ByQuery),
					// Let the region-select directive attach before the drag (as
					// checkHTMLRegionSelect does); a drag that beats it is lost.
					chromedp.Sleep(300*time.Millisecond),
					chromedp.Evaluate(`(() => { const r = document.querySelector('iframe.html-preview').getBoundingClientRect(); return { Left: r.left, Top: r.top, Width: r.width }; })()`, &ir),
				); err != nil {
					t.Fatalf("arm Annotate: %v%s", err, diag())
				}
				x1, y1, x2, y2 := ir.Left+ir.Width*0.1, ir.Top+12, ir.Left+ir.Width*0.8, ir.Top+100
				if err := chromedp.Run(p.ctx,
					cdpinput.DispatchMouseEvent(cdpinput.MousePressed, x1, y1).WithButton(cdpinput.Left).WithClickCount(1),
					cdpinput.DispatchMouseEvent(cdpinput.MouseMoved, x2, y2).WithButton(cdpinput.Left),
					cdpinput.DispatchMouseEvent(cdpinput.MouseReleased, x2, y2).WithButton(cdpinput.Left).WithClickCount(1),
				); err != nil {
					t.Fatalf("drag: %v", err)
				}
				// Poll a fresh query: a re-render can replace the textarea right after
				// the drag, and WaitVisible would keep polling the detached node.
				waitJSDiag(t, p, `(() => { const ta = document.querySelector('.composer textarea'); return !!ta && ta.offsetParent !== null; })()`, "the composer for "+body, diag)
				if err := chromedp.Run(p.ctx,
					chromedp.SendKeys(`.composer textarea`, body, chromedp.ByQuery),
					chromedp.Click(`.composer button[name="addComment"]`, chromedp.ByQuery),
				); err != nil {
					t.Fatalf("comment %q: %v%s", body, err, diag())
				}
				waitJSDiag(t, p, `!document.querySelector('.composer textarea')`, "the composer closes after saving", diag)
			}

			box := `.preview-pins .area-overlay[data-id="MB-4-4"]`
			waitJSDiag(t, p, `(() => { const b = document.querySelector('`+box+`'); return !!b && getComputedStyle(b).visibility === 'visible' && b.querySelector('.pin-btn').textContent.trim() === '2'; })()`, "one marker reading 2 on the top section", diag)

			type geom struct {
				BoxLeft, BoxTop, BoxW, BoxH float64
				FrameLeft, FrameTop, FrameW float64
				HitsPin                     bool
			}
			measure := func() geom {
				t.Helper()
				var g geom
				if err := chromedp.Run(p.ctx, chromedp.Evaluate(`(() => {
					const b = document.querySelector('`+box+`').getBoundingClientRect();
					const f = document.querySelector('iframe.html-preview').getBoundingClientRect();
					const hit = document.elementFromPoint(b.left + b.width / 2, b.top + b.height / 2);
					return { BoxLeft: b.left, BoxTop: b.top, BoxW: b.width, BoxH: b.height,
						FrameLeft: f.left, FrameTop: f.top, FrameW: f.width,
						HitsPin: !!(hit && hit.closest('.pin-layer')) };
				})()`, &g)); err != nil {
					t.Fatalf("measure the marker box: %v%s", err, diag())
				}
				return g
			}
			// The fixture's top section is the first 240px of the page, full width.
			onBlock := func(when string) {
				t.Helper()
				g := measure()
				if math.Abs(g.BoxLeft-g.FrameLeft) > 2 || math.Abs(g.BoxTop-g.FrameTop) > 2 || math.Abs(g.BoxW-g.FrameW) > 2 || math.Abs(g.BoxH-240) > 3 {
					t.Errorf("%s: box %+v is not over the top section (frame at %v,%v width %v)%s", when, g, g.FrameLeft, g.FrameTop, g.FrameW, diag())
				}
				if g.HitsPin {
					t.Errorf("%s: a tap at the block's centre lands on the marker box, not the preview", when)
				}
			}
			onBlock("after saving")

			// The agent works on both: the marker ticks.
			for _, row := range p.readCSV()[1:] {
				mustCLI(t, p, "done", "--out", p.store, row[0])
			}
			waitJSDiag(t, p, `!!document.querySelector('`+box+`.is-acted')`, "the marker ticks once both are worked on", diag)

			// Tap: the box is outlined and the block's cards scroll into view.
			// Centre the marker first: scrolled just into view it sits under the
			// sticky file header, which would take the click.
			if err := chromedp.Run(p.ctx,
				chromedp.Evaluate(`document.querySelector('`+box+` .pin-btn').scrollIntoView({block: 'center'}), true`, nil),
				chromedp.Click(box+` .pin-btn`, chromedp.ByQuery),
			); err != nil {
				t.Fatalf("tap the marker: %v%s", err, diag())
			}
			waitJSDiag(t, p, `!!document.querySelector('`+box+`.is-focused') && (() => { const r = document.querySelector('.html-block[data-key="HB-4-4"]').getBoundingClientRect(); return r.top < innerHeight && r.bottom > 0; })()`, "the box outlined and its cards in view", diag)

			// Show boxes, then Hide annotations.
			if err := chromedp.Run(p.ctx, chromedp.KeyEvent("b")); err != nil {
				t.Fatalf("press b: %v", err)
			}
			waitJSDiag(t, p, `!!document.querySelector('.preview-stage.show-boxes `+box+`')`, "Show boxes on the preview", diag)
			if err := chromedp.Run(p.ctx, chromedp.KeyEvent("b")); err != nil {
				t.Fatalf("press b again: %v", err)
			}
			if err := chromedp.Run(p.ctx, chromedp.Evaluate(`document.querySelector('button[name="toggleMarks"]').click(), true`, nil)); err != nil {
				t.Fatalf("hide annotations: %v", err)
			}
			waitJSDiag(t, p, `getComputedStyle(document.querySelector('`+box+`')).display === 'none'`, "Hide annotations hides the marker", diag)
			if err := chromedp.Run(p.ctx, chromedp.Evaluate(`document.querySelector('button[name="toggleMarks"]').click(), true`, nil)); err != nil {
				t.Fatalf("show annotations: %v", err)
			}
			waitJSDiag(t, p, `getComputedStyle(document.querySelector('`+box+`')).display !== 'none'`, "the marker back", diag)

			// The marker follows the block when the preview resizes.
			if err := chromedp.Run(p.ctx, chromedp.EmulateViewport(int64(vp.w*3/4), int64(vp.h)), chromedp.Sleep(800*time.Millisecond)); err != nil {
				t.Fatalf("resize: %v", err)
			}
			onBlock("after a resize")
		})
	}
}
