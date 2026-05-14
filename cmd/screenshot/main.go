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

	"github.com/chromedp/chromedp"
)

func main() {
	url := flag.String("url", "http://127.0.0.1:8765", "running prereview URL")
	outDir := flag.String("out", "/tmp/prereview-shots", "directory to write PNGs")
	debug := flag.Bool("debug", false, "also dump hamburger HTML + computed style to stdout")
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
