// Animated-GIF capture for the README. Drives the same demo-repo server as
// the screenshot set, but records a *sequence* of frames through a scripted
// flow and encodes them as a looping GIF.
//
// Encoding is pure-Go (image/gif) — there is no ffmpeg/gifsicle dependency,
// matching prereview's "single binary, no external tools" ethos. Frames are
// captured at the desktop viewport, downscaled to the README display width
// (golang.org/x/image/draw, high-quality CatmullRom), then quantized to a
// 256-colour paletted image for GIF. The quantizer is pluggable (see
// quantizeFrame) so we can start with the stdlib fixed palette and swap to an
// adaptive one only if the syntax-highlighted UI bands badly.
//
// Usage (see cmd/screenshot/capture-gifs.sh):
//
//	go run ./cmd/screenshot --gif hero --url http://127.0.0.1:8765 --out docs
package main

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/draw"
	"image/gif"
	"image/png"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/ericpauley/go-quantize/quantize"
	xdraw "golang.org/x/image/draw"
)

// gifWidth is the on-page display width of the README GIFs. Capturing at the
// desktop viewport (1280) and downscaling here keeps file size in check while
// preserving the desktop three-column layout.
const gifWidth = 760

// Hero composite geometry. The hero stacks the browser pane above a terminal
// pane (see terminal.go). heroWidth is its encoded width; the terminal tab is
// captured at termW×termH and stacked under the 1280×800 browser screenshot
// with a heroGap-px gutter. Stacked (not side-by-side) keeps each pane full
// width — legible at GitHub's ~content-width image cap.
const (
	heroWidth = 820
	heroGap   = 12
	termW     = 1280
	termH     = 392
)

// gifRec accumulates paletted frames + per-frame delays (centiseconds).
type gifRec struct {
	frames []*image.Paletted
	delays []int
}

// grab screenshots a chromedp tab and decodes it to an image.
func grab(ctx context.Context) (image.Image, error) {
	var buf []byte
	if err := chromedp.Run(ctx, chromedp.CaptureScreenshot(&buf)); err != nil {
		return nil, err
	}
	return png.Decode(bytes.NewReader(buf))
}

// addFrame downscales src to targetW, quantizes it, and appends it as a frame
// held for holdCs centiseconds (1cs = 10ms).
func (r *gifRec) addFrame(src image.Image, holdCs, targetW int) {
	b := src.Bounds()
	w := targetW
	h := b.Dy() * w / b.Dx()
	scaled := image.NewRGBA(image.Rect(0, 0, w, h))
	xdraw.CatmullRom.Scale(scaled, scaled.Bounds(), src, b, xdraw.Over, nil)

	r.frames = append(r.frames, quantizeFrame(scaled))
	r.delays = append(r.delays, holdCs)
}

// capture grabs the current viewport and appends it as a gifWidth frame.
func (r *gifRec) capture(ctx context.Context, holdCs int) error {
	src, err := grab(ctx)
	if err != nil {
		return err
	}
	r.addFrame(src, holdCs, gifWidth)
	return nil
}

// captureComposite grabs both tabs, stacks browser-over-terminal, and appends
// the result as a heroWidth frame.
func (r *gifRec) captureComposite(browser, term context.Context, holdCs int) error {
	top, err := grab(browser)
	if err != nil {
		return err
	}
	bottom, err := grab(term)
	if err != nil {
		return err
	}
	r.addFrame(composeStacked(top, bottom, heroGap, color.White), holdCs, heroWidth)
	return nil
}

// quantizeFrame maps a full-colour frame onto a 256-colour paletted image
// using an ADAPTIVE per-frame palette (median cut). The stdlib fixed palette
// (Plan9) was tried first per the no-extra-deps preference, but it washed the
// pastel diff backgrounds (#d9f5d9 add / #f5d9d9 del) and the accent blues out
// to white/grey — colours that carry meaning in a code-review tool. A palette
// built from the frame's own colours preserves them. No dithering (draw.Src):
// the UI is mostly flat fills, so dithering would only add noise that bloats
// the LZW stream without improving the look.
func quantizeFrame(src image.Image) *image.Paletted {
	pal := quantize.MedianCutQuantizer{}.Quantize(make([]color.Color, 0, 256), src)
	dst := image.NewPaletted(src.Bounds(), pal)
	draw.Draw(dst, dst.Bounds(), src, src.Bounds().Min, draw.Src)
	return dst
}

