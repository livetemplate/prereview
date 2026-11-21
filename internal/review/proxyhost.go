package review

import (
	"context"
	"net"
	"net/http"
	"net/url"

	"github.com/livetemplate/livetemplate"
)

type requestHostKey struct{}

// WithRequestHost wraps a review page handler so Mount can see the host the
// browser reached it by. Both of livetemplate's Mount paths (page load and
// WebSocket connect) build their context from the request's.
func WithRequestHost(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestHostKey{}, r.Host)))
	})
}

// proxyURLFor is the live site's proxy address as this browser must load it:
// the proxy's own port on the host the browser used for the page. The launch
// token's cookie belongs to that host, so a page opened by its tailnet name
// (an ALT line) and a frame pointed at the bind IP would send the proxy no
// cookie, and every request would be refused. The proxy binds the same
// address as the page, so the same name reaches it.
func proxyURLFor(base string, ctx *livetemplate.Context) string {
	if base == "" || ctx == nil {
		return base
	}
	host, _ := ctx.Value(requestHostKey{}).(string)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	u, err := url.Parse(base)
	if host == "" || err != nil {
		return base
	}
	u.Host = net.JoinHostPort(host, u.Port())
	return u.String()
}
