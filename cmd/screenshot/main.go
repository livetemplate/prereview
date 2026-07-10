// Screenshot dev helper. Connects to a running prereview server and captures
// PNGs at iPhone and laptop viewports so visual bugs can be checked without
// asking the user to refresh and screenshot.
//
// Usage:
//
//	go run ./cmd/screenshot --url http://127.0.0.1:8765 --out /tmp/prereview
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/chromedp"
)

func main() {
	url := flag.String("url", "http://127.0.0.1:8765", "running prereview URL")
	outDir := flag.String("out", "/tmp/prereview-shots", "directory to write PNGs")
	debug := flag.Bool("debug", false, "also dump hamburger HTML + computed style to stdout")
	readme := flag.Bool("readme", false, "capture the curated README screenshot set (needs a demo-repo server; see `make screenshots`)")
	gifFlow := flag.String("gif", "", "capture a single animated GIF flow (hero); needs a demo-repo server")
	repo := flag.String("repo", "", "demo repo working tree (the hero flow edits payment.go here to show Claude's fix)")
	flag.Parse()

	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		log.Fatalf("mkdir: %v", err)
	}

	chromium := findChromium()
	if chromium == "" {
		log.Fatal("no chromium binary found")
	}

	allocOpts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(chromium),
		chromedp.Flag("headless", true),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("no-sandbox", true),
	)
	allocCtx, allocCancel := chromedp.NewExecAllocator(context.Background(), allocOpts...)
	defer allocCancel()

	if *readme {
		runReadmeShots(allocCtx, *url, *outDir)
		return
	}

	if *gifFlow != "" {
		runGif(allocCtx, *url, *gifFlow, *repo, *outDir)
		return
	}

	ctx, cancel := chromedp.NewContext(allocCtx)
	defer cancel()

	ctx, tCancel := context.WithTimeout(ctx, 60*time.Second)
	defer tCancel()

	if *debug {
		var dump, dump2 string
		_ = dump2
		if err := chromedp.Run(ctx,
			chromedp.EmulateViewport(375, 812),
			chromedp.Navigate(*url),
			chromedp.WaitVisible(`.hamburger`, chromedp.ByQuery),
			chromedp.Sleep(300*time.Millisecond),
			chromedp.Click(`.hamburger`, chromedp.ByQuery),
			chromedp.Sleep(500*time.Millisecond),
			chromedp.Click(`//button[@name='selectFile' and contains(., 'prereview.tmpl')]`, chromedp.BySearch),
			chromedp.Sleep(700*time.Millisecond),
			chromedp.Evaluate(`(() => {
				const ic = document.querySelector('.inline-comment');
				if (ic) ic.scrollIntoView({block:'center'});
			})()`, nil),
			chromedp.Sleep(300*time.Millisecond),
			chromedp.Evaluate(`(() => {
				const ic = document.querySelector('.inline-comment');
				const actions = ic ? ic.querySelector('.ic-actions') : null;
				return JSON.stringify({
					found_inline_comment: !!ic,
					ic_outer: ic ? ic.outerHTML : null,
					actions_html: actions ? actions.innerHTML.slice(0, 500) : null,
				}, null, 2);
			})()`, &dump),
		); err != nil {
			log.Fatalf("debug fail: %v", err)
		}
		fmt.Println("=== X CLOSE ===")
		fmt.Println(dump)
		fmt.Println("=== BACKDROP CLICK ===")
		fmt.Println(dump2)
		return
	}

	shots := []struct {
		name  string
		w, h  int
		setup []chromedp.Action
	}{
		{"iphone-closed", 375, 812, nil},
		{"iphone-overflow-menu-closed", 375, 812, nil},
		{"iphone-overflow-menu-open", 375, 812, []chromedp.Action{
			chromedp.Click(`.more-trigger`, chromedp.ByQuery),
			chromedp.Sleep(300 * time.Millisecond),
		}},
		{"iphone-overflow-menu-with-file", 375, 812, []chromedp.Action{
			chromedp.Click(`.hamburger`, chromedp.ByQuery),
			chromedp.Sleep(300 * time.Millisecond),
			chromedp.Click(`(//button[@name='selectFile'])[1]`, chromedp.BySearch),
			chromedp.Sleep(500 * time.Millisecond),
			chromedp.Click(`.more-trigger`, chromedp.ByQuery),
			chromedp.Sleep(300 * time.Millisecond),
		}},
		{"iphone-file-view-on", 375, 812, []chromedp.Action{
			chromedp.Click(`.hamburger`, chromedp.ByQuery),
			chromedp.Sleep(300 * time.Millisecond),
			chromedp.Click(`(//button[@name='selectFile'])[1]`, chromedp.BySearch),
			chromedp.Sleep(500 * time.Millisecond),
			chromedp.Click(`.more-trigger`, chromedp.ByQuery),
			chromedp.Sleep(300 * time.Millisecond),
			chromedp.Click(`.more-menu button[name='toggleFileView']`, chromedp.ByQuery),
			chromedp.Sleep(500 * time.Millisecond),
		}},
		{"iphone-drawer-open", 375, 812, []chromedp.Action{
			chromedp.Click(`.hamburger`, chromedp.ByQuery),
			chromedp.Sleep(400 * time.Millisecond),
		}},
		{"iphone-composer-typing", 375, 812, []chromedp.Action{
			chromedp.Click(`.hamburger`, chromedp.ByQuery),
			chromedp.Sleep(400 * time.Millisecond),
			chromedp.Click(`(//button[@name='selectFile'])[5]`, chromedp.BySearch),
			chromedp.Sleep(500 * time.Millisecond),
			// pick a line so the composer appears
			chromedp.Evaluate(`(() => {
				const b = document.querySelector('.code button.line');
				if (b) b.click();
			})()`, nil),
			chromedp.Sleep(400 * time.Millisecond),
			chromedp.SendKeys(`.composer textarea`, "test comment", chromedp.ByQuery),
			chromedp.Sleep(200 * time.Millisecond),
			// scroll composer into view
			chromedp.Evaluate(`document.querySelector('.composer')?.scrollIntoView({block:'center'})`, nil),
			chromedp.Sleep(200 * time.Millisecond),
		}},
		{"iphone-drawer-with-selection", 375, 812, []chromedp.Action{
			chromedp.Click(`.hamburger`, chromedp.ByQuery),
			chromedp.Sleep(400 * time.Millisecond),
			// pick csv/writer.go (index 5) so it auto-closes
			chromedp.Click(`(//button[@name='selectFile'])[5]`, chromedp.BySearch),
			chromedp.Sleep(700 * time.Millisecond),
			// reopen drawer — now csv/writer.go should show the selected highlight
			chromedp.Click(`.hamburger`, chromedp.ByQuery),
			chromedp.Sleep(700 * time.Millisecond),
			chromedp.Evaluate(`document.querySelector('#files-drawer').scrollTop = 0`, nil),
			chromedp.Sleep(200 * time.Millisecond),
		}},
		{"iphone-after-file-pick", 375, 812, []chromedp.Action{
			chromedp.Click(`.hamburger`, chromedp.ByQuery),
			chromedp.Sleep(200 * time.Millisecond),
			chromedp.WaitVisible(`#files-drawer button.file-btn`, chromedp.ByQuery),
			chromedp.Click(`(//button[@name='selectFile'])[7]`, chromedp.BySearch),
			chromedp.Sleep(400 * time.Millisecond),
		}},
		{"iphone-sticky-gutter", 375, 812, []chromedp.Action{
			chromedp.Click(`.hamburger`, chromedp.ByQuery),
			chromedp.Sleep(200 * time.Millisecond),
			chromedp.Click(`(//button[@name='selectFile'])[7]`, chromedp.BySearch),
			chromedp.Sleep(400 * time.Millisecond),
			// Scroll the .code container horizontally by 200px so the gutter
			// must stay sticky on the left.
			chromedp.Evaluate(`document.querySelector('.code').scrollLeft = 200`, nil),
			chromedp.Sleep(300 * time.Millisecond),
		}},
		{"iphone-resumed-comment", 375, 812, []chromedp.Action{
			chromedp.Click(`.hamburger`, chromedp.ByQuery),
			chromedp.Sleep(200 * time.Millisecond),
			// csv/writer.go is at index 5
			chromedp.Click(`(//button[@name='selectFile'])[5]`, chromedp.BySearch),
			chromedp.Sleep(400 * time.Millisecond),
			// scroll to whatever inline comment exists
			chromedp.Evaluate(`(() => { const c = document.querySelectorAll('.inline-comment'); if (c.length) c[0].scrollIntoView({block:'center'}); })()`, nil),
			chromedp.Sleep(200 * time.Millisecond),
		}},
		{"laptop", 1280, 800, nil},
		{"laptop-file-selected", 1280, 800, []chromedp.Action{
			chromedp.Click(`(//button[@name='selectFile'])[1]`, chromedp.BySearch),
			chromedp.Sleep(500 * time.Millisecond),
		}},
		{"laptop-unchanged-file", 1280, 800, []chromedp.Action{
			// Pick a file without a diff status (no [M]/[A] badge). The
			// drawer is sorted alphabetically, so we filter to "history" — a
			// known-unchanged file in the prereview repo's working tree.
			chromedp.SendKeys(`#files-drawer input[name='filter']`, "history", chromedp.ByQuery),
			chromedp.Sleep(400 * time.Millisecond),
			chromedp.Click(`(//button[@name='selectFile'])[1]`, chromedp.BySearch),
			chromedp.Sleep(500 * time.Millisecond),
		}},
		{"laptop-file-view-on", 1280, 800, []chromedp.Action{
			chromedp.Click(`(//button[@name='selectFile'])[1]`, chromedp.BySearch),
			chromedp.Sleep(400 * time.Millisecond),
			chromedp.Click(`.toolbar-inline button[name='toggleFileView']`, chromedp.ByQuery),
			chromedp.Sleep(400 * time.Millisecond),
		}},
		{"laptop-composer-typing", 1280, 800, []chromedp.Action{
			chromedp.Click(`(//button[@name='selectFile'])[1]`, chromedp.BySearch),
			chromedp.Sleep(400 * time.Millisecond),
			chromedp.Evaluate(`(() => {
				const b = document.querySelector('.code button.line');
				if (b) b.click();
			})()`, nil),
			chromedp.Sleep(300 * time.Millisecond),
			chromedp.SendKeys(`.composer textarea`, "this could be simpler", chromedp.ByQuery),
			chromedp.Sleep(200 * time.Millisecond),
		}},
		{"laptop-base-picker-error", 1280, 800, []chromedp.Action{
			chromedp.Click(`.base-custom summary`, chromedp.ByQuery),
			chromedp.SendKeys(`#base-custom-input`, "definitely-not-a-ref", chromedp.ByQuery),
			chromedp.Click(`.base-custom button[name='setBase']`, chromedp.ByQuery),
			chromedp.Sleep(400 * time.Millisecond),
		}},
		{"laptop-base-picker-head1", 1280, 800, []chromedp.Action{
			chromedp.Evaluate(`(() => {
				const s = document.querySelector('#base-input');
				s.value = "HEAD~1";
				s.dispatchEvent(new Event("change", {bubbles: true}));
			})()`, nil),
			chromedp.Sleep(500 * time.Millisecond),
		}},
		{"laptop-all-comments-view", 1280, 800, []chromedp.Action{
			// Open the inline comments-pill (only renders when there are
			// existing comments — the repo's prereview.tmpl has a real one).
			chromedp.Sleep(400 * time.Millisecond),
			chromedp.Evaluate(`(() => {
				const b = document.querySelector('.toolbar-inline button[name="toggleCommentList"]');
				if (b) b.click();
			})()`, nil),
			chromedp.Sleep(500 * time.Millisecond),
		}},
		// NOTE: mutating scenarios (clicking Done, adding comments, opening
		// delete dialog) are intentionally omitted — they overwrite the live
		// repo's .prereview/comments.csv. Enable them only against a throwaway
		// test repo.
	}

	for _, s := range shots {
		// Fresh chromedp context per scenario — prevents stale CSS or DOM
		// state from one scenario bleeding into the next.
		shotCtx, shotCancel := chromedp.NewContext(allocCtx)
		shotCtx, shotTCancel := context.WithTimeout(shotCtx, 30*time.Second)

		actions := []chromedp.Action{
			chromedp.EmulateViewport(int64(s.w), int64(s.h)),
			chromedp.Navigate(*url),
			chromedp.WaitVisible(`#files-drawer`, chromedp.ByQuery),
			chromedp.Sleep(300 * time.Millisecond),
		}
		actions = append(actions, s.setup...)

		var buf []byte
		actions = append(actions, chromedp.CaptureScreenshot(&buf))

		if err := chromedp.Run(shotCtx, actions...); err != nil {
			log.Printf("[%s] failed: %v", s.name, err)
			shotTCancel()
			shotCancel()
			continue
		}
		path := filepath.Join(*outDir, s.name+".png")
		if err := os.WriteFile(path, buf, 0o644); err != nil {
			log.Printf("[%s] write: %v", s.name, err)
		} else {
			fmt.Printf("wrote %s (%d bytes)\n", path, len(buf))
		}
		shotTCancel()
		shotCancel()
	}
}

