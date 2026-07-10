package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/livetemplate/prereview/csv"
	"github.com/livetemplate/prereview/internal/review"
)

// seedStore writes a comments.csv under <root>/.prereview with the given rows so
// the validation in `processed` has ids to check against.
func seedStore(t *testing.T, root string, rows []csv.Row) {
	t.Helper()
	dir := filepath.Join(root, ".prereview")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := csv.NewWriter(filepath.Join(dir, review.CommentsFileName)).Write(rows); err != nil {
		t.Fatalf("seed csv: %v", err)
	}
}

func row(id string) csv.Row {
	return csv.Row{ID: id, File: "f", FromLine: 1, ToLine: 1, Side: "new", Body: "x", CreatedAt: time.Unix(0, 0)}
}

// TestRunProcessed_AppendsMarks verifies the subcommand writes one JSONL line per
// id into <out>/.prereview/processed.jsonl and APPENDS on subsequent calls (never
// rewrites — the append-only contract that keeps it off the server's CSV rail).
func TestRunProcessed_AppendsMarks(t *testing.T) {
	dir := t.TempDir()
	seedStore(t, dir, []csv.Row{row("id1"), row("id2"), row("id3")})

	if err := runProcessed([]string{"--out", dir, "id1", "id2"}); err != nil {
		t.Fatalf("runProcessed: %v", err)
	}
	path := filepath.Join(dir, ".prereview", review.ProcessedFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read markers: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 lines, got %d: %q", len(lines), data)
	}
	if !strings.Contains(lines[0], `"id":"id1"`) || !strings.Contains(lines[1], `"id":"id2"`) {
		t.Errorf("markers missing ids: %q", data)
	}

	// Append-only: a second call adds a line, never rewrites.
	if err := runProcessed([]string{"--out", dir, "id3"}); err != nil {
		t.Fatalf("runProcessed 2: %v", err)
	}
	data2, _ := os.ReadFile(path)
	if n := len(strings.Split(strings.TrimSpace(string(data2)), "\n")); n != 3 {
		t.Fatalf("want 3 lines after append, got %d: %q", n, data2)
	}
}

// TestRunProcessed_NoIDs is the error path: a mark with no id is a usage error.
func TestRunProcessed_NoIDs(t *testing.T) {
	dir := t.TempDir()
	seedStore(t, dir, []csv.Row{row("id1")})
	if err := runProcessed([]string{"--out", dir}); err == nil {
		t.Error("expected error when no ids given")
	}
}

// --- unit: id-input parsing ---

func TestParseIDsInput(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"bare newline ids", "a\nb\n\nc\n", []string{"a", "b", "c"}},
		{"json array of strings", `["a","b"]`, []string{"a", "b"}},
		{"json array of objects", `[{"id":"a","file":"x"},{"id":"b"}]`, []string{"a", "b"}},
		{"single json object", `{"id":"a"}`, []string{"a"}},
		{"jsonl objects", `{"id":"a"}` + "\n" + `{"id":"b"}`, []string{"a", "b"}},
	}
	for _, tt := range tests {
		got, err := parseIDsInput([]byte(tt.in))
		if err != nil {
			t.Errorf("%s: %v", tt.name, err)
			continue
		}
		if strings.Join(got, ",") != strings.Join(tt.want, ",") {
			t.Errorf("%s: got %v, want %v", tt.name, got, tt.want)
		}
	}
	if _, err := parseIDsInput([]byte("   ")); err == nil {
		t.Error("empty input should error")
	}
}

func TestDedupe(t *testing.T) {
	got := dedupe([]string{"a", "b", "a", "c", "b"})
	if want := "a,b,c"; strings.Join(got, ",") != want {
		t.Errorf("dedupe = %v, want %v", got, want)
	}
}

// --- integration: the built binary's exit code (the #1 regression) ---

var (
	binOnce sync.Once
	binPath string
	binErr  error
)

