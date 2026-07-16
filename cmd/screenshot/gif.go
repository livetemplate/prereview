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
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/gif"
	"image/png"
	"log"
	"os"
	"os/exec"
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
// repo is the demo working tree; the hero flow needs it to apply the scripted
// "Claude edit" on disk, and the versions/thread flows additionally need bin to
// drive the agent-side subcommands (status/reply) that seed real state.
func runGif(allocCtx context.Context, url, name, repo, bin, outDir string) {
	switch name {
	case "hero":
		gifHero(allocCtx, url, repo, outDir)
	case "image":
		gifImage(allocCtx, url, outDir)
	case "markdown":
		gifMarkdown(allocCtx, url, outDir)
	case "external":
		gifExternal(allocCtx, url, outDir)
	case "suggestion":
		gifSuggestion(allocCtx, url, outDir)
	case "search":
		gifSearch(allocCtx, url, outDir)
	case "themes":
		gifThemes(allocCtx, url, outDir)
	case "versions":
		gifVersions(allocCtx, url, repo, bin, outDir)
	case "thread":
		gifThread(allocCtx, url, repo, bin, outDir)
	default:
		log.Fatalf("unknown gif flow %q (have: hero|image|markdown|external|suggestion|search|themes|versions|thread)", name)
	}
}

// agentRun invokes the prereview binary as the coding agent would (status/reply)
// against the demo store, propagating failures. It is deliberately NOT best-effort:
// a swallowed `reply`/`status` error produces a GIF missing its payload, so the
// caller aborts the flow rather than encode a misleading recording.
func agentRun(bin string, args ...string) error {
	cmd := exec.Command(bin, args...)
	cmd.Env = append(os.Environ(), "PREREVIEW_NO_UPDATE=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %v: %w\n%s", filepath.Base(bin), args, err, out)
	}
	log.Printf("[gif] %s %v → %s", filepath.Base(bin), args, bytes.TrimSpace(out))
	return nil
}

// backoffFixedPayment is the scripted agent edit for the versions/thread flows: it
// gives the retry loop exponential backoff and a 5s context deadline — exactly what
// the changelog and the reviewer's comment describe. Authored (not a live LLM call)
// so `make gifs` reproduces the same version diff every run.
const backoffFixedPayment = `package payment

import (
	"context"
	"errors"
	"time"
)

// Charge captures a payment for an order. Amount is in minor units (cents).
func Charge(orderID string, cents int64) error {
	if cents <= 0 {
		return errors.New("amount must be positive")
	}
	if orderID == "" {
		return errors.New("missing order id")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	backoff := 50 * time.Millisecond
	for attempt := 0; attempt < maxRetries; attempt++ {
		if err := gateway.Submit(orderID, cents); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
			backoff *= 2
		}
	}
	return errors.New("charge failed after retries")
}

// Refund reverses a prior capture in full.
func Refund(orderID string) error {
	return gateway.Reverse(orderID)
}
`

