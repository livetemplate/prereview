package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"

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
	failAt(2, err)
	return err
}

// check records err, if any, at the caller's file:line. For the frame grabs
// and terminal renders whose errors the flows don't otherwise look at.
func check(err error) {
	failAt(2, err)
}

func failAt(skip int, err error) {
	if err != nil {
		_, file, line, _ := runtime.Caller(skip)
		fail("%s:%d: %v", filepath.Base(file), line, err)
	}
}

// exitOnFailure ends the run with status 1 when any step failed.
func exitOnFailure() {
	if len(failures) > 0 {
		log.Printf("%d capture step(s) failed", len(failures))
		os.Exit(1)
	}
}
