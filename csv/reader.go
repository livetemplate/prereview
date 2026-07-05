package csv

import (
	stdcsv "encoding/csv"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"
)

// Read parses every Row from a CSV written by Writer. Returns an empty
// slice (and nil error) if the file doesn't exist — that's the "fresh
// session" case, indistinguishable from "session with zero comments".
// Skips the header row and any malformed rows (logging would be a caller
// concern; here we stay quiet to keep the CSV-as-truth contract simple).
func Read(path string) ([]Row, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	r := stdcsv.NewReader(f)
	r.FieldsPerRecord = -1 // variable: tolerate 7..15-col legacy + 16-col current schema

	// Discard header.
	if _, err := r.Read(); err != nil {
		if err == io.EOF {
			return nil, nil // empty file → no rows
		}
		return nil, fmt.Errorf("read header: %w", err)
	}

	var rows []Row
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read row: %w", err)
		}
		row, ok := recordToRow(rec)
		if !ok {
			// Malformed row — skip. The CSV is regenerated on every write
			// from in-memory state, so a single bad row shouldn't kill the
			// whole load; the next write will repair the file.
			continue
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func recordToRow(rec []string) (Row, bool) {
	// Accept 7-col (pre-resolve), 8-col (pre-anchor), 9-col (anchor, no
	// status), 10-col (pre-kind), 11-col (pre-area), 12-col (pre-url),
	// 13-col (pre-text-cols), 14-col (from_col, no to_col — never written,
	// tolerated for safety), 15-col (pre-hidden), 16-col (pre-enqueued) and
	// 17-col (current) rows so old CSVs round-trip cleanly. Missing resolved →
	// false; missing anchor → ""; missing status → "" (treated as "ok"); missing
	// kind → "" (treated as "line"); missing area → "" (only meaningful for
	// kind=area/region); missing url → "" (only meaningful for kind=region);
	// missing from_col/to_col → 0 (only meaningful for kind=text); missing hidden
	// → false; missing enqueued → true (legacy comments were always active, #119).
	switch len(rec) {
	case 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17:
	default:
		return Row{}, false
	}
	from, err := strconv.Atoi(rec[2])
	if err != nil {
		return Row{}, false
	}
	to, err := strconv.Atoi(rec[3])
	if err != nil {
		return Row{}, false
	}
	created, err := time.Parse(time.RFC3339, rec[6])
	if err != nil {
		return Row{}, false
	}
	resolved := false
	if len(rec) >= 8 {
		resolved = rec[7] == "true"
	}
	anchor := ""
	if len(rec) >= 9 {
		anchor = rec[8]
	}
	status := ""
	if len(rec) >= 10 {
		status = rec[9]
	}
	kind := ""
	if len(rec) >= 11 {
		kind = rec[10]
	}
	area := ""
	if len(rec) >= 12 {
		area = rec[11]
	}
	url := ""
	if len(rec) >= 13 {
		url = rec[12]
	}
	// from_col/to_col are only meaningful for kind=text; a non-integer or
	// missing value degrades to 0 (whole-line semantics) rather than
	// dropping the whole row.
	fromCol := 0
	if len(rec) >= 14 {
		fromCol, _ = strconv.Atoi(rec[13])
	}
	toCol := 0
	if len(rec) >= 15 {
		toCol, _ = strconv.Atoi(rec[14])
	}
	hidden := false
	if len(rec) >= 16 {
		hidden = rec[15] == "true"
	}
	// `enqueued` (col 17) persists inverted: Draft == (enqueued=="false").
	// ABSENT ⇒ enqueued=true ⇒ Draft=false, so legacy rows stay active (#119).
	draft := false
	if len(rec) >= 17 {
		draft = rec[16] == "false"
	}
	return Row{
		ID:           rec[0],
		File:         rec[1],
		FromLine:     from,
		ToLine:       to,
		Side:         rec[4],
		Body:         rec[5],
		CreatedAt:    created,
		Resolved:     resolved,
		Anchor:       anchor,
		AnchorStatus: status,
		Kind:         kind,
		Area:         area,
		URL:          url,
		FromCol:      fromCol,
		ToCol:        toCol,
		Hidden:       hidden,
		Draft:        draft,
	}, true
}
