package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestMain points every per-user folder at a temp dir unless a test sets its
// own: subcommands such as reply, suggest and quiz read your checks, library and
// prefs, and a test must never read (or write) the real ones.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "prereview-test-home-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for env, rel := range map[string]string{
		// The checks, prompts, quizzes and shortcuts folders default under the library.
		"PREREVIEW_LIBRARY_DIR":   "library",
		"PREREVIEW_UI_PREFS_PATH": "ui-prefs.json",
	} {
		if os.Getenv(env) == "" {
			os.Setenv(env, filepath.Join(dir, rel))
		}
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