// gifVersions — the artifact version store (#90/#155): the agent records a real
// version (status working → edit payment.go → status done "<changelog>"), then the
// reviewer opens the Versions panel to read the AI changelog and diff the baseline
// against the current file. Composite (browser over terminal), like gifHero, to
// show the version was produced by a finished agent batch. Runs against its OWN
// fresh demo-repo server so the seeded state never bleeds into the other GIFs.
func gifVersions(allocCtx context.Context, url, repo, bin, outDir string) {
	if bin == "" || repo == "" {
		log.Printf("[gif:versions] needs --bin and --repo")
		return
	}

	// 1) Agent records a version. The server's 750ms status watcher checkpoints on
	// the working→done transition, binding the done message as the changelog (#155).
	// The two writes must straddle at least one poll: if working→done lands inside a
	// single 750ms window the watcher only ever sees "done" (no working→done edge)
	// and never checkpoints, so sleep >1 interval between them.
	// Flag order matters: Go's flag package stops at the first positional, so --out
	// must precede the <state> <message> positionals or it is silently ignored and
	// the status file lands in the CWD instead of the demo store the server watches.
	if err := agentRun(bin, "status", "--out", repo, "working", "Reworking the charge retry loop"); err != nil {
		log.Printf("[gif:versions] %v", err)
		return
	}
	time.Sleep(1200 * time.Millisecond)
	if err := os.WriteFile(filepath.Join(repo, "payment.go"), []byte(backoffFixedPayment), 0o644); err != nil {
		log.Printf("[gif:versions] write payment.go: %v", err)
		return
	}
	const changelog = "Add exponential backoff and a 5s context deadline to the retry loop so a stalled gateway can't hammer or hang a charge."
	if err := agentRun(bin, "status", "--out", repo, "done", changelog); err != nil {
		log.Printf("[gif:versions] %v", err)
		return
	}
	// Let the checkpoint land (watcher polls every 750ms) before the browser mounts,
	// so the very first render already sees version 2.
	time.Sleep(1500 * time.Millisecond)

	bctx, bcancel := chromedp.NewContext(allocCtx)
	defer bcancel()
	bctx, bt := context.WithTimeout(bctx, 90*time.Second)
	defer bt()
	tctx, tcancel := chromedp.NewContext(allocCtx)
	defer tcancel()
	tctx, tt := context.WithTimeout(tctx, 90*time.Second)
	defer tt()

	rec := &gifRec{}

	// Terminal: the agent finished a batch, so a new version was recorded.
	term := []termLine{
		{"$ claude", "dim"},
		{"> /prereview", "prompt"},
		{"● Applied your comment — payment.go", "bullet"},
		{"Edit payment.go", "bullet"},
		{`+   ctx, cancel := context.WithTimeout(…, 5*time.Second)`, "add"},
		{`+   case <-time.After(backoff): backoff *= 2`, "add"},
		{"", "sp"},
		{"✓ Done — recorded version 2", "bullet"},
	}
	if err := chromedp.Run(tctx, chromedp.EmulateViewport(termW, termH)); err != nil {
		log.Printf("[gif:versions] term viewport: %v", err)
		return
	}
	if err := termInit(tctx); err != nil {
		log.Printf("[gif:versions] term init: %v", err)
		return
	}
	if err := termRender(tctx, term); err != nil {
		log.Printf("[gif:versions] term render: %v", err)
		return
	}

	// Browser: open the review, then select payment.go by CLICKING it in the drawer
	// — NOT via the #hash. The deep-link path sets the file + diff but does not
	// refresh the Versions panel (it keeps the mount-default file's history), so a
	// hash nav would show "1 · Original"; a click routes through selectFile →
	// applyVersionList and shows both versions. Poll for the count to reach 2.
	if err := chromedp.Run(bctx, base(url, "", 1280, 800)...); err != nil {
		log.Printf("[gif:versions] open review: %v", err)
		return
	}
	_ = chromedp.Run(bctx,
		chromedp.Evaluate(`[...document.querySelectorAll('button[name="selectFile"]')].find(b=>/payment\.go/.test(b.textContent))?.click()`, nil),
		chromedp.Sleep(400*time.Millisecond),
	)
	if err := chromedp.Run(bctx, chromedp.Poll(`document.querySelector('.versions-count')?.textContent==='2'`, nil, chromedp.WithPollingTimeout(10*time.Second))); err != nil {
		log.Printf("[gif:versions] versions-count never reached 2: %v", err)
		return
	}
	_ = rec.captureComposite(bctx, tctx, 160) // the diff, Versions chip shows 2

	// Open the Versions panel (client-only lvt-el toggle).
	_ = chromedp.Run(bctx, clickJS(`.versions-dropdown .versions-trigger`), chromedp.Sleep(500*time.Millisecond))
	_ = rec.captureComposite(bctx, tctx, 150) // panel: Agent edit (current) + Original

	// Expand the top row (Agent edit, current) → reveals the AI changelog.
	_ = chromedp.Run(bctx,
		chromedp.Evaluate(`document.querySelector('.version-row .version-summary-toggle')?.click()`, nil),
		chromedp.Sleep(450*time.Millisecond))
	_ = rec.captureComposite(bctx, tctx, 240) // changelog visible

	// Expand the Original (baseline) row — it's non-current, so it carries the
	// Diff/Restore actions — and diff it against the current file.
	_ = chromedp.Run(bctx,
		chromedp.Evaluate(`document.querySelectorAll('.version-row .version-summary-toggle')[1]?.click()`, nil),
		chromedp.Sleep(400*time.Millisecond))
	_ = rec.captureComposite(bctx, tctx, 160) // baseline row: View / Diff / Restore
	_ = chromedp.Run(bctx,
		clickJS(`.version-row:last-child button[name="diffVersion"]`),
		chromedp.Poll(`!!document.querySelector('.version-view-banner')`, nil, chromedp.WithPollingTimeout(10*time.Second)),
		chromedp.Sleep(500*time.Millisecond))
	_ = rec.captureComposite(bctx, tctx, 320) // payoff: the read-only version diff

	if err := rec.encode(filepath.Join(outDir, "versions.gif")); err != nil {
		log.Printf("[gif:versions] encode: %v", err)
	}
}