// ===== README screenshot set =================================================
//
// Captures the curated, captioned shots embedded in README.md against a demo
// repo (see cmd/screenshot/demo-repo.sh + `make screenshots`). Comments are
// created through the real UI — line, file, and image-area — so anchors are
// authentic and nothing shows as "outdated". prereview's deep-link hash
// (`#path`, `#path:Ln-Lm`) drives file/line navigation; clicks/drags handle the
// rest. Run the server in --agent mode for the hero flow.

const saveBtn = `button.save-btn[name="addComment"]`

func runReadmeShots(allocCtx context.Context, url, outDir string) {
	// 1) Seed comments that later shots read back from the CSV. Order matters:
	//    all-comments / file / image shots depend on these existing.
	seedLineComment(allocCtx, url, "payment.go:L13-L16",
		"Retry loop has no backoff or ctx — a failing gateway gets hammered.")
	seedFileComment(allocCtx, url, "payment.go",
		"Add a package doc comment summarizing the charge → gateway → refund flow.")
	seedAreaComment(allocCtx, url, "architecture.png",
		"This box overlaps the gateway lane — tighten the spacing.")

	// 2) Capture. review-mobile shows the live composing flow (unsaved draft);
	//    the rest read the seeded comments. The desktop hero, image-area, and
	//    markdown-block flows are now animated GIFs (see `make gifs`), so they
	//    are intentionally NOT captured here as stills.
	shots := []struct {
		name  string
		w, h  int
		frag  string
		setup []chromedp.Action
	}{
		{"file-comment", 1280, 800, "payment.go", nil},
		{"all-comments", 1280, 800, "", []chromedp.Action{
			clickJS(`button[name="toggleCommentList"]`),
			chromedp.Sleep(600 * time.Millisecond),
		}},
		{"review-mobile", 390, 844, "payment.go:L18", []chromedp.Action{
			typeComposer("Surface the gateway's real error here, not a generic string."),
		}},
	}
	for _, s := range shots {
		captureShot(allocCtx, url, s.name, s.w, s.h, s.frag, s.setup, outDir)
	}
}

