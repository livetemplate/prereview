package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// readPayload reads an agent verb's JSON payload from file, or from stdin when
// file is "" or "-" (the idiom `done`/`reply`/`applied` document).
func readPayload(file string) ([]byte, error) {
	if file != "" && file != "-" {
		raw, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", file, err)
		}
		return raw, nil
	}
	raw, err := io.ReadAll(os.Stdin)
	if err != nil {
		return nil, fmt.Errorf("read stdin: %w", err)
	}
	return raw, nil
}

// decodePayload decodes a JSON array, a single object, or newline-delimited
// objects into Ts.
func decodePayload[T any](raw []byte) ([]T, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("empty payload")
	}
	var out []T
	if trimmed[0] == '[' {
		if err := json.Unmarshal(trimmed, &out); err != nil {
			return nil, fmt.Errorf("parse JSON array: %w", err)
		}
		return out, nil
	}
	// A stream of one-or-more concatenated / newline-delimited objects covers a
	// single object and JSONL with one code path.
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	for {
		var v T
		if err := dec.Decode(&v); err == io.EOF {
			return out, nil
		} else if err != nil {
			return nil, fmt.Errorf("parse JSON object: %w", err)
		}
		out = append(out, v)
	}
}
