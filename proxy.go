package main

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
)

// maxInjectableHTML caps how much of an HTML response we buffer to inject the
// beacon. Real pages are far smaller; anything larger is passed through
// untouched rather than held entirely in memory.
const maxInjectableHTML = 8 << 20 // 8 MiB

// proxyBeaconJS is injected into every proxied HTML page. It runs *inside* the
// proxied (cross-origin) iframe and reports navigation + scroll up to the
// parent prereview UI via postMessage. It is a beacon, not a UI — no DOM, no
// styles, no deps — so it can't collide with the proxied app. The parent side
// validates event.origin before trusting a message (see the client bridge).
//
// Proven in the Phase 0 spike against a live Vite dev server: load + pushState
// + popstate fire `nav`; throttled scroll fires `scroll` with the document
// dimensions the parent needs to place re-pinned annotations.
const proxyBeaconJS = `<script>(function(){
  function rel(){return location.pathname+location.search;}
  function metrics(){var e=document.documentElement;return {scrollX:window.scrollX,scrollY:window.scrollY,docW:e.scrollWidth,docH:e.scrollHeight};}
  function post(extra){try{parent.postMessage(Object.assign({__prereview:true,url:rel()},metrics(),extra),'*');}catch(e){}}
  function nav(){post({type:'nav'});}
  addEventListener('load',nav);addEventListener('popstate',nav);
  var _p=history.pushState;history.pushState=function(){var r=_p.apply(this,arguments);nav();return r;};
  var _r=history.replaceState;history.replaceState=function(){var r=_r.apply(this,arguments);nav();return r;};
  var tick=false;
  addEventListener('scroll',function(){if(tick)return;tick=true;requestAnimationFrame(function(){tick=false;post({type:'scroll'});});},{passive:true});
  // Locate-an-annotation: the parent posts {__prereviewFocus,url,y} when the
  // user taps a sidebar entry. y is a 0..1 fraction of the document. If the
  // annotation lives on another page, navigate there (stashing the scroll for
  // after load); otherwise scroll it into view. Runs inside the iframe, so it
  // can do both — the parent can't (cross-origin reads/scroll are blocked).
  function scrollToFrac(f){var h=document.documentElement.scrollHeight;window.scrollTo({top:Math.max(0,f*h-80),behavior:'smooth'});}
  addEventListener('message',function(e){
    var d=e.data; if(!d||d.__prereviewFocus!==true)return;
    var y=typeof d.y==='number'?d.y:0;
    if(d.url && d.url!==rel()){ try{sessionStorage.setItem('__prereviewFocusY',String(y));}catch(_){} location.href=d.url; return; }
    scrollToFrac(y);
  });
  try{var py=sessionStorage.getItem('__prereviewFocusY');if(py!==null){sessionStorage.removeItem('__prereviewFocusY');setTimeout(function(){scrollToFrac(parseFloat(py));},60);}}catch(_){}
  // postMessage isn't queued for listeners that attach later, and the parent's
  // proxy-bridge attaches only after its deferred client connects — which can
  // be AFTER this (tiny) page has already loaded. Re-announce nav at a few
  // early delays so the bridge catches one once attached; it dedupes by url.
  [0,250,700,1500,3000].forEach(function(d){setTimeout(nav,d);});
})();</script>`

// newExternalProxy builds the reverse proxy that fronts the user's live local
// server in `--external` mode. It runs on its OWN port (a separate origin from
// the prereview UI) so the app's root-relative URLs (`/api/…`, `/@vite/client`,
// its own websocket) resolve against the proxy root and forward cleanly with
// zero URL rewriting — the make-or-break property a same-origin path-prefix
// proxy can't provide. httputil.ReverseProxy upgrades websockets natively.
//
// For HTML navigations it strips framing/CSP blockers (so the page can be
// iframed by the UI) and injects proxyBeaconJS before </body>.
func newExternalProxy(target *url.URL) http.Handler {
	rp := &httputil.ReverseProxy{}
	rp.Rewrite = func(pr *httputil.ProxyRequest) {
		pr.SetURL(target)
		// Only document navigations get the beacon injected, so only those
		// need to come back uncompressed. Asset responses keep their
		// upstream compression untouched.
		if isHTMLNavigation(pr.Out) {
			pr.Out.Header.Del("Accept-Encoding")
		}
	}

	rp.ModifyResponse = func(resp *http.Response) error {
		// Allow the proxied page to be framed by the UI and allow the
		// injected inline beacon to run. These are the user's own local
		// dev responses, served back to the user — relaxing them is safe.
		resp.Header.Del("X-Frame-Options")
		resp.Header.Del("Content-Security-Policy")
		resp.Header.Del("Content-Security-Policy-Report-Only")

		if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
			return nil
		}
		if resp.ContentLength > maxInjectableHTML {
			return nil
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxInjectableHTML+1))
		resp.Body.Close()
		if err != nil {
			return err
		}
		if len(body) > maxInjectableHTML {
			// Too large to safely buffer — pass the (already-read) bytes
			// straight back without injection.
			resp.Body = io.NopCloser(bytes.NewReader(body))
			return nil
		}
		out := injectBeacon(body)
		resp.Body = io.NopCloser(bytes.NewReader(out))
		resp.ContentLength = int64(len(out))
		resp.Header.Set("Content-Length", strconv.Itoa(len(out)))
		return nil
	}

	rp.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		slog.Debug("proxy upstream error", "url", r.URL.String(), "err", err)
		http.Error(w, "prereview: proxy target unreachable ("+target.String()+")", http.StatusBadGateway)
	}
	return rp
}

// isHTMLNavigation reports whether a request is a top-level document fetch
// (where the response is HTML we'll inject into), as opposed to an asset/XHR.
func isHTMLNavigation(req *http.Request) bool {
	return strings.Contains(req.Header.Get("Accept"), "text/html")
}

// injectBeacon places the beacon <script> just before the last </body>, or
// appends it if the document has no body close tag.
func injectBeacon(body []byte) []byte {
	inj := []byte(proxyBeaconJS)
	if i := bytes.LastIndex(body, []byte("</body>")); i >= 0 {
		out := make([]byte, 0, len(body)+len(inj))
		out = append(out, body[:i]...)
		out = append(out, inj...)
		out = append(out, body[i:]...)
		return out
	}
	return append(body, inj...)
}
