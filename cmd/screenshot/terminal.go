// Terminal pane + compositor for the README hero GIF.
//
// The hero GIF closes prereview's review→fix loop: the browser pane shows the
// human commenting and handing off; this terminal pane shows the Claude Code
// skill reading that comment and editing the file. The transcript is scripted
// (deterministic — no live LLM call, so `make gifs` reproduces byte-for-byte)
// but faithful to skill/SKILL.md: Claude reads the handoff and edits the file.
// It does NOT mark comments resolved — that's a human action (the "Resolve"
// button), so the transcript never claims to resolve.
//
// The pane is a plain HTML doc rendered in a second chromedp tab (no server, no
// new dependency); composeStacked then stitches the browser screenshot above
// the terminal screenshot into one frame, which gif.go downscales + quantizes
// through the same pure-Go path as every other flow.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/draw"

	"github.com/chromedp/chromedp"
)

// termLine is one rendered transcript line. Kind selects the styling class:
//
//	prompt — the human's slash-command input (">")
//	dim    — a muted tool-result / status line ("⎿")
//	bullet — a Claude action line ("●", accent dot)
//	del    — a removed line in an Edit block ("-", red)
//	add    — an added line in an Edit block ("+", green)
//	plain  — default text
//	sp     — a blank spacer line
type termLine struct {
	T string `json:"t"` // text (rendered via textContent — auto-escaped)
	K string `json:"k"` // kind
}

// termInit loads a blank page and injects the terminal chrome (title bar +
// scrollback container) plus its styling. Subsequent termRender calls only
// replace the scrollback body, so the chrome stays put across frames.
func termInit(ctx context.Context) error {
	return chromedp.Run(ctx,
		chromedp.Navigate("about:blank"),
		chromedp.Evaluate(termSkeletonJS, nil),
	)
}

// termRender replaces the scrollback with the given lines. Call it with a
// growing slice to animate the transcript appearing line by line.
func termRender(ctx context.Context, lines []termLine) error {
	data, err := json.Marshal(lines)
	if err != nil {
		return err
	}
	return chromedp.Run(ctx, chromedp.Evaluate(fmt.Sprintf(termRenderJS, string(data)), nil))
}

// composeStacked draws top above bottom on one canvas (top-aligned, centered
// horizontally if widths differ), separated by a gap of bg. Heights need not
// match — the canvas is sized to fit both. This keeps each pane at full width
// (legible at GitHub's ~content-width image cap), unlike a side-by-side split.
func composeStacked(top, bottom image.Image, gap int, bg color.Color) image.Image {
	tb, bb := top.Bounds(), bottom.Bounds()
	w := tb.Dx()
	if bb.Dx() > w {
		w = bb.Dx()
	}
	h := tb.Dy() + gap + bb.Dy()
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(dst, dst.Bounds(), image.NewUniform(bg), image.Point{}, draw.Src)
	draw.Draw(dst, image.Rect((w-tb.Dx())/2, 0, (w-tb.Dx())/2+tb.Dx(), tb.Dy()), top, tb.Min, draw.Over)
	by := tb.Dy() + gap
	draw.Draw(dst, image.Rect((w-bb.Dx())/2, by, (w-bb.Dx())/2+bb.Dx(), by+bb.Dy()), bottom, bb.Min, draw.Over)
	return dst
}

// termSkeletonJS injects the terminal styling + chrome into about:blank. Colors
// mirror a dark terminal running Claude Code: muted body text, an accent dot on
// action lines, red/green on Edit diff lines.
const termSkeletonJS = `(() => {
  document.documentElement.style.cssText = 'margin:0;padding:0;';
  document.head.innerHTML = '<style>' +
    'html,body{margin:0;padding:0;background:#16181d;}' +
    '*{box-sizing:border-box;}' +
    '.term{font-family:"SFMono-Regular",Menlo,Consolas,"Liberation Mono",monospace;' +
      'font-size:15px;line-height:1.65;color:#c9d1d9;height:100vh;display:flex;flex-direction:column;}' +
    '.term-bar{display:flex;align-items:center;gap:8px;padding:9px 14px;background:#21262d;' +
      'border-bottom:1px solid #30363d;color:#8b949e;font-size:13px;}' +
    '.term-bar .dot{width:11px;height:11px;border-radius:50%;display:inline-block;}' +
    '.term-bar .r{background:#ff5f56;}.term-bar .y{background:#ffbd2e;}.term-bar .g{background:#27c93f;}' +
    '.term-bar .title{margin-left:6px;}' +
    '.term-body{padding:14px 18px;white-space:pre-wrap;word-break:break-word;flex:1;overflow:hidden;}' +
    '.ln{min-height:1.65em;}' +
    '.ln.prompt{color:#e6edf3;font-weight:600;}' +
    '.ln.dim{color:#8b949e;}' +
    '.ln.bullet{color:#e6edf3;}' +
    '.ln.bullet .bdot{color:#d97757;font-weight:700;}' +
    '.ln.del{color:#f85149;}' +
    '.ln.add{color:#3fb950;}' +
    '.ln.plain{color:#c9d1d9;}' +
    '.ln.sp{min-height:.7em;}' +
  '</style>';
  document.body.innerHTML =
    '<div class="term">' +
      '<div class="term-bar"><span class="dot r"></span><span class="dot y"></span>' +
        '<span class="dot g"></span><span class="title">claude — prereview</span></div>' +
      '<div class="term-body" id="term-body"></div>' +
    '</div>';
})()`

// termRenderJS rebuilds the scrollback from a JSON array of {t,k}. textContent
// keeps text escaped; bullet lines get a colored "●" dot prepended via a span.
const termRenderJS = `(() => {
  const lines = %s;
  const b = document.getElementById('term-body');
  b.innerHTML = '';
  for (const l of lines) {
    const d = document.createElement('div');
    d.className = 'ln ' + l.k;
    if (l.k === 'bullet') {
      const dot = document.createElement('span');
      dot.className = 'bdot';
      dot.textContent = '● ';
      d.appendChild(dot);
      d.appendChild(document.createTextNode(l.t));
    } else {
      d.textContent = l.t;
    }
    b.appendChild(d);
  }
})()`