func navURL(url, frag string) string {
	if frag == "" {
		return url
	}
	return url + "#" + frag
}

// base navigates to url[#frag] at the given viewport and waits for the
// livetemplate client to connect over WebSocket and apply the hash.
func base(url, frag string, w, h int) []chromedp.Action {
	return []chromedp.Action{
		chromedp.EmulateViewport(int64(w), int64(h)),
		chromedp.Navigate(navURL(url, frag)),
		chromedp.WaitVisible(`#files-drawer`, chromedp.ByQuery),
		chromedp.Sleep(1000 * time.Millisecond),
	}
}

func withCtx(allocCtx context.Context, label string, fn func(context.Context) error) {
	ctx, cancel := chromedp.NewContext(allocCtx)
	defer cancel()
	ctx, tcancel := context.WithTimeout(ctx, 45*time.Second)
	defer tcancel()
	if err := fn(ctx); err != nil {
		log.Printf("[%s] %v", label, err)
	}
}

func seedLineComment(allocCtx context.Context, url, frag, body string) {
	withCtx(allocCtx, "seed-line:"+frag, func(ctx context.Context) error {
		acts := base(url, frag, 1280, 800)
		acts = append(acts,
			chromedp.WaitVisible(`.composer textarea`, chromedp.ByQuery),
			chromedp.SendKeys(`.composer textarea`, body, chromedp.ByQuery),
			chromedp.Sleep(150*time.Millisecond),
			chromedp.Click(saveBtn, chromedp.ByQuery),
			chromedp.Sleep(600*time.Millisecond),
		)
		return chromedp.Run(ctx, acts...)
	})
}

