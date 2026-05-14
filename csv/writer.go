package csv

import (
	stdcsv "encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

// Row is one comment serialized to the CSV. The csv package stays free of
// prereview's main-package types so the schema layer stays independent and
// reusable (and avoids an import cycle with the main package).
type Row struct {
	ID        string
	File      string
	FromLine  int
	ToLine    int
	Side      string
	Body      string
	CreatedAt time.Time
	Resolved  bool
}

// Writer serializes Rows to a CSV file atomically. Each Write replaces the
// entire file (write-tmp → fsync → rename → fsync parent dir), so the file
// on disk is either the pre-write state or the post-write state — never
// half-written, even if the process is killed mid-call.
//
// All operations are serialized by an internal mutex; multiple WebSocket
// sessions for the same prereview process can call Write concurrently
// without corruption.
type Writer struct {
	path string
	mu   sync.Mutex
}

// NewWriter returns a Writer targeting path. The file is not created until
// the first Write call.
func NewWriter(path string) *Writer {
	return &Writer{path: path}
}

// Path returns the destination path the writer was constructed with.
func (w *Writer) Path() string { return w.path }

// Write replaces the CSV at w.path with header + every row. It returns only
// after the on-disk file has been fsynced and renamed; once Write returns
// nil, a reader on another process will see the full file.
func (w *Writer) Write(rows []Row) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	dir := filepath.Dir(w.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, ".prereview-csv-*.tmp")
	if err != nil {
		return fmt.Errorf("create tmp: %w", err)
	}
	tmpPath := tmp.Name()
	// On any error after this point, best-effort delete the tmp file.
	defer func() {
		if tmpPath != "" {
			_ = os.Remove(tmpPath)
		}
	}()

	cw := stdcsv.NewWriter(tmp)
	if err := cw.Write(Header); err != nil {
		tmp.Close()
		return fmt.Errorf("write header: %w", err)
	}
	for _, r := range rows {
		if err := cw.Write(rowToRecord(r)); err != nil {
			tmp.Close()
			return fmt.Errorf("write row %s: %w", r.ID, err)
		}
	}
	cw.Flush()
	if err := cw.Error(); err != nil {
		tmp.Close()
		return fmt.Errorf("csv flush: %w", err)
	}

	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("fsync tmp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close tmp: %w", err)
	}
	if err := os.Rename(tmpPath, w.path); err != nil {
		return fmt.Errorf("rename tmp -> %s: %w", w.path, err)
	}
	tmpPath = "" // suppress the deferred cleanup; the file IS the final destination now

	// fsync the parent directory so the rename itself is durable. POSIX
	// rename is atomic visibility-wise, but the directory entry change
	// isn't durable until the dir's metadata is synced.
	dirF, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open dir for fsync: %w", err)
	}
	defer dirF.Close()
	if err := dirF.Sync(); err != nil {
		return fmt.Errorf("fsync dir: %w", err)
	}
	return nil
}

func rowToRecord(r Row) []string {
	resolved := "false"
	if r.Resolved {
		resolved = "true"
	}
	return []string{
		r.ID,
		r.File,
		strconv.Itoa(r.FromLine),
		strconv.Itoa(r.ToLine),
		r.Side,
		r.Body,
		r.CreatedAt.UTC().Format(time.RFC3339),
		resolved,
	}
}