// gifThread — the two-way conversation (#149): the reviewer comments, the agent
// replies (`prereview reply`) to explain what it did and ask a follow-up question,
// and the reviewer answers back inline. Composite (browser over terminal) so both
// sides of the thread are visible. Runs against its OWN fresh demo-repo server (the
// retry-loop working tree), so the "no backoff or ctx" comment lands naturally.
func gifThread(allocCtx context.Context, url, repo, bin, outDir string) {
	if bin == "" || repo == "" {
		log.Printf("[gif:thread] needs --bin and --repo")
		return
	}

	bctx, bcancel := chromedp.NewContext(allocCtx)
	defer bcancel()
	bctx, bt := context.WithTimeout(bctx, 120*time.Second)
	defer bt()
	tctx, tcancel := chromedp.NewContext(allocCtx)
	defer tcancel()
	tctx, tt := context.WithTimeout(tctx, 120*time.Second)
	defer tt()

	rec := &gifRec{}

	idle := []termLine{
		{"$ claude", "dim"},
		{"> /prereview", "prompt"},
		{"● Review session live — open in your browser:", "bullet"},
		{"  http://127.0.0.1:8420  (tap to open →)", "link"},
		{"", "sp"},
		{"watching the queue…", "dim"},
	}
	if err := chromedp.Run(tctx, chromedp.EmulateViewport(termW, termH)); err != nil {
		log.Printf("[gif:thread] term viewport: %v", err)
		return
	}
	if err := termInit(tctx); err != nil {
		log.Printf("[gif:thread] term init: %v", err)
		return
	}
	if err := termRender(tctx, idle); err != nil {
		log.Printf("[gif:thread] term render: %v", err)
		return
	}

	// Browser: comment on the retry loop (L13 in the working tree).
	if err := chromedp.Run(bctx, base(url, "payment.go", 1280, 800)...); err != nil {
		log.Printf("[gif:thread] open payment.go: %v", err)
		return
	}
	_ = chromedp.Run(bctx,
		chromedp.Navigate(navURL(url, "payment.go:L13")),
		chromedp.Sleep(700*time.Millisecond),
		chromedp.WaitVisible(`.composer textarea`, chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelector('.composer')?.scrollIntoView({block:'center'})`, nil),
		chromedp.Sleep(150*time.Millisecond),
	)
	_ = rec.captureComposite(bctx, tctx, 80) // empty composer
	_ = chromedp.Run(bctx, chromedp.SendKeys(`.composer textarea`, "This retry loop has no backoff or ctx — a failing gateway gets hammered.", chromedp.ByQuery), chromedp.Sleep(150*time.Millisecond))
	_ = rec.captureComposite(bctx, tctx, 140) // typed
	_ = chromedp.Run(bctx, chromedp.Click(saveBtn, chromedp.ByQuery), chromedp.Sleep(700*time.Millisecond))
	_ = rec.captureComposite(bctx, tctx, 150) // comment saved

	// Read the new comment's id from its card (the delete/reply forms carry it).
	var id string
	_ = chromedp.Run(bctx, chromedp.Evaluate(`document.querySelector('.inline-comment input[name="id"]')?.value||''`, &id))
	if id == "" {
		log.Printf("[gif:thread] could not read comment id from the saved card")
		return
	}

	// Agent reads the comment and replies (`prereview reply` appends to
	// agent-replies.jsonl). No status choreography here — the flow is about the
	// conversation, and the scripted terminal narrates the agent side.
	recv := append(idle[:4:4],
		termLine{"", "sp"},
		termLine{"Read 1 comment — payment.go:13", "bullet"},
		termLine{`  "…no backoff or ctx — a failing gateway gets hammered."`, "dim"},
	)
	_ = termRender(tctx, recv)
	_ = rec.captureComposite(bctx, tctx, 150)

	// Conditional tense on purpose: the flow deliberately does NOT edit the file, so
	// the diff still shows the bare retry loop. An agent that asks before editing is
	// both coherent with the visible code and the more realistic interaction.
	const reply = "Good catch — I can add exponential backoff. Want a context deadline too, or keep it unbounded?"
	if err := agentRun(bin, "reply", "--body", reply, "--out", repo, id); err != nil {
		log.Printf("[gif:thread] %v", err)
		return
	}
	replied := append(recv[:len(recv):len(recv)],
		termLine{"", "sp"},
		termLine{"↳ Replied — asked whether to add a ctx deadline", "bullet"},
	)
	_ = termRender(tctx, replied)

	// Browser: reload so Mount re-reads agent-replies.jsonl — the agent's reply now
	// shows in the thread. Navigate to the bare file (no line hash) so no fresh
	// composer opens over the card; poll for the agent entry, then scroll to it.
	_ = chromedp.Run(bctx,
		chromedp.Navigate("about:blank"),
		chromedp.Sleep(150*time.Millisecond),
		chromedp.Navigate(navURL(url, "payment.go")),
		chromedp.WaitVisible(`.code`, chromedp.ByQuery),
		chromedp.Poll(`!!document.querySelector('.thread .thread-agent')`, nil, chromedp.WithPollingTimeout(10*time.Second)),
		chromedp.Evaluate(scrollCommentJS, nil),
		chromedp.Sleep(300*time.Millisecond),
	)
	_ = rec.captureComposite(bctx, tctx, 240) // the agent's reply in the thread

	// Reviewer replies back inline.
	_ = chromedp.Run(bctx,
		clickJS(`.inline-comment button[name="openReply"]`),
		chromedp.WaitVisible(`.reply-form textarea`, chromedp.ByQuery),
		chromedp.Evaluate(scrollCommentJS, nil),
		chromedp.Sleep(150*time.Millisecond),
	)
	_ = rec.captureComposite(bctx, tctx, 90) // reply form open
	_ = chromedp.Run(bctx, chromedp.SendKeys(`.reply-form textarea`, "Add a ctx deadline too — cap at 5s total.", chromedp.ByQuery), chromedp.Sleep(150*time.Millisecond))
	_ = rec.captureComposite(bctx, tctx, 150) // typed
	_ = chromedp.Run(bctx,
		clickJS(`.reply-form button[name="postReply"]`),
		chromedp.Sleep(700*time.Millisecond),
		chromedp.Evaluate(scrollCommentJS, nil),
		chromedp.Sleep(200*time.Millisecond),
	)
	follow := append(replied[:len(replied):len(replied)],
		termLine{"", "sp"},
		termLine{"Read your reply — capping total elapsed at 5s", "bullet"},
	)
	_ = termRender(tctx, follow)
	_ = rec.captureComposite(bctx, tctx, 320) // the two-way thread, held longest

	if err := rec.encode(filepath.Join(outDir, "thread.gif")); err != nil {
		log.Printf("[gif:thread] encode: %v", err)
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

	// Terminal pane: launched, idle, waiting for the snapshot. Mirrors the real
	// skill flow — /prereview prints a review URL the user taps to open.
	idle := []termLine{
		{"$ claude", "dim"},
		{"> /prereview", "prompt"},
		{"● Review session live — open in your browser:", "bullet"},
		{"  http://127.0.0.1:8420  (tap to open →)", "link"},
		{"", "sp"},
		{"watching the queue…", "dim"},
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

	// Save → the comment becomes an inline card and, in agent mode, is
	// automatically streamed to the agent's queue (no explicit hand-off click).
	_ = chromedp.Run(bctx, chromedp.Click(saveBtn, chromedp.ByQuery), chromedp.Sleep(600*time.Millisecond))
	_ = rec.captureComposite(bctx, tctx, 110)
	_ = rec.captureComposite(bctx, tctx, 150) // terminal still idle (event arriving)

	// Terminal: Claude receives the snapshot and reads the comment. Keep the
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
	_ = rec.captureComposite(bctx, tctx, 220) // loop closed: comment resolved

	// --- The other direction (issue #98): the agent PROPOSES a follow-up edit and
	// the human accepts it inline. Seed a suggestion on the now-fixed payment.go
	// (line 8, the doc comment); the running server surfaces it on the next load. ---
	_ = appendSuggestion(repo, `{"id":"hero-sg","file":"payment.go","from_line":8,"to_line":8,"original":"// Charge captures a payment for an order. Amount is in minor units (cents).","proposed":"// Charge captures a payment for an order, retrying transient gateway errors. Amount is in minor units (cents).","note":"note the retry behaviour"}`+"\n")
	propose := append(done[:len(done):len(done)],
		termLine{"", "sp"},
		termLine{"● Suggested an edit — payment.go:8", "bullet"},
		termLine{"  note the retry behaviour in the doc comment", "dim"},
	)
	_ = termRender(tctx, propose)
	_ = chromedp.Run(bctx,
		chromedp.Navigate("about:blank"),
		chromedp.Sleep(150*time.Millisecond),
		chromedp.Navigate(navURL(url, "payment.go")),
		chromedp.WaitVisible(`.inline-suggestion`, chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelector('.inline-suggestion')?.scrollIntoView({block:'center'})`, nil),
		chromedp.Sleep(300*time.Millisecond),
	)
	_ = rec.captureComposite(bctx, tctx, 220) // the before→after suggestion box, agent waiting

	_ = chromedp.Run(bctx,
		chromedp.Click(`.inline-suggestion button[name="acceptSuggestion"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.inline-suggestion.is-decided.sg-accept`, chromedp.ByQuery),
		chromedp.Sleep(300*time.Millisecond),
		chromedp.Evaluate(`document.querySelector('.inline-suggestion')?.scrollIntoView({block:'center'})`, nil),
		chromedp.Sleep(150*time.Millisecond),
	)
	_ = rec.captureComposite(bctx, tctx, 300) // accepted — the reverse loop closed, held longest

	if err := rec.encode(filepath.Join(outDir, "hero.gif")); err != nil {
		log.Printf("[gif:hero] encode: %v", err)
	}
}

