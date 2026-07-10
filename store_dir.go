package main

import (
	"fmt"
	"path/filepath"
)

// storeDir resolves the .prereview directory for a review rooted at out — the
// REPO path prereview prints at launch (the same directory the review server
// watches). An empty out means the current directory.
//
// It is pure path resolution with NO side effects: it never creates the
// directory. Write subcommands (processed, suggest) call os.MkdirAll on the
// result themselves; read subcommands (events, comments) tolerate its absence.
// Centralising it here keeps every subcommand agreeing on one location —
// mirroring the review.ProcessedPath/SuggestionPath helpers on the server side.
func storeDir(out string) (string, error) {
	root := out
	if root == "" {
		root = "."
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve dir: %w", err)
	}
	return filepath.Join(abs, ".prereview"), nil
}
