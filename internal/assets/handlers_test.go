package assets

import (
	"strings"
	"testing"
)

// clientSentinel is a log message only the client's attribute registry
// contains, so its presence in the handler bundle means the client was bundled
// in.
const clientSentinel = "registering a queued handler threw"

// The handler bundle imports @livetemplate/client for types only. A runtime
// import would bundle a second copy of the client, and the handlers would
// register into that copy's registry, which the page never runs: every
// selection, deep link and viewport report would silently do nothing.
func TestHandlerBundleExcludesTheClient(t *testing.T) {
	if !strings.Contains(string(clientJS), clientSentinel) {
		t.Fatalf("sentinel %q is not in the vendored client either; pick one the client still logs", clientSentinel)
	}
	if strings.Contains(string(handlersJS), clientSentinel) {
		t.Fatal("prereview-handlers.js contains the livetemplate client; import it with `import type` and keep --external:@livetemplate/client")
	}
	if !strings.Contains(string(handlersJS), "lvtAttributes") {
		t.Fatal("prereview-handlers.js does not register through window.lvtAttributes")
	}
}