// appendSuggestion writes one suggestion JSON line to the demo repo's
// .prereview/suggestions.jsonl so the hero flow can show the agent proposing an
// edit that the human then accepts (issue #98). The running server surfaces it on
// the next navigation (Mount re-reads the file).
func appendSuggestion(repo, line string) error {
	dir := filepath.Join(repo, ".prereview")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, "suggestions.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(line)
	return err
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

// gifSuggestion — the REVERSE loop (issue #98): the agent proposes an edit
// (terminal, `prereview suggest`), it renders inline as a before→after box
// (browser), and the human accepts it (browser). Composite (browser over
// terminal), like gifHero, to show both sides of the round-trip. The suggestion
// is seeded by capture-gifs.sh before this flow runs (a suggest subcommand call),
// so the box is already present when the browser opens guide.md.
func gifSuggestion(allocCtx context.Context, url, outDir string) {
	bctx, bcancel := chromedp.NewContext(allocCtx)
	defer bcancel()
	bctx, bt := context.WithTimeout(bctx, 90*time.Second)
	defer bt()
	tctx, tcancel := chromedp.NewContext(allocCtx)
	defer tcancel()
	tctx, tt := context.WithTimeout(tctx, 90*time.Second)
	defer tt()

	rec := &gifRec{}

	// Terminal: the agent was asked to review the doc and proposed one edit.
	proposed := []termLine{
		{"$ claude", "dim"},
		{"> review guide.md and suggest edits in prereview", "prompt"},
		{"● Submitted 1 suggestion — guide.md:12", "bullet"},
		{`-   Transient gateway errors are retried with backoff.`, "del"},
		{`+   …retried with exponential backoff, up to maxRetries attempts.`, "add"},
		{"", "sp"},
		{"waiting for your decision…", "dim"},
	}
	if err := chromedp.Run(tctx, chromedp.EmulateViewport(termW, termH)); err != nil {
		log.Printf("[gif:suggestion] term viewport: %v", err)
		return
	}
	if err := termInit(tctx); err != nil {
		log.Printf("[gif:suggestion] term init: %v", err)
		return
	}
	if err := termRender(tctx, proposed); err != nil {
		log.Printf("[gif:suggestion] term render: %v", err)
		return
	}

	// Browser: open guide.md (rendered), where the suggestion box renders inline.
	if err := chromedp.Run(bctx, base(url, "guide.md", 1280, 800)...); err != nil {
		log.Printf("[gif:suggestion] open guide.md: %v", err)
		return
	}
	if err := chromedp.Run(bctx,
		chromedp.WaitVisible(`.inline-suggestion`, chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelector('.inline-suggestion')?.scrollIntoView({block:'center'})`, nil),
		chromedp.Sleep(300*time.Millisecond),
	); err != nil {
		log.Printf("[gif:suggestion] await box: %v", err)
		return
	}
	_ = rec.captureComposite(bctx, tctx, 220) // the before→after box, agent waiting
	_ = rec.captureComposite(bctx, tctx, 90)  // hold on the decision row

	// Human clicks Accept → the verdict badge appears. Fire the click via JS
	// (a direct form submit, not a coordinate click) and wait for the decided
	// card to EXIST — not to be visible: on the rendered-Markdown view the accept
	// re-render shifts the box out of the viewport, so a WaitVisible would block
	// until the context deadline and drop this frame. Poll for existence, then
	// scroll it back into view for the capture.
	_ = chromedp.Run(bctx,
		clickJS(`.inline-suggestion button[name="acceptSuggestion"]`),
		chromedp.Poll(`!!document.querySelector('.inline-suggestion.is-decided.sg-accept')`, nil, chromedp.WithPollingTimeout(10*time.Second)),
		chromedp.Sleep(300*time.Millisecond),
		chromedp.Evaluate(`document.querySelector('.inline-suggestion')?.scrollIntoView({block:'center'})`, nil),
		chromedp.Sleep(200*time.Millisecond),
	)

	// Terminal: the accept arrives in the next queue snapshot; the agent applies
	// it and acks with `prereview applied`.
	accepted := append(proposed[:5:5],
		termLine{"", "sp"},
		termLine{"✓ Accepted — applying the edit now", "bullet"},
	)
	_ = termRender(tctx, accepted)
	_ = rec.captureComposite(bctx, tctx, 260) // accepted, held longest

	if err := rec.encode(filepath.Join(outDir, "suggestion.gif")); err != nil {
		log.Printf("[gif:suggestion] encode: %v", err)
	}
}

// gifSearch — ⌘K search across files (issue #91): open the palette, type a
// query, jump to a hit. Browser-only.
func gifSearch(allocCtx context.Context, url, outDir string) {
	withCtx(allocCtx, "gif:search", func(ctx context.Context) error {
		rec := &gifRec{}
		if err := chromedp.Run(ctx, base(url, "payment.go", 1280, 800)...); err != nil {
			return err
		}
		_ = rec.capture(ctx, 120) // the diff, before search
		// Open the palette via the toolbar button (reliable in headless; the
		// ⌘K chord is skip-when-typing and flaky to synthesize).
		if err := chromedp.Run(ctx,
			chromedp.Click(`button[name="openSearch"]`, chromedp.ByQuery),
			chromedp.WaitVisible(`.search-modal input[name="q"]`, chromedp.ByQuery),
			chromedp.Sleep(200*time.Millisecond),
		); err != nil {
			return err
		}
		_ = rec.capture(ctx, 90) // empty palette
		// Type a query that matches across files → results stream in.
		_ = chromedp.Run(ctx,
			chromedp.SendKeys(`.search-modal input[name="q"]`, "gateway", chromedp.ByQuery),
			chromedp.WaitVisible(`.search-hit`, chromedp.ByQuery),
			chromedp.Sleep(400*time.Millisecond),
		)
		_ = rec.capture(ctx, 200) // results with highlighted matches
		// Jump to the first hit → the file opens at the matched line.
		_ = chromedp.Run(ctx,
			chromedp.Click(`.search-hit`, chromedp.ByQuery),
			chromedp.Sleep(700*time.Millisecond),
			chromedp.Evaluate(`document.querySelector('.line.is-cursor')?.scrollIntoView({block:'center'})`, nil),
			chromedp.Sleep(200*time.Millisecond),
		)
		_ = rec.capture(ctx, 220) // landed on the match
		return rec.encode(filepath.Join(outDir, "search.gif"))
	})
}

// gifThemes — cycle the colour schemes (Solarized → Gruvbox → Catppuccin) and
// flip the mode, showing the whole UI + syntax recolour live. Browser-only.
func gifThemes(allocCtx context.Context, url, outDir string) {
	withCtx(allocCtx, "gif:themes", func(ctx context.Context) error {
		rec := &gifRec{}
		if err := chromedp.Run(ctx, base(url, "payment.go", 1280, 800)...); err != nil {
			return err
		}
		_ = chromedp.Run(ctx, chromedp.WaitVisible(`.code`, chromedp.ByQuery), chromedp.Sleep(200*time.Millisecond))
		_ = rec.capture(ctx, 180) // Solarized (default)
		// The scheme/mode buttons live in the desktop "View ▾" dropdown, so a
		// chromedp.Click would block on visibility — clickJS fires the DOM click
		// directly. But the click bubbles to the dropdown's toggleClass and pops the
		// menu open; close it after each click so every frame shows the clean
		// full-UI recolour (the whole UI + syntax recolour regardless of the menu).
		const closeMenu = `document.querySelectorAll('.tb-dropdown.open').forEach(d=>d.classList.remove('open'))`
		// Cycle the scheme twice: Gruvbox, then Catppuccin.
		for range 2 {
			_ = chromedp.Run(ctx, clickJS(`button[name="cycleScheme"]`), chromedp.Sleep(500*time.Millisecond),
				chromedp.Evaluate(closeMenu, nil), chromedp.Sleep(150*time.Millisecond))
			_ = rec.capture(ctx, 180)
		}
		// Flip the mode to Dark on the current scheme. The default is System and the
		// cycle is System → Light → Dark, so it takes TWO clicks to land on Dark.
		_ = chromedp.Run(ctx,
			clickJS(`button[name="cycleTheme"]`), chromedp.Sleep(300*time.Millisecond),
			clickJS(`button[name="cycleTheme"]`), chromedp.Sleep(500*time.Millisecond),
			chromedp.Evaluate(closeMenu, nil), chromedp.Sleep(150*time.Millisecond))
		_ = rec.capture(ctx, 240) // dark mode, held longest
		return rec.encode(filepath.Join(outDir, "themes.gif"))
	})
}