func seedFileComment(allocCtx context.Context, url, frag, body string) {
	withCtx(allocCtx, "seed-file:"+frag, func(ctx context.Context) error {
		acts := base(url, frag, 1280, 800)
		acts = append(acts,
			chromedp.Click(`button[name="openFileComment"]`, chromedp.ByQuery),
			chromedp.WaitVisible(`.composer textarea`, chromedp.ByQuery),
			chromedp.SendKeys(`.composer textarea`, body, chromedp.ByQuery),
			chromedp.Sleep(150*time.Millisecond),
			chromedp.Click(saveBtn, chromedp.ByQuery),
			chromedp.Sleep(600*time.Millisecond),
		)
		return chromedp.Run(ctx, acts...)
	})
}

func seedAreaComment(allocCtx context.Context, url, frag, body string) {
	withCtx(allocCtx, "seed-area:"+frag, func(ctx context.Context) error {
		imgSel := `img[src*="` + frag + `"]`
		var r struct{ X, Y, W, H float64 }
		acts := base(url, frag, 1280, 800)
		acts = append(acts,
			chromedp.WaitVisible(imgSel, chromedp.ByQuery),
			chromedp.Sleep(300*time.Millisecond),
			// #57: image area-select is gated behind the region toggle (images
			// pinch-zoom by default), so arm it before the drag can capture.
			chromedp.Click(`button[name="toggleRegionSelect"]`, chromedp.ByQuery),
			chromedp.WaitVisible(`.image-with-areas.is-armed`, chromedp.ByQuery),
			chromedp.Evaluate(`(()=>{const im=document.querySelector('`+imgSel+`');const b=im.getBoundingClientRect();return {X:b.x,Y:b.y,W:b.width,H:b.height};})()`, &r),
			chromedp.ActionFunc(func(ctx context.Context) error {
				return mouseDrag(ctx, r.X+r.W*0.15, r.Y+r.H*0.15, r.X+r.W*0.62, r.Y+r.H*0.58)
			}),
			chromedp.WaitVisible(`.composer textarea`, chromedp.ByQuery),
			chromedp.SendKeys(`.composer textarea`, body, chromedp.ByQuery),
			chromedp.Sleep(150*time.Millisecond),
			chromedp.Click(saveBtn, chromedp.ByQuery),
			chromedp.Sleep(600*time.Millisecond),
		)
		return chromedp.Run(ctx, acts...)
	})
}

