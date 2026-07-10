//go:build browser

package e2e

import (
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// TestE2E_DarkNoLowContrast is the comprehensive regression guard for #124: in
// dark mode, EVERY visible text element must clear a readable WCAG contrast against
// its effective background. It renders a rich UI state (comment card + actions,
// open composer, queue dropdown, file/folder tree, and the search modal with a
// file match) and walks every text node, asserting zero elements below ~3:1 —
// across all three schemes. Catches the whole "invisible / dark-on-dark" class.
func TestE2E_DarkNoLowContrast(t *testing.T) {
	p := bootChromeAgainstPrereview(t, 1400, 900, "--agent")
	p.waitReadyAt(1400, 900)
	p.clickFile("edited.go")

	p.clickLine(3, 3)
	_ = chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.composer textarea`, chromedp.ByQuery),
		chromedp.SendKeys(`.composer textarea`, "a review comment", chromedp.ByQuery),
		chromedp.Click(`button[name='addComment']`, chromedp.ByQuery),
		chromedp.WaitVisible(`.inline-comment`, chromedp.ByQuery),
	)
	p.clickLine(0, 4) // second composer open
	_ = chromedp.Run(p.ctx, chromedp.WaitVisible(`.composer textarea`, chromedp.ByQuery))
	_ = chromedp.Run(p.ctx, chromedp.Click(`.queue-dropdown .queue-trigger`, chromedp.ByQuery), chromedp.Sleep(200*time.Millisecond))
	// Open search with a file match (exercises the .search-hit file-name code, F1).
	_ = chromedp.Run(p.ctx,
		chromedp.Click(`header.bar button[name="openSearch"]`, chromedp.ByQuery),
		chromedp.Sleep(150*time.Millisecond),
		chromedp.SendKeys(`.search-modal input, input[name="q"], .search-body input`, "edited", chromedp.ByQuery),
		chromedp.Sleep(400*time.Millisecond),
	)

	for i := 0; i < 2; i++ { // System → Light → Dark
		_ = chromedp.Run(p.ctx, chromedp.Evaluate(`document.querySelector('header.bar button[name="cycleTheme"]').click()`, nil))
		time.Sleep(400 * time.Millisecond)
	}

	walk := `(() => {
	  const lum = (c) => {
	    const m = c.match(/\d+(\.\d+)?/g); if(!m) return null;
	    let [r,g,b,a] = m.map(Number);
	    // getComputedStyle returns rgb()/rgba() as 0-255, but color-mix() results
	    // come back as color(srgb 0..1 ...) — scale those floats to 0-255 so the
	    // WCAG math below (which divides by 255) is correct for both. Without this
	    // a color(srgb ...) fg reads as near-black and reports a false low ratio.
	    if (/^color\(/.test(c)) { r*=255; g*=255; b*=255; }
	    if (a===0) return null;
	    const f = (v)=>{v/=255; return v<=0.03928 ? v/12.92 : Math.pow((v+0.055)/1.055,2.4);};
	    return 0.2126*f(r)+0.7152*f(g)+0.0722*f(b);
	  };
	  const opaque = (c) => { const m=c.match(/[\d.]+/g); return m && (m.length<4 || Number(m[3])>0.5); };
	  const effBg = (el) => { let n=el; while(n && n!==document.documentElement){ const bg=getComputedStyle(n).backgroundColor; if(opaque(bg)) return bg; n=n.parentElement; } return getComputedStyle(document.body).backgroundColor; };
	  const hasText = (el) => [...el.childNodes].some(n=>n.nodeType===3 && n.textContent.trim().length>1);
	  const out=[]; const seen=new Set();
	  for(const el of document.querySelectorAll('body *')){
	    const s=getComputedStyle(el);
	    if(s.visibility==='hidden'||s.display==='none'||Number(s.opacity)===0) continue;
	    const r=el.getBoundingClientRect(); if(r.width<2||r.height<2) continue;
	    if(!hasText(el)) continue;
	    const l1=lum(s.color), l2=lum(effBg(el)); if(l1===null||l2===null) continue;
	    const ratio=(Math.max(l1,l2)+0.05)/(Math.min(l1,l2)+0.05);
	    if(ratio<3.0){ const k=(el.className||'')+'|'+(el.textContent||'').trim().slice(0,20); if(!seen.has(k)){seen.add(k); out.push({t:(el.textContent||'').trim().slice(0,24),cls:(el.className||'').toString().slice(0,40),fg:s.color,bg:effBg(el),ratio:Math.round(ratio*100)/100});} }
	  }
	  return JSON.stringify(out.sort((a,b)=>a.ratio-b.ratio));
	})()`

	scheme := func() string {
		var v string
		_ = chromedp.Run(p.ctx, chromedp.Evaluate(`(document.querySelector('.theme-root')?.getAttribute('data-scheme'))||''`, &v))
		return v
	}
	for i := 0; i < 3; i++ {
		var report string
		_ = chromedp.Run(p.ctx, chromedp.Evaluate(walk, &report))
		if report != "[]" {
			t.Errorf("[scheme=%s dark] low-contrast text (ratio<3.0): %s", scheme(), report)
		}
		_ = chromedp.Run(p.ctx, chromedp.Evaluate(`document.querySelector('header.bar button[name="cycleScheme"]').click()`, nil), chromedp.Sleep(400*time.Millisecond))
	}
}
