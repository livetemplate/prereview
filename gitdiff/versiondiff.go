package gitdiff

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/hexops/gotextdiff"
	"github.com/hexops/gotextdiff/myers"
	"github.com/hexops/gotextdiff/span"
)

// RenderBytesAsFile renders raw file content as an all-context FileDiff — every
// line is "ctx", so the diff/file views show it plainly with no add/del
// coloring. It is the read-only render used to VIEW a historical artifact
// version (#90): the version store hands back the exact bytes of a past version
// (VersionStore.Blob), and this turns them into the same *FileDiff the live diff
// pane renders, syntax-highlighted identically via the shared highlightLines
// hook. Mirrors loadFileAsCtx but reads from bytes instead of a working-tree
// path, so it works on version blobs that no longer exist on disk.
func RenderBytesAsFile(path string, data []byte) *FileDiff {
	// Cheap binary detection: any NUL byte → binary (same rule as loadFileAsCtx).
	if bytes.IndexByte(data, 0x00) >= 0 {
		return &FileDiff{Path: path, IsBinary: true, Note: "binary file"}
	}
	fd := &FileDiff{Path: path}
	// Strip a single trailing newline so we don't emit a phantom empty line.
	content := strings.TrimSuffix(string(data), "\n")
	if content == "" {
		highlightLines(fd)
		return fd
	}
	for i, line := range strings.Split(content, "\n") {
		fd.Lines = append(fd.Lines, DiffLine{OldNum: i + 1, NewNum: i + 1, Kind: "ctx", Content: line})
	}
	highlightLines(fd)
	return fd
}

// DiffContents renders the line diff between two byte contents (old → new) as a
// FileDiff — used to compare a historical artifact version against the current
// working-tree file (#90 "Diff vs current"). It uses a pure-Go Myers diff
// (hexops/gotextdiff), so it needs no git and works on version blobs that were
// never committed, then feeds the unified output through the same
// parseUnifiedDiff + highlightLines the git path uses so the result renders
// identically (add/del/ctx rows, syntax highlighting). Identical inputs render
// as a plain all-context file; a binary on either side is reported as binary.
func DiffContents(path string, oldData, newData []byte) *FileDiff {
	if bytes.IndexByte(oldData, 0x00) >= 0 || bytes.IndexByte(newData, 0x00) >= 0 {
		return &FileDiff{Path: path, IsBinary: true, Note: "binary file"}
	}
	oldStr, newStr := string(oldData), string(newData)
	if oldStr == newStr {
		fd := RenderBytesAsFile(path, newData)
		fd.Note = "identical to current"
		return fd
	}
	edits := myers.ComputeEdits(span.URIFromPath("/"+path), oldStr, newStr)
	unified := fmt.Sprint(gotextdiff.ToUnified("a/"+path, "b/"+path, oldStr, edits))
	fd, err := parseUnifiedDiff(path, []byte(unified))
	if err != nil {
		// Never fail the view over a diff hiccup — fall back to the plain file.
		return RenderBytesAsFile(path, newData)
	}
	highlightLines(fd)
	return fd
}
