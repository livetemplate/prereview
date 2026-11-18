//go:build browser

// End-to-end for repo files served to the HTML preview: a file reached through
// a link inside the repo is served only when the file it points at would be.
//
// Run: go test -tags=browser -run 'TestE2E_PreviewServesLinkedFilesByTarget' ./e2e/...

package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	cdpruntime "github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

const linkedSecret = "linked-secret-7f3a"

// The page posts what its own loads returned to the parent page.
const linkedPage = `<!doctype html><html><body><p>links</p>
<script>
function report(k, v) { parent.postMessage({linkProbe: k, value: String(v)}, "*"); }
fetch("logo.png").then(r => r.text()).then(t => report("fetch", t), () => report("fetch", "error"));
window.addEventListener("load", () => report("script", typeof TOKEN === "string" ? TOKEN : "none"));
</script>
<script src="cfg.js"></script>
</body></html>
`

func setupLinkedFilesRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runCmd(t, dir, "git", "init", "-q", "-b", "main")
	runCmd(t, dir, "git", "config", "user.email", "test@example.com")
	runCmd(t, dir, "git", "config", "user.name", "Test")
	runCmd(t, dir, "git", "config", "commit.gpgsign", "false")
	mustWrite(t, dir, "keep.go", "package keep\n")
	runCmd(t, dir, "git", "add", "-A")
	runCmd(t, dir, "git", "commit", "-q", "-m", "seed")

	mustWrite(t, dir, ".env", `TOKEN="`+linkedSecret+`"`+"\n")
	mustWrite(t, dir, "links.html", linkedPage)
	for _, name := range []string{"logo.png", "cfg.js"} {
		if err := os.Symlink(".env", filepath.Join(dir, name)); err != nil {
			t.Fatalf("symlink %s: %v", name, err)
		}
	}
	return dir
}

func TestE2E_PreviewServesLinkedFilesByTarget(t *testing.T) {
	p := bootChromeAgainstRepo(t, setupLinkedFilesRepo(t), 1200, 800)
	diag := captureDiag(t, p)
	p.waitReady()
	if err := chromedp.Run(p.ctx, chromedp.Evaluate(`window.__linkProbe = {};
window.addEventListener("message", (e) => { if (e.data && e.data.linkProbe) window.__linkProbe[e.data.linkProbe] = e.data.value; });`, nil)); err != nil {
		t.Fatal(err)
	}
	p.clickFile("links.html")
	waitPreviewBridgeReady(t, p)

	// The preview's own loads, from its opaque origin.
	waitJSTrue(t, p.ctx, `Object.keys(window.__linkProbe).length === 2`, 10*time.Second, "the preview reported its loads")
	var inPreview map[string]string
	if err := chromedp.Run(p.ctx, chromedp.Evaluate(`window.__linkProbe`, &inPreview)); err != nil {
		t.Fatal(err)
	}
	for k, v := range inPreview {
		if strings.Contains(v, linkedSecret) {
			t.Errorf("the preview read a dotfile through a link (%s = %q)%s", k, v, diag())
		}
	}

	// The preview's files key serves only files that pass the checks by target.
	var byKey map[string]string
	if err := chromedp.Run(p.ctx, chromedp.Evaluate(`(async () => {
  const doc = new DOMParser().parseFromString(document.querySelector("iframe[srcdoc]").srcdoc, "text/html");
  const base = doc.querySelector("base").getAttribute("href");
  const out = {};
  for (const name of ["logo.png", "cfg.js", "links.html"]) {
    const r = await fetch(base + name);
    out[name] = r.status + " " + (await r.text()).slice(0, 80);
  }
  return out;
})()`, &byKey, func(e *cdpruntime.EvaluateParams) *cdpruntime.EvaluateParams { return e.WithAwaitPromise(true) })); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"logo.png", "cfg.js"} {
		if !strings.HasPrefix(byKey[name], "404 ") {
			t.Errorf("the files key served %s, a link to a dotfile: %q", name, byKey[name])
		}
	}
	if !strings.HasPrefix(byKey["links.html"], "200 ") {
		t.Errorf("the files key no longer serves the page itself: %q%s", byKey["links.html"], diag())
	}
}
