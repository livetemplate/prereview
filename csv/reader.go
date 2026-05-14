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
	r.FieldsPerRecord = -1 // variable: tolerate 7-col legacy + 8-col current schema

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
	// Accept both 7-column (pre-resolve schema) and 8-column (current) rows
	// so old CSVs round-trip cleanly. Missing resolved field defaults to false.
	if len(rec) != 7 && len(rec) != 8 {
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
	if len(rec) == 8 {
		resolved = rec[7] == "true"
	}
	return Row{
		ID:        rec[0],
		File:      rec[1],
		FromLine:  from,
		ToLine:    to,
		Side:      rec[4],
		Body:      rec[5],
		CreatedAt: created,
		Resolved:  resolved,
	}, true
}