func (r *gifRec) encode(path string) error {
	if len(r.frames) == 0 {
		return nil
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := gif.EncodeAll(f, &gif.GIF{Image: r.frames, Delay: r.delays, LoopCount: 0}); err != nil {
		return err
	}
	info, _ := f.Stat()
	log.Printf("wrote %s (%d frames, %d KB)", path, len(r.frames), info.Size()/1024)
	return nil
}

// runGif dispatches a single named flow against a running demo-repo server.
// repo is the demo working tree; only the hero flow needs it (to apply the
// scripted "Claude edit" on disk).
func runGif(allocCtx context.Context, url, name, repo, outDir string) {
	switch name {
	case "hero":
		gifHero(allocCtx, url, repo, outDir)
	case "image":
		gifImage(allocCtx, url, outDir)
	case "markdown":
		gifMarkdown(allocCtx, url, outDir)
	case "external":
		gifExternal(allocCtx, url, outDir)
	default:
		log.Fatalf("unknown gif flow %q (have: hero|image|markdown|external)", name)
	}
}

// heroFixedPayment is the scripted "Claude edit": it replaces the generic
// retry error with the gateway's real error wrapped via fmt.Errorf %w —
// exactly what the hero comment asks for ("surface the gateway's real error").
// The fixture is never compiled, so the new fmt import is purely visual. Authored
// (not a live LLM call) so `make gifs` reproduces the same diff every run.
const heroFixedPayment = `package payment

import (
	"errors"
	"fmt"
)

// Charge captures a payment for an order. Amount is in minor units (cents).
func Charge(orderID string, cents int64) error {
	if cents <= 0 {
		return errors.New("amount must be positive")
	}
	if orderID == "" {
		return errors.New("missing order id")
	}
	var lastErr error
	for attempt := 0; attempt < maxRetries; attempt++ {
		if err := gateway.Submit(orderID, cents); err == nil {
			return nil
		} else {
			lastErr = err
		}
	}
	return fmt.Errorf("charge failed after %d attempts: %w", maxRetries, lastErr)
}

// Refund reverses a prior capture in full.
func Refund(orderID string) error {
	return gateway.Reverse(orderID)
}
`

// scrollCommentJS centers the inline comment card in the viewport so the
// resolved strikethrough is visible in the captured frame.
const scrollCommentJS = `(()=>{const c=document.querySelector('.inline-comment');if(c)c.scrollIntoView({block:'center'});})()`

// gifHero — the full review→fix loop, browser stacked over terminal. The human
// comments and hands off (browser); the Claude Code skill reads the comment and
// edits payment.go (terminal, scripted per skill/SKILL.md); the fixed diff
// appears (browser); the human resolves the comment (browser). Per SKILL.md the
// terminal never claims to resolve — resolving is the human's action.
func gifHero(allocCtx context.Context, url, repo, outDir string) {
	// Two tabs: browser (prereview) above, terminal (Claude) below.
	bctx, bcancel := chromedp.NewContext(allocCtx)
	defer bcancel()
	bctx, bt := context.WithTimeout(bctx, 120*time.Second)
	defer bt()
	tctx, tcancel := chromedp.NewContext(allocCtx)
	defer tcancel()
	tctx, tt := context.WithTimeout(tctx, 120*time.Second)
	defer tt()

	rec := &gifRec{}

	// Terminal pane: launched, idle, waiting for the handoff. Mirrors the real
	// skill flow — /prereview prints a review URL the user taps to open.
	idle := []termLine{
		{"$ claude", "dim"},
		{"> /prereview", "prompt"},
		{"● Review session live — open in your browser:", "bullet"},
		{"  http://127.0.0.1:8420  (tap to open →)", "link"},
		{"", "sp"},
		{"waiting for handoff…", "dim"},
	}
	if err := chromedp.Run(tctx, chromedp.EmulateViewport(termW, termH)); err != nil {
		log.Printf("[gif:hero] term viewport: %v", err)
		return
	}
	if err := termInit(tctx); err != nil {
		log.Printf("[gif:hero] term init: %v", err)
		return
	}
	if err := termRender(tctx, idle); err != nil {
		log.Printf("[gif:hero] term render: %v", err)
		return
	}

	// Browser: open payment.go's diff.
	if err := chromedp.Run(bctx, base(url, "payment.go", 1280, 800)...); err != nil {
		log.Printf("[gif:hero] open diff: %v", err)
		return
	}
	_ = rec.captureComposite(bctx, tctx, 130) // diff open, terminal idle

	// Select the gateway.Submit line (L14) via the deep-link hash → composer
	// opens. We anchor here, not on the return (L18), on purpose: Claude's fix
	// rewrites the return line, which would mark a return-anchored comment
	// "outdated" (can't render inline). The gateway.Submit line is byte-identical
	// before and after the fix, so it re-anchors as "moved" and stays inline —
	// letting the human resolve it visibly. It's also exactly what the comment is
	// about ("surface the gateway's real error here").
	_ = chromedp.Run(bctx,
		chromedp.Navigate(navURL(url, "payment.go:L14")),
		chromedp.Sleep(700*time.Millisecond),
		chromedp.WaitVisible(`.composer textarea`, chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelector('.composer')?.scrollIntoView({block:'center'})`, nil),
		chromedp.Sleep(150*time.Millisecond),
	)
	_ = rec.captureComposite(bctx, tctx, 80) // empty composer

	// Type the comment in two chunks for a "typing" feel.
	_ = chromedp.Run(bctx, chromedp.SendKeys(`.composer textarea`, "Surface the gateway's real error ", chromedp.ByQuery), chromedp.Sleep(150*time.Millisecond))
	_ = rec.captureComposite(bctx, tctx, 45)
	_ = chromedp.Run(bctx, chromedp.SendKeys(`.composer textarea`, "here, not a generic string.", chromedp.ByQuery), chromedp.Sleep(150*time.Millisecond))
	_ = rec.captureComposite(bctx, tctx, 140) // full comment

	// Save → the comment becomes an inline card.
	_ = chromedp.Run(bctx, chromedp.Click(saveBtn, chromedp.ByQuery), chromedp.Sleep(600*time.Millisecond))
	_ = rec.captureComposite(bctx, tctx, 110)

	// Hand off → Claude: writes the marker and shows the "Handed off" toast.
	_ = chromedp.Run(bctx, chromedp.Click(`header.bar button[name='handOff']`, chromedp.ByQuery), chromedp.Sleep(800*time.Millisecond))
	_ = rec.captureComposite(bctx, tctx, 150) // toast; terminal still idle (event arriving)

	// Terminal: Claude receives the handoff and reads the comment. Keep the
	// launch lines + review URL (first 4), drop the blank + "waiting…" lines.
	recv := append(idle[:4:4],
		termLine{"", "sp"},
		termLine{"Read 1 comment — payment.go:14", "bullet"},
		termLine{`  "Surface the gateway's real error here, not a generic string."`, "dim"},
	)
	_ = termRender(tctx, recv)
	_ = rec.captureComposite(bctx, tctx, 150)

	// Terminal: Claude edits the file (the diff it writes).
	edit := append(recv[:len(recv):len(recv)],
		termLine{"", "sp"},
		termLine{"Edit payment.go", "bullet"},
		termLine{`-   return errors.New("charge failed after retries")`, "del"},
		termLine{`+   return fmt.Errorf("charge failed after %d attempts: %w", maxRetries, lastErr)`, "add"},
	)
	_ = termRender(tctx, edit)
	_ = rec.captureComposite(bctx, tctx, 180)

	// Apply the scripted edit on disk (no CSV surgery — Claude doesn't resolve).
	if err := applyHeroFix(repo); err != nil {
		log.Printf("[gif:hero] apply fix: %v", err)
		return
	}
	done := append(edit[:len(edit):len(edit)],
		termLine{"", "sp"},
		termLine{"Done — 1 file changed", "bullet"},
	)
	_ = termRender(tctx, done)
	_ = rec.captureComposite(bctx, tctx, 120)

	// Browser: force a real reload (bounce via about:blank — a same-document
	// hash change would NOT re-Mount) so Mount re-reads the file; the diff cache
	// busts on the new mtime and the fixed line now shows in the diff.
	_ = chromedp.Run(bctx,
		chromedp.Navigate("about:blank"),
		chromedp.Sleep(150*time.Millisecond),
		chromedp.Navigate(navURL(url, "payment.go")),
		chromedp.WaitVisible(`.code`, chromedp.ByQuery),
		chromedp.Sleep(900*time.Millisecond),
		chromedp.Evaluate(scrollCommentJS, nil),
		chromedp.Sleep(200*time.Millisecond),
	)
	_ = rec.captureComposite(bctx, tctx, 220) // payoff: the fix landed

	// Human resolves the comment (the real product action), then reveals
	// resolved comments so the struck-through card stays visible (resolved rows
	// are filtered from the diff by default — CommentsByEndLine honors
	// ShowResolved). Settle, then scroll the resolved card into view.
	_ = chromedp.Run(bctx,
		chromedp.Evaluate(`(()=>{const b=document.querySelector('.inline-comment button[name="toggleResolved"]')||document.querySelector('button[name="toggleResolved"]');if(b)b.click();})()`, nil),
		chromedp.Sleep(700*time.Millisecond),
		chromedp.Evaluate(`(()=>{const b=document.querySelector('button[name="toggleShowResolved"]');if(b)b.click();})()`, nil),
		chromedp.Sleep(900*time.Millisecond),
		chromedp.Evaluate(`(()=>{const c=document.querySelector('.inline-comment.is-resolved')||document.querySelector('.inline-comment');if(c)c.scrollIntoView({block:'center'});})()`, nil),
		chromedp.Sleep(250*time.Millisecond),
	)
	_ = rec.captureComposite(bctx, tctx, 260) // loop closed: comment resolved, held longest

	if err := rec.encode(filepath.Join(outDir, "hero.gif")); err != nil {
		log.Printf("[gif:hero] encode: %v", err)
	}
}

// applyHeroFix writes the scripted "Claude edit" into the demo repo's working
// tree so prereview re-renders the improved diff on the next mount.
func applyHeroFix(repo string) error {
	return os.WriteFile(filepath.Join(repo, "payment.go"), []byte(heroFixedPayment), 0o644)
}

// gifImage — reviewing a NON-code artifact: drag a region on a binary image and
// comment on it. Demonstrates that "review any artifact" is literal.
func gifImage(allocCtx context.Context, url, outDir string) {
	withCtx(allocCtx, "gif:image", func(ctx context.Context) error {
		rec := &gifRec{}
		imgSel := `img[src*="architecture.png"]`
		if err := chromedp.Run(ctx, base(url, "architecture.png", 1280, 800)...); err != nil {
			return err
		}
		var r struct{ X, Y, W, H float64 }
		if err := chromedp.Run(ctx,
			chromedp.WaitVisible(imgSel, chromedp.ByQuery),
			chromedp.Sleep(300*time.Millisecond),
			chromedp.Evaluate(`(()=>{const im=document.querySelector('`+imgSel+`');const b=im.getBoundingClientRect();return {X:b.x,Y:b.y,W:b.width,H:b.height};})()`, &r),
		); err != nil {
			return err
		}
		_ = rec.capture(ctx, 130) // the image, before annotation (examine/zoom mode)
		// #57: arm the region toggle — images pinch-zoom by default, so capture
		// the gesture only after switching to comment mode.
		_ = chromedp.Run(ctx,
			chromedp.Click(`button[name="toggleRegionSelect"]`, chromedp.ByQuery),
			chromedp.WaitVisible(`.image-with-areas.is-armed`, chromedp.ByQuery),
			chromedp.Sleep(200*time.Millisecond),
		)
		_ = rec.capture(ctx, 80) // armed — "Select a region to comment" engaged
		// Drag a rectangle → pending box + composer.
		_ = chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
			return mouseDrag(ctx, r.X+r.W*0.15, r.Y+r.H*0.15, r.X+r.W*0.62, r.Y+r.H*0.58)
		}), chromedp.Sleep(250*time.Millisecond))
		_ = rec.capture(ctx, 110) // box drawn, composer open
		_ = chromedp.Run(ctx, chromedp.SendKeys(`.composer textarea`, "This box overlaps the gateway lane — tighten the spacing.", chromedp.ByQuery), chromedp.Sleep(150*time.Millisecond))
		_ = rec.capture(ctx, 140) // typed
		_ = chromedp.Run(ctx, chromedp.Click(saveBtn, chromedp.ByQuery), chromedp.Sleep(600*time.Millisecond))
		_ = rec.capture(ctx, 190) // saved area comment
		return rec.encode(filepath.Join(outDir, "image-area.gif"))
	})
}

// gifMarkdown — reviewing rendered prose: click a rendered Markdown block and
// the comment anchors to the real source lines (block-level granularity).
func gifMarkdown(allocCtx context.Context, url, outDir string) {
	withCtx(allocCtx, "gif:markdown", func(ctx context.Context) error {
		rec := &gifRec{}
		if err := chromedp.Run(ctx, base(url, "guide.md", 1280, 800)...); err != nil {
			return err
		}
		if err := chromedp.Run(ctx, chromedp.WaitVisible(`.md-rendered`, chromedp.ByQuery), chromedp.Sleep(200*time.Millisecond)); err != nil {
			return err
		}
		_ = rec.capture(ctx, 140) // the rendered doc
		// Click the "A charge validates…" paragraph block → selectBlock opens
		// the composer anchored to that block's source lines.
		_ = chromedp.Run(ctx,
			chromedp.Evaluate(`(()=>{const els=[...document.querySelectorAll('.md-block .md-rendered')];const t=els.find(e=>/validates the amount/.test(e.textContent))||els[1];t.scrollIntoView({block:'center'});t.click();})()`, nil),
			chromedp.Sleep(500*time.Millisecond),
			chromedp.WaitVisible(`.composer textarea`, chromedp.ByQuery),
		)
		_ = rec.capture(ctx, 110) // block selected, composer open
		_ = chromedp.Run(ctx, chromedp.SendKeys(`.composer textarea`, "Say which gateway errors are retryable here.", chromedp.ByQuery), chromedp.Sleep(150*time.Millisecond))
		_ = rec.capture(ctx, 140) // typed
		_ = chromedp.Run(ctx, chromedp.Click(saveBtn, chromedp.ByQuery), chromedp.Sleep(600*time.Millisecond))
		_ = rec.capture(ctx, 190) // comment anchored under the block
		return rec.encode(filepath.Join(outDir, "markdown-block.gif"))
	})
}

// gifExternal — reviewing a LIVE local site (`--external`): arm region select,
// drag a box over the proxied page, comment. The strongest "review ANY
// artifact" proof. Drag sequence mirrors e2e_external_test.go.
func gifExternal(allocCtx context.Context, url, outDir string) {
	withCtx(allocCtx, "gif:external", func(ctx context.Context) error {
		rec := &gifRec{}
		if err := chromedp.Run(ctx,
			chromedp.EmulateViewport(1280, 860),
			chromedp.Navigate(url),
			chromedp.WaitVisible(`iframe.ext-frame`, chromedp.ByQuery),
			chromedp.WaitVisible(`.bar-external .ext-page`, chromedp.ByQuery),
			chromedp.Sleep(700*time.Millisecond),
		); err != nil {
			return err
		}
		_ = rec.capture(ctx, 130) // the live site, framed

		// Arm region select → page-surface overlay appears.
		if err := chromedp.Run(ctx,
			chromedp.Click(`button[name="toggleRegionSelect"]`, chromedp.ByQuery),
			chromedp.WaitVisible(`.region-overlay[data-surface="page"]`, chromedp.ByQuery),
			chromedp.Sleep(250*time.Millisecond),
		); err != nil {
			return err
		}
		_ = rec.capture(ctx, 90) // armed (button says "Selecting…")

		// Read the overlay rect and drag a box around the hero CTA.
		var ov struct{ X, Y, Width, Height float64 }
		if err := chromedp.Run(ctx, chromedp.Evaluate(`(() => {
			const b = document.querySelector('.region-overlay[data-surface="page"]').getBoundingClientRect();
			return { X: b.left, Y: b.top, Width: b.width, Height: b.height };
		})()`, &ov)); err != nil {
			return err
		}
		x1 := ov.X + ov.Width*0.34
		y1 := ov.Y + ov.Height*0.32
		x2 := ov.X + ov.Width*0.66
		y2 := ov.Y + ov.Height*0.46
		_ = chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
			return mouseDrag(ctx, x1, y1, x2, y2)
		}), chromedp.Sleep(300*time.Millisecond), chromedp.WaitVisible(`.ext-composer textarea`, chromedp.ByQuery))
		_ = rec.capture(ctx, 110) // box drawn, composer open

		_ = chromedp.Run(ctx, chromedp.SendKeys(`.ext-composer textarea`, "CTA is too low-contrast — darken it for AA.", chromedp.ByQuery), chromedp.Sleep(150*time.Millisecond))
		_ = rec.capture(ctx, 140) // typed

		_ = chromedp.Run(ctx,
			chromedp.Click(`.ext-composer button[name="addComment"]`, chromedp.ByQuery),
			chromedp.WaitVisible(`.pin-layer .area-overlay`, chromedp.ByQuery),
			chromedp.Sleep(400*time.Millisecond),
		)
		_ = rec.capture(ctx, 190) // pinned annotation on the live page
		return rec.encode(filepath.Join(outDir, "external-region.gif"))
	})
}
