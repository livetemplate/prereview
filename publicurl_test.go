package main

import (
	"strings"
	"testing"
)

func TestResolvePublicURL(t *testing.T) {
	cases := []struct {
		name, raw string
		port      int
		portSet   bool
		external  bool
		wantURL   string
		wantPort  int
		wantErr   string
	}{
		{name: "unset keeps --port", raw: "", port: 0, wantPort: 0},
		{name: "unset with external is fine", raw: "", external: true, port: 7, wantPort: 7},
		{name: "port from the URL", raw: "https://vm.exe.xyz:4799/", wantURL: "https://vm.exe.xyz:4799", wantPort: 4799},
		{name: "explicit --port wins", raw: "https://vm.exe.xyz:4799", port: 5000, portSet: true, wantURL: "https://vm.exe.xyz:4799", wantPort: 5000},
		{name: "no port and no --port", raw: "https://box-4799.app.github.dev", wantErr: "pass --port"},
		{name: "explicit --port 0", raw: "https://vm.exe.xyz:4799", port: 0, portSet: true, wantErr: "pass --port"},
		{name: "no port but --port", raw: "https://box-4799.app.github.dev", port: 4799, portSet: true, wantURL: "https://box-4799.app.github.dev", wantPort: 4799},
		{name: "not http", raw: "ftp://h:21", wantErr: "want an http(s) URL"},
		{name: "no host", raw: "https://", wantErr: "want an http(s) URL"},
		{name: "with --external", raw: "https://vm.exe.xyz:4799", external: true, wantErr: "doesn't work with --external"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			u, p, err := resolvePublicURL(c.raw, c.port, c.portSet, c.external)
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("err = %v, want one containing %q", err, c.wantErr)
				}
				return
			}
			if err != nil || u != c.wantURL || p != c.wantPort {
				t.Errorf("= (%q, %d, %v), want (%q, %d, nil)", u, p, err, c.wantURL, c.wantPort)
			}
		})
	}
}
