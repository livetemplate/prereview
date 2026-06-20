package netaddr

import (
	"strings"
	"testing"
)

// TestResolveBindHost pins the four-row contract documented above
// ResolveBindHost. It is the executable spec for the user-implemented
// decision function — keep these green.
func TestResolveBindHost(t *testing.T) {
	tests := []struct {
		name         string
		explicitHost bool
		host         string
		remote       bool
		tsIP         string
		wantHost     string
		wantWarn     bool // true ⇒ warn must be non-empty
	}{
		{
			name:         "explicit --host wins even when remote with tailnet",
			explicitHost: true, host: "0.0.0.0", remote: true, tsIP: "100.1.2.3",
			wantHost: "0.0.0.0", wantWarn: false,
		},
		{
			name:         "explicit --host wins locally",
			explicitHost: true, host: "192.168.1.5", remote: false, tsIP: "",
			wantHost: "192.168.1.5", wantWarn: false,
		},
		{
			name:         "remote with tailnet binds the Tailscale IP",
			explicitHost: false, host: "127.0.0.1", remote: true, tsIP: "100.123.67.113",
			wantHost: "100.123.67.113", wantWarn: false,
		},
		{
			name:         "remote WITHOUT tailnet falls back to loopback and warns",
			explicitHost: false, host: "127.0.0.1", remote: true, tsIP: "",
			wantHost: "127.0.0.1", wantWarn: true,
		},
		{
			name:         "local dev keeps the historical loopback default",
			explicitHost: false, host: "127.0.0.1", remote: false, tsIP: "",
			wantHost: "127.0.0.1", wantWarn: false,
		},
		{
			name:         "local dev on a tailnet still uses loopback (gated on remote, not tailnet presence)",
			explicitHost: false, host: "127.0.0.1", remote: false, tsIP: "100.9.9.9",
			wantHost: "127.0.0.1", wantWarn: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotHost, gotWarn := ResolveBindHost(tt.explicitHost, tt.host, tt.remote, tt.tsIP)
			if gotHost != tt.wantHost {
				t.Errorf("bindHost = %q, want %q", gotHost, tt.wantHost)
			}
			if (gotWarn != "") != tt.wantWarn {
				t.Errorf("warn = %q, want non-empty=%v", gotWarn, tt.wantWarn)
			}
			// The remote-without-tailnet warning is the only feedback the
			// user gets before chasing an unreachable URL. It must be
			// actionable: name the --host escape hatch.
			if tt.wantWarn && !strings.Contains(gotWarn, "--host") {
				t.Errorf("fallback warn must mention --host so the user can fix it; got %q", gotWarn)
			}
		})
	}
}

func TestAltURLs(t *testing.T) {
	tests := []struct {
		name     string
		bindHost string
		tsIP     string
		magicDNS string
		port     int
		want     []string
	}{
		{
			name:     "bound to Tailscale IP: advertise the friendlier MagicDNS host, dedupe the IP",
			bindHost: "100.1.2.3", tsIP: "100.1.2.3", magicDNS: "box.tail-scale.ts.net", port: 8080,
			want: []string{"http://box.tail-scale.ts.net:8080"},
		},
		{
			name:     "loopback, no tailnet: nothing extra to advertise",
			bindHost: "127.0.0.1", tsIP: "", magicDNS: "", port: 9000,
			want: nil,
		},
		{
			name:     "explicit --host on a tailnet box: advertise both Tailscale forms",
			bindHost: "127.0.0.1", tsIP: "100.5.5.5", magicDNS: "h.ts.net", port: 3000,
			want: []string{"http://h.ts.net:3000", "http://100.5.5.5:3000"},
		},
		{
			name:     "tailnet IP but no MagicDNS (CLI absent): advertise the IP form",
			bindHost: "127.0.0.1", tsIP: "100.5.5.5", magicDNS: "", port: 3000,
			want: []string{"http://100.5.5.5:3000"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := AltURLs(tt.bindHost, tt.tsIP, tt.magicDNS, tt.port)
			if len(got) != len(tt.want) {
				t.Fatalf("altURLs = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("altURLs[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}
