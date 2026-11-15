package review

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestCheckpoint_AcrossProcesses: two processes checkpointing the same content
// into the same stores record it once per store, never a duplicate version (the
// cross-process lock around Checkpoint's read-dedupe-append).
func TestCheckpoint_AcrossProcesses(t *testing.T) {
	const rounds = 40
	if dir := os.Getenv("CHECKPOINT_CHILD"); dir != "" {
		for i := 0; i < rounds; i++ {
			vs, err := NewVersionStore(filepath.Join(dir, fmt.Sprint(i)))
			if err != nil {
				os.Exit(2)
			}
			if _, _, err := vs.Checkpoint([]FileRef{{Path: "a.md", AbsPath: filepath.Join(dir, "a.md")}}, VersionTriggerFileEdit, ""); err != nil {
				os.Exit(3)
			}
		}
		os.Exit(0)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.md"), []byte("same content\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var cmds []*exec.Cmd
	for range 2 {
		cmd := exec.Command(os.Args[0], "-test.run", "^TestCheckpoint_AcrossProcesses$")
		cmd.Env = append(os.Environ(), "CHECKPOINT_CHILD="+dir)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		cmds = append(cmds, cmd)
	}
	for _, cmd := range cmds {
		if err := cmd.Wait(); err != nil {
			t.Fatalf("child: %v", err)
		}
	}
	for i := 0; i < rounds; i++ {
		vs, _ := NewVersionStore(filepath.Join(dir, fmt.Sprint(i)))
		cps, err := vs.Checkpoints()
		if err != nil {
			t.Fatal(err)
		}
		if len(cps) != 1 {
			t.Fatalf("store %d recorded %d versions of one content, want 1", i, len(cps))
		}
	}
}
