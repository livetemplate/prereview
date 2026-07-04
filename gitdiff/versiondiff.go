package gitdiff

import (
	"bytes"
	"strings"
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