// mouseDrag presses, glides, and releases the left button — the gesture the
// image area-select directive listens for.
func mouseDrag(ctx context.Context, x1, y1, x2, y2 float64) error {
	if err := input.DispatchMouseEvent(input.MousePressed, x1, y1).
		WithButton(input.Left).WithClickCount(1).Do(ctx); err != nil {
		return err
	}
	const steps = 10
	for i := 1; i <= steps; i++ {
		x := x1 + (x2-x1)*float64(i)/steps
		y := y1 + (y2-y1)*float64(i)/steps
		if err := input.DispatchMouseEvent(input.MouseMoved, x, y).
			WithButton(input.Left).Do(ctx); err != nil {
			return err
		}
		time.Sleep(25 * time.Millisecond)
	}
	return input.DispatchMouseEvent(input.MouseReleased, x2, y2).
		WithButton(input.Left).WithClickCount(1).Do(ctx)
}

func typeComposer(body string) chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		return chromedp.Run(ctx,
			chromedp.WaitVisible(`.composer textarea`, chromedp.ByQuery),
			chromedp.SendKeys(`.composer textarea`, body, chromedp.ByQuery),
			chromedp.Sleep(250*time.Millisecond),
			chromedp.Evaluate(`document.querySelector('.composer')?.scrollIntoView({block:'center'})`, nil),
			chromedp.Sleep(150*time.Millisecond),
		)
	})
}

func clickJS(sel string) chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		return chromedp.Run(ctx, chromedp.Evaluate(
			fmt.Sprintf(`(()=>{const b=document.querySelector(%q);if(b)b.click();})()`, sel), nil))
	})
}

func captureShot(allocCtx context.Context, url, name string, w, h int, frag string, setup []chromedp.Action, outDir string) {
	withCtx(allocCtx, "shot:"+name, func(ctx context.Context) error {
		acts := base(url, frag, w, h)
		acts = append(acts, setup...)
		var buf []byte
		acts = append(acts, chromedp.CaptureScreenshot(&buf))
		if err := chromedp.Run(ctx, acts...); err != nil {
			return err
		}
		path := filepath.Join(outDir, name+".png")
		if err := os.WriteFile(path, buf, 0o644); err != nil {
			return err
		}
		fmt.Printf("wrote %s (%d bytes)\n", path, len(buf))
		return nil
	})
}

func findChromium() string {
	for _, c := range []string{
		"/run/current-system/sw/bin/chromium",
		"/usr/bin/chromium",
		"/usr/bin/chromium-browser",
		"/usr/bin/google-chrome",
	} {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	if path, err := exec.LookPath("chromium"); err == nil {
		return path
	}
	return ""
}
