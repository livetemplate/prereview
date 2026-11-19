package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/chromedp/chromedp"
)

// failures holds every capture step that errored. A capture keeps going, so
// one run names every broken step, and then exits 1: `make gifs` and `make
// screenshots` can't pass on a flow that dropped a frame.
var failures []string

// fail records a failed capture step.
func fail(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	log.Print("FAIL " + msg)
	failures = append(failures, msg)
}

// step runs actions and records a failure at the caller's file:line.
func step(ctx context.Context, actions ...chromedp.Action) error {
	err := chromedp.Run(ctx, actions...)
	if err != nil {
		_, file, line, _ := runtime.Caller(1)
		fail("%s:%d: %v", filepath.Base(file), line, err)
	}
	return err
}

// optional runs actions whose failure is expected and harmless (closing a menu
// that may not be open), so it is not counted.
func optional(ctx context.Context, actions ...chromedp.Action) {
	_ = chromedp.Run(ctx, actions...)
}

// exitOnFailure ends the run with status 1 when any step failed.
func exitOnFailure() {
	if len(failures) > 0 {
		log.Printf("%d capture step(s) failed", len(failures))
		os.Exit(1)
	}
}

// prepareReview answers the first-launch card with Not now if the review shows
// it. The capture's library is fresh, so a later server finds an earlier one's
// review and offers to learn from it; the answer lands in the shared library,
// so every later server skips the card. The scan runs after load, so this waits
// a few seconds for the card before deciding there is none.
func prepareReview(allocCtx context.Context, url string) {
	withCtx(allocCtx, "prepare", func(ctx context.Context) error {
		if step(ctx,
			chromedp.EmulateViewport(1200, 800),
			chromedp.Navigate(url),
			chromedp.WaitVisible(`body`, chromedp.ByQuery),
		) != nil {
			return nil
		}
		var shown bool
		optional(ctx, chromedp.Poll(`!!document.querySelector('button[name="dismissPastReviews"]')`, &shown,
			chromedp.WithPollingTimeout(5*time.Second), chromedp.WithPollingInterval(200*time.Millisecond)))
		if shown {
			_ = step(ctx,
				chromedp.Click(`button[name="dismissPastReviews"]`, chromedp.ByQuery),
				chromedp.WaitNotPresent(`button[name="dismissPastReviews"]`, chromedp.ByQuery),
			)
		}
		return nil
	})
}
