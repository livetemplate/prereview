package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/livetemplate/prereview/internal/review"
)

// runStatus implements `prereview status <working|done> [message] [--out <dir>]`:
// the coding agent echoes what it's doing so the live review UI shows a status
// pill across every open tab — `working` while applying a batch, `done` when
// finished. It writes <REPO>/.prereview/llm-status.json ATOMICALLY (temp file +
// rename) so the server's watcher never reads a half-written file. This is the
// CLI owning the write the agent used to hand-roll as a shell helper.
func runStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	out := fs.String("out", "", "directory whose .prereview/ holds the review (the REPO printed at launch); defaults to the current directory")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(),
			"Usage: prereview status <working|done> [message] [--out <dir>]\n\n"+
				"  Echo the agent's status to the live review UI (a pill shown across every\n"+
				"  open tab): `working` while applying a batch, `done` when finished. The\n"+
				"  optional message is short human detail (keep it plain). Written atomically\n"+
				"  to <REPO>/.prereview/llm-status.json; --out must match the REPO line.\n")
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) == 0 {
		fs.Usage()
		return fmt.Errorf("missing state (working|done)")
	}
	state := rest[0]
	if state != review.LLMStateWorking && state != review.LLMStateDone {
		return fmt.Errorf("state must be %q or %q, got %q", review.LLMStateWorking, review.LLMStateDone, state)
	}
	message := strings.Join(rest[1:], " ")

	dir, err := storeDir(*out)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	b, err := json.Marshal(review.LLMStatus{
		State:     state,
		Message:   message,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		return err
	}
	b = append(b, '\n')

	// Atomic write: temp file in the same dir + rename, so the server's 750ms
	// poll never sees a torn file (matches the atomicity the shell helper hand-
	// rolled, now owned by the CLI).
	path := filepath.Join(dir, review.LLMStatusFileName)
	tmp, err := os.CreateTemp(dir, ".llm-status-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return fmt.Errorf("write status: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename status: %w", err)
	}
	fmt.Printf("status: %s\n", state)
	return nil
}
