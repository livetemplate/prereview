package main

import "testing"

func TestRunExternalValidation(t *testing.T) {
	cases := []struct {
		name, extURL, out string
	}{
		{"not a url", "::::not-a-url", t.TempDir()},
		{"non-http scheme", "ftp://localhost:8080", t.TempDir()},
		{"missing host", "http://", t.TempDir()},
		{"missing out dir", "http://localhost:8080", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// All cases fail validation before any listener is bound.
			if err := runExternal(tc.extURL, tc.out, "127.0.0.1", false, 0, false, false); err == nil {
				t.Errorf("expected validation error for %s", tc.name)
			}
		})
	}
}
