//go:build browser

package e2e

import (
	"fmt"
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
// scrolls the block's cards into view and shows each comment's box where it
// was drawn, each in its own colour, which its card shares; Show boxes and
// Hide annotations apply; the marker follows the block when the preview
// resizes; and each shown box's corner bar resolves, deletes and hides.
func TestE2E_HTMLMarkers(t *testing.T) {
	for _, vp := range []struct {
		name string
		w, h int
	}{{"desktop", 1200, 800}, {"phone", 390, 844}} {
		t.Run(vp.name, func(t *testing.T) {
			p, diag := bootHTMLPreview(t, setupFixtureHTMLRegionRepo(t), vp.w, vp.h)

			// Three comments on the top section (line 4, the first 240px), on
			// different parts of it; the second overlaps the first.
			drawn := []previewBox{{0.05, 10, 0.4, 90}, {0.3, 30, 0.95, 120}, {0.2, 140, 0.7, 220}}
			for i, b := range drawn {
				drawHTMLRegionAt(t, p, fmt.Sprintf("Note %d", i+1), b, diag)
			}

			box := `.preview-pins .area-overlay[data-id="MB-4-4"]`
			waitJSDiag(t, p, `(() => { const b = document.querySelector('`+box+`'); return !!b && getComputedStyle(b).visibility === 'visible' && b.querySelector('.pin-btn').textContent.trim() === '3'; })()`, "one marker reading 3 on the top section", diag)
			if shown := evalInt(t, p, `[...document.querySelectorAll('.preview-pins .drawn-box')].filter(b => getComputedStyle(b).display !== 'none').length`); shown != 0 {
				t.Errorf("%d drawn boxes show before the marker is tapped; want none%s", shown, diag())
			}

			// The fixture's top section is the first 240px of the page, full width.
			onBlock := func(when string) {
				t.Helper()
				var g struct {
					BoxLeft, BoxTop, BoxW, BoxH float64
					FrameLeft, FrameTop, FrameW float64
					HitsPin                     bool
				}
				if err := chromedp.Run(p.ctx, chromedp.Evaluate(`(() => {
					const b = document.querySelector('`+box+`').getBoundingClientRect();
					const f = document.querySelector('iframe.html-preview').getBoundingClientRect();
					const hit = document.elementFromPoint(b.left + b.width / 2, b.top + b.height / 2);
					return { BoxLeft: b.left, BoxTop: b.top, BoxW: b.width, BoxH: b.height,
						FrameLeft: f.left, FrameTop: f.top, FrameW: f.width,
						HitsPin: !!(hit && hit.closest('.pin-layer')) };
				})()`, &g)); err != nil {
					t.Fatalf("%s: measure the marker box: %v%s", when, err, diag())
				}
				if math.Abs(g.BoxLeft-g.FrameLeft) > 2 || math.Abs(g.BoxTop-g.FrameTop) > 2 || math.Abs(g.BoxW-g.FrameW) > 2 || math.Abs(g.BoxH-240) > 3 {
					t.Errorf("%s: box %+v is not over the top section%s", when, g, diag())
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

			// Tap: the boxes show, and the block's card opens under them.
			// Centre the marker first: scrolled just into view it sits under the
			// sticky file header, which would take the click.
			tapCentred(t, p, box+` .pin-btn`, "tap the marker", diag)
			waitJSDiag(t, p, `document.querySelectorAll('.preview-pins .drawn-box.is-shown').length === 3 && (() => { const c = document.querySelector('.preview-card'); if (!c) return false; const r = c.getBoundingClientRect(); return c.querySelectorAll('.inline-comment').length === 3 && r.top < innerHeight && r.bottom > 0; })()`, "the three boxes shown and the card with their comments", diag)
			checkDrawnBoxes(t, p, drawn, diag)
			if w := evalInt(t, p, `parseInt(getComputedStyle(document.querySelector('`+box+`')).borderTopWidth)`); w != 0 {
				t.Errorf("the block is outlined (%dpx) although every comment on it has its own box%s", w, diag())
			}

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

			checkAnnotationBar(t, p, box, diag)
		})
	}
}

// drawHTMLRegion arms Annotate on an HTML file's preview, drags a box over the
// top ~100px of the preview (the top block of the region fixtures) and saves a
// comment with body.
func drawHTMLRegion(t *testing.T, p *runningPrereview, body string, diag func() string) {
	t.Helper()
	drawHTMLRegionAt(t, p, body, previewBox{0.1, 12, 0.8, 100}, diag)
}

// previewBox is a box to draw on an HTML preview: X1/X2 as fractions of its
// width, Y1/Y2 in pixels from its top.
type previewBox struct{ X1, Y1, X2, Y2 float64 }

// drawHTMLRegionAt arms Annotate on an HTML file's preview, drags box and saves
// a comment with body.
func drawHTMLRegionAt(t *testing.T, p *runningPrereview, body string, box previewBox, diag func() string) {
	t.Helper()
	openHTMLComposer(t, p, box, "the composer for "+body, diag)
	if err := chromedp.Run(p.ctx,
		chromedp.SendKeys(`.composer textarea`, body, chromedp.ByQuery),
		chromedp.Click(`.composer button[name="addComment"]`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("save %q: %v%s", body, err, diag())
	}
	waitJSDiag(t, p, `!document.querySelector('.composer textarea')`, "the composer closes after saving", diag)
}

// openHTMLComposer arms Annotate on an HTML file's preview, drags box and waits
// for the composer.
func openHTMLComposer(t *testing.T, p *runningPrereview, box previewBox, what string, diag func() string) {
	t.Helper()
	var ir struct{ Left, Top, Width, Height float64 }
	if err := chromedp.Run(p.ctx,
		armAnnotate(),
		chromedp.WaitVisible(`.region-overlay[data-surface="html"]`, chromedp.ByQuery),
		// Let the region-select directive attach first: a drag that beats it is lost.
		chromedp.Sleep(300*time.Millisecond),
		// The preview is an opaque-origin frame, but its own rect is readable.
		chromedp.Evaluate(`(() => { const r = document.querySelector('iframe.html-preview').getBoundingClientRect(); return { Left: r.left, Top: r.top, Width: r.width, Height: r.height }; })()`, &ir),
	); err != nil {
		t.Fatalf("arm Annotate on the preview: %v%s", err, diag())
	}
	if ir.Width < 50 || ir.Height < 200 {
		t.Fatalf("the preview's rect looks wrong: %+v%s", ir, diag())
	}
	x1, y1, x2, y2 := ir.Left+ir.Width*box.X1, ir.Top+box.Y1, ir.Left+ir.Width*box.X2, ir.Top+box.Y2
	if err := chromedp.Run(p.ctx,
		cdpinput.DispatchMouseEvent(cdpinput.MousePressed, x1, y1).WithButton(cdpinput.Left).WithClickCount(1),
		cdpinput.DispatchMouseEvent(cdpinput.MouseMoved, (x1+x2)/2, (y1+y2)/2).WithButton(cdpinput.Left),
		cdpinput.DispatchMouseEvent(cdpinput.MouseMoved, x2, y2).WithButton(cdpinput.Left),
		cdpinput.DispatchMouseEvent(cdpinput.MouseReleased, x2, y2).WithButton(cdpinput.Left).WithClickCount(1),
	); err != nil {
		t.Fatalf("drag over the preview: %v%s", err, diag())
	}
	// Poll a fresh query: a re-render can replace the textarea right after the
	// drag, and WaitVisible would keep polling the detached node.
	waitJSDiag(t, p, `(() => { const ta = document.querySelector('.composer textarea'); return !!ta && ta.offsetParent !== null; })()`, what, diag)
}

// annColourJS defines colour(el): the annotation colour class (ann-cN) an
// element carries, or ”.
const annColourJS = `const colour = el => [...el.classList].find(c => c.startsWith('ann-c')) || '';`

// checkDrawnBoxes checks the preview's drawn boxes after a marker tap: one per
// box in want, shown where it was drawn, each in a different colour, and each
// card under the preview in its box's colour.
func checkDrawnBoxes(t *testing.T, p *runningPrereview, want []previewBox, diag func() string) {
	t.Helper()
	var got struct {
		Frame struct{ Left, Top, Width float64 }
		Boxes []struct {
			ID, Class, Color         string
			Left, Top, Right, Bottom float64
			Shown                    bool
		}
		Cards map[string]string
	}
	if err := chromedp.Run(p.ctx, chromedp.Evaluate(`(() => {
		const f = document.querySelector('iframe.html-preview').getBoundingClientRect();
		`+annColourJS+`
		const cards = {};
		for (const c of document.querySelectorAll('.preview-card .inline-comment')) cards[c.dataset.key] = colour(c);
		return {
			Frame: { Left: f.left, Top: f.top, Width: f.width },
			Boxes: [...document.querySelectorAll('.preview-pins .drawn-box')].map(b => {
				const r = b.getBoundingClientRect();
				return { ID: b.dataset.box, Class: colour(b), Color: getComputedStyle(b).borderTopColor,
					Left: r.left, Top: r.top, Right: r.right, Bottom: r.bottom,
					Shown: getComputedStyle(b).display !== 'none' };
			}),
			Cards: cards,
		};
	})()`, &got)); err != nil {
		t.Fatalf("read the drawn boxes: %v%s", err, diag())
	}
	if len(got.Boxes) != len(want) {
		t.Fatalf("%d drawn boxes, want %d%s", len(got.Boxes), len(want), diag())
	}
	colours := map[string]bool{}
	for i, b := range got.Boxes {
		w := want[i] // boxes render in creation order
		f := got.Frame
		if !b.Shown {
			t.Errorf("box %d is hidden after the tap", i+1)
		}
		if math.Abs(b.Left-(f.Left+f.Width*w.X1)) > 3 || math.Abs(b.Top-(f.Top+w.Y1)) > 3 ||
			math.Abs(b.Right-(f.Left+f.Width*w.X2)) > 3 || math.Abs(b.Bottom-(f.Top+w.Y2)) > 3 {
			t.Errorf("box %d at (%.0f,%.0f)-(%.0f,%.0f), drawn at %+v over the frame %+v%s", i+1, b.Left, b.Top, b.Right, b.Bottom, w, f, diag())
		}
		if b.Class == "" || colours[b.Color] {
			t.Errorf("box %d has no colour of its own (class %q, colour %s)", i+1, b.Class, b.Color)
		}
		colours[b.Color] = true
		if got.Cards[b.ID] != b.Class {
			t.Errorf("box %d is %q but its card is %q", i+1, b.Class, got.Cards[b.ID])
		}
	}
}

// checkAnnotationBar drives the corner bar on the boxes a preview marker opens:
// every shown box has one, with tap-sized buttons; a tap on the first box,
// which the second overlaps, brings it to the front; Resolve and Reopen flip
// the comment; Delete removes it and Undo brings it back; Hide hides one box;
// a second tap on the marker closes them all, and the next shows them all.
func checkAnnotationBar(t *testing.T, p *runningPrereview, marker string, diag func() string) {
	t.Helper()
	tap := func(sel, what string) { t.Helper(); tapCentred(t, p, sel, what, diag) }
	shown := `document.querySelectorAll('.preview-pins .drawn-box.is-shown').length`
	waitJSDiag(t, p, shown+` === 3 && document.querySelectorAll('.preview-pins .drawn-box .anno-bar').length === 3`, "a bar on each shown box", diag)
	if small := evalInt(t, p, `[...document.querySelectorAll('.preview-pins .anno-act')].filter(b => { const r = b.getBoundingClientRect(); return r.width < 28 || r.height < 28 || !b.getAttribute('aria-label'); }).length`); small != 0 {
		t.Errorf("%d bar buttons are under 28px or unlabelled%s", small, diag())
	}
	var first string
	if err := chromedp.Run(p.ctx, chromedp.Evaluate(`document.querySelector('.preview-pins .drawn-box').dataset.box`, &first)); err != nil {
		t.Fatalf("read a box id: %v", err)
	}
	firstBox := `.preview-pins .drawn-box[data-box="` + first + `"]`
	bar := firstBox + ` .anno-bar`

	// Where the first two boxes overlap, the second is on top until the
	// first is tapped.
	onTop := `(() => { const a = document.querySelector('` + firstBox + `').getBoundingClientRect(); const el = document.elementFromPoint(a.right - 4, a.bottom - 4); const b = el && el.closest('.drawn-box'); return b ? b.dataset.box : ''; })()`
	var top string
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`document.querySelector('`+firstBox+`').scrollIntoView({block: 'center'}), true`, nil),
		chromedp.Evaluate(onTop, &top),
	); err != nil || top == "" || top == first {
		t.Fatalf("before the tap, the overlap shows %q (err %v), want the second box%s", top, err, diag())
	}
	tapCentred(t, p, firstBox, "tap the first box", diag)
	waitJSDiag(t, p, onTop+` === '`+first+`' && !!document.querySelector('`+firstBox+`.is-front')`, "the tapped box comes to the front", diag)
	card := `.preview-card .inline-comment[data-key="` + first + `"]`

	tap(bar+` .anno-act[aria-label="Resolve"]`, "resolve from the bar")
	waitJSDiag(t, p, `!!document.querySelector('`+card+`.is-resolved') && !!document.querySelector('`+bar+` .anno-act[aria-label="Reopen"]')`, "resolved, and the bar offers Reopen", diag)
	tap(bar+` .anno-act[aria-label="Reopen"]`, "reopen from the bar")
	waitJSDiag(t, p, `!!document.querySelector('`+card+`') && !document.querySelector('`+card+`.is-resolved')`, "reopened", diag)

	tap(bar+` .anno-act[aria-label="Delete"]`, "delete from the bar")
	waitJSDiag(t, p, `!document.querySelector('`+card+`') && !!document.querySelector('.undo-toast button[name="undoDelete"]')`, "deleted, with the Undo notice", diag)
	if err := chromedp.Run(p.ctx, chromedp.Evaluate(`document.querySelector('.undo-toast button[name="undoDelete"]').click(), true`, nil)); err != nil {
		t.Fatalf("undo: %v", err)
	}
	waitJSDiag(t, p, `!!document.querySelector('`+card+`')`, "Undo brings the comment back", diag)

	waitJSDiag(t, p, shown+` === 3`, "the boxes still shown", diag)
	tap(bar+` .anno-act[aria-label="Hide"]`, "hide one box from its bar")
	waitJSDiag(t, p, shown+` === 2 && !document.querySelector('`+firstBox+`.is-shown')`, "Hide hides only that box", diag)

	tap(marker+` .pin-btn`, "tap the marker")
	waitJSDiag(t, p, shown+` === 0`, "a marker tap closes the rest", diag)
	tap(marker+` .pin-btn`, "tap the marker again")
	waitJSDiag(t, p, shown+` === 3`, "the next tap shows all three again", diag)
}

// tapCentred scrolls sel to the middle of the viewport, clear of the sticky
// header, and clicks it.
func tapCentred(t *testing.T, p *runningPrereview, sel, what string, diag func() string) {
	t.Helper()
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`document.querySelector('`+sel+`').scrollIntoView({block: 'center'}), true`, nil),
		chromedp.Click(sel, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("%s: %v%s", what, err, diag())
	}
}
