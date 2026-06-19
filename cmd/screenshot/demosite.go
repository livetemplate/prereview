//go:build ignore

// demosite is a throwaway static "live site" used only as the --external
// proxy target when recording the live-site-region README GIF. It is excluded
// from normal builds/vet (//go:build ignore) and run directly:
//
//	go run cmd/screenshot/demosite.go --port 0
//
// It prints "READY http://127.0.0.1:PORT" so the capture script can pick up
// the address, mirroring prereview's own startup protocol.
package main

import (
	"flag"
	"fmt"
	"net"
	"net/http"
)

const page = `<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Acme — Ship faster</title>
<style>
  * { box-sizing: border-box; }
  body { margin: 0; font-family: -apple-system, Segoe UI, Roboto, Helvetica, Arial, sans-serif; color: #0f172a; }
  header { display: flex; align-items: center; justify-content: space-between; padding: 18px 40px; border-bottom: 1px solid #e2e8f0; }
  header .brand { font-weight: 700; font-size: 20px; }
  header nav a { margin-left: 24px; color: #475569; text-decoration: none; font-size: 15px; }
  .hero { text-align: center; padding: 88px 24px 72px; background: linear-gradient(#ffffff, #f1f5f9); }
  .hero h1 { font-size: 44px; margin: 0 0 16px; letter-spacing: -0.02em; }
  .hero p { font-size: 19px; color: #475569; max-width: 560px; margin: 0 auto 32px; }
  .cta { display: inline-block; padding: 13px 26px; border-radius: 8px; background: #cbd5e1; color: #64748b; font-weight: 600; font-size: 16px; text-decoration: none; }
  .features { display: flex; gap: 24px; max-width: 960px; margin: 64px auto; padding: 0 24px; }
  .card { flex: 1; border: 1px solid #e2e8f0; border-radius: 12px; padding: 24px; }
  .card h3 { margin: 0 0 8px; font-size: 18px; }
  .card p { margin: 0; color: #64748b; font-size: 15px; }
  footer { text-align: center; color: #94a3b8; padding: 40px; font-size: 14px; border-top: 1px solid #e2e8f0; }
</style></head>
<body>
  <header>
    <div class="brand">▲ Acme</div>
    <nav><a href="/">Product</a><a href="/">Pricing</a><a href="/">Docs</a><a href="/">Sign in</a></nav>
  </header>
  <section class="hero">
    <h1>Ship faster, review locally.</h1>
    <p>Catch what matters before it leaves your machine — code, docs, designs, and live pages.</p>
    <a class="cta" href="/">Get started free</a>
  </section>
  <section class="features">
    <div class="card"><h3>Any artifact</h3><p>Diffs, Markdown, HTML, images, and running sites.</p></div>
    <div class="card"><h3>Any granularity</h3><p>Line, block, file, or a dragged region.</p></div>
    <div class="card"><h3>Hand off to an LLM</h3><p>Your comments become fixes, before you push.</p></div>
  </section>
  <footer>© Acme, Inc. — demo site for prereview --external</footer>
</body></html>`

func main() {
	port := flag.Int("port", 0, "port (0 = OS-assigned)")
	flag.Parse()

	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", *port))
	if err != nil {
		panic(err)
	}
	fmt.Printf("READY http://%s\n", ln.Addr().String())

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(page))
	})
	_ = http.Serve(ln, mux)
}
