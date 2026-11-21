package review

import (
	"context"
	"testing"

	"github.com/livetemplate/livetemplate"
)

// TestProxyURLFor pins that the frame loads the proxy on the host the browser
// used for the page (so the launch cookie goes with it), keeping its port.
func TestProxyURLFor(t *testing.T) {
	ctxFor := func(host string) *livetemplate.Context {
		return livetemplate.NewContext(context.WithValue(context.Background(), requestHostKey{}, host), "", nil)
	}
	const base = "http://100.64.0.1:41935/"
	for _, tc := range []struct{ name, host, want string }{
		{"tailnet name with port", "devbox.example.ts.net:44017", "http://devbox.example.ts.net:41935/"},
		{"same IP", "100.64.0.1:44017", base},
		{"IPv6", "[::1]:44017", "http://[::1]:41935/"},
		{"no host", "", base},
	} {
		if got := proxyURLFor(base, ctxFor(tc.host)); got != tc.want {
			t.Errorf("%s: proxyURLFor(%q) = %q, want %q", tc.name, tc.host, got, tc.want)
		}
	}
	if got := proxyURLFor(base, nil); got != base {
		t.Errorf("nil ctx: %q", got)
	}
	if got := proxyURLFor("", ctxFor("x:1")); got != "" {
		t.Errorf("no proxy: %q", got)
	}
}