// prereviewBin builds the CLI once (GOWORK=off, matching CI) so the integration
// tests exercise the real argv → exit-code contract, not just runProcessed's
// return value.
func prereviewBin(t *testing.T) string {
	t.Helper()
	binOnce.Do(func() {
		dir, err := os.MkdirTemp("", "prereview-bin")
		if err != nil {
			binErr = err
			return
		}
		binPath = filepath.Join(dir, "prereview")
		cmd := exec.Command("go", "build", "-o", binPath, ".")
		cmd.Env = append(os.Environ(), "GOWORK=off")
		if out, buildErr := cmd.CombinedOutput(); buildErr != nil {
			binErr = fmt.Errorf("build: %v\n%s", buildErr, out)
		}
	})
	if binErr != nil {
		t.Fatalf("build test binary: %v", binErr)
	}
	return binPath
}

type runResult struct {
	stdout, stderr string
	exit           int
}

func runBin(t *testing.T, stdin string, args ...string) runResult {
	t.Helper()
	cmd := exec.Command(prereviewBin(t), args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var outBuf, errBuf strings.Builder
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	exit := 0
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("run %v: %v", args, err)
		}
		exit = ee.ExitCode()
	}
	return runResult{outBuf.String(), errBuf.String(), exit}
}

func processedIDs(t *testing.T, root string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, ".prereview", review.ProcessedFileName))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

func TestProcessed_BogusIDExitsNonZero(t *testing.T) {
	root := t.TempDir()
	seedStore(t, root, []csv.Row{row("real-1")})

	res := runBin(t, "", "processed", "--out", root, "totally-bogus-xyz")
	if res.exit == 0 {
		t.Errorf("bogus id should exit non-zero; got 0\nstdout: %s", res.stdout)
	}
	if !strings.Contains(res.stderr, "totally-bogus-xyz") {
		t.Errorf("stderr should name the unknown id; got: %s", res.stderr)
	}
	if got := processedIDs(t, root); len(got) != 0 {
		t.Errorf("bogus id must NOT be recorded; processed.jsonl has: %v", got)
	}
}

func TestProcessed_ValidIDSucceeds(t *testing.T) {
	root := t.TempDir()
	seedStore(t, root, []csv.Row{row("real-1")})

	res := runBin(t, "", "processed", "--out", root, "real-1")
	if res.exit != 0 {
		t.Fatalf("valid id should exit 0; got %d\nstderr: %s", res.exit, res.stderr)
	}
	if got := processedIDs(t, root); len(got) != 1 || !strings.Contains(got[0], "real-1") {
		t.Errorf("expected real-1 recorded; got: %v", got)
	}
}

func TestProcessed_FileStdin(t *testing.T) {
	root := t.TempDir()
	seedStore(t, root, []csv.Row{row("a"), row("b")})

	res := runBin(t, "a\nb\n", "processed", "--out", root, "--file", "-")
	if res.exit != 0 {
		t.Fatalf("stdin ids should exit 0; got %d\nstderr: %s", res.exit, res.stderr)
	}
	if got := processedIDs(t, root); len(got) != 2 {
		t.Errorf("expected 2 marks from stdin; got: %v", got)
	}
}

func TestProcessed_AllOpen(t *testing.T) {
	root := t.TempDir()
	seedStore(t, root, []csv.Row{
		row("open-1"),
		{ID: "resolved-1", File: "f", FromLine: 2, ToLine: 2, Side: "new", Body: "y", CreatedAt: time.Unix(0, 0), Resolved: true},
	})

	res := runBin(t, "", "processed", "--out", root, "--all-open")
	if res.exit != 0 {
		t.Fatalf("--all-open should exit 0; got %d\nstderr: %s", res.exit, res.stderr)
	}
	got := processedIDs(t, root)
	if len(got) != 1 || !strings.Contains(got[0], "open-1") {
		t.Errorf("--all-open should mark only the open comment; got: %v", got)
	}
}

func TestProcessed_AllOpenRejectsExplicitIDs(t *testing.T) {
	root := t.TempDir()
	seedStore(t, root, []csv.Row{row("a")})
	res := runBin(t, "", "processed", "--out", root, "--all-open", "a")
	if res.exit == 0 {
		t.Errorf("--all-open with explicit ids should error")
	}
}
