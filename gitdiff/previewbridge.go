package gitdiff

// previewBridgeJS is injected into every HTML-preview srcdoc (see headInject in
// htmlpreview.go). The preview iframe is an opaque-origin sandbox
// (sandbox="allow-scripts", no allow-same-origin), so the page's OWN scripts run
// — e.g. the Tailwind Play CDN that generates the page's CSS at runtime — but
// can never reach the parent app. The flip side: the parent can no longer read
// the iframe's contentDocument. This beacon is the bridge: it posts the document
// height (for auto-sizing the iframe) and the [data-from] block rects (for
// region-select hit-testing) OUT to the parent via postMessage.
//
// The parent (livetemplate client's lvt-fx:preview-bridge directive) trusts a
// message only when event.source is this iframe's own contentWindow and the
// payload carries __lvtPreview:true, so posting with targetOrigin '*' is safe
// (an opaque srcdoc has no resolvable origin to target anyway).
//
// box() measures a block that has no box of its own — display:contents, which
// livetemplate's data-lvt-id root and many app shells use for the one element
// wrapping the page — as the union of its children's boxes (recursively), so
// region-select still has something to hit. A block with nothing visible inside
// is left out rather than posted as a 0×0 rect.
//
// Every visible block is posted; a pure WRAPPER (an annotated element whose
// content is all in annotated descendants — no text of its own) carries
// wrap:true. Every element has its own range (RenderHTMLPreview), and the
// client (lineRangeFromBlocks, livetemplate/client#165) unions only the content
// blocks a drawn box touches — exactly the lines it covers — and falls back to
// the smallest wrapper when the box hits only empty space (padding, gaps). An invisible
// block (visibility:hidden, or under an opacity:0 ancestor — e.g. a closed
// full-screen backdrop) is left out too: its box would join every drag. Posts are
// coalesced (one per 32ms): scroll fires per frame and each post measures every
// block. A timer, not requestAnimationFrame, which Chrome throttles in an
// offscreen cross-origin iframe like this one.
//
// Mirrors internal/proxy/proxy.go's proxyBeaconJS (the --external live-site
// bridge): same {__lvt…:true,type,…} envelope and the re-announce-on-delays
// trick — the parent directive attaches only after the deferred client connects
// and postMessage is not queued for listeners that attach after a post.
//
// The first line sets --prereview-vh, the basis rewriteViewportUnits pins the
// page's vh units to: 1% of the screen height, set before the page lays out.
//
// height() is the backstop for growth the rewrite can't see: runtime CSS (the
// Tailwind CDN's h-screen, a script setting 100vh), linked stylesheets, or an
// html/body height:100% chain. Because the parent sets the frame to the height
// posted, such a page grows by at least as much as the frame did on every
// resize, and never settles. When that happens twice in a row (one late image
// can coincide once), height() holds the last height it posted. The page is
// then clipped at that height, but the frame stays finite.
//
// It is injected as a literal <script> string into the srcdoc, then escaped by
// html/template's attribute autoescaper when placed in srcdoc="…" (same path as
// the <base>/<style> in headInject); the browser reconstitutes and runs it.
const previewBridgeJS = `<script>(function(){
try{document.documentElement.style.setProperty('--prereview-vh',((screen.availHeight||900)/100)+'px');}catch(e){}
function box(el){var r=el.getBoundingClientRect();if(r.width||r.height)return{left:r.left,top:r.top,right:r.right,bottom:r.bottom};var u=null,c=el.children;for(var i=0;i<c.length;i++){var b=box(c[i]);if(!b)continue;u=u?{left:Math.min(u.left,b.left),top:Math.min(u.top,b.top),right:Math.max(u.right,b.right),bottom:Math.max(u.bottom,b.bottom)}:b;}return u;}
function faded(a,m){if(!a||a===document.documentElement)return false;if(m.has(a))return m.get(a);var v=getComputedStyle(a).opacity==='0'||faded(a.parentElement,m);m.set(a,v);return v;}
function ownText(el){for(var n=el.firstChild;n;n=n.nextSibling)if(n.nodeType===3&&/\S/.test(n.data))return true;return false;}
function rects(){var o=[],e=document.querySelectorAll('[data-from]'),wrap=new Set(),i,el,p;for(i=0;i<e.length;i++){p=e[i].parentElement;p=p&&p.closest('[data-from]');if(p)wrap.add(p);}var m=new Map();for(i=0;i<e.length;i++){el=e[i];if(getComputedStyle(el).visibility==='hidden'||faded(el,m))continue;var r=box(el),f=parseInt(el.getAttribute('data-from'),10),t=parseInt(el.getAttribute('data-to'),10);if(!(f>0)||!r)continue;var b={from:f,to:(t>=f?t:f),left:r.left,top:r.top,right:r.right,bottom:r.bottom};if(wrap.has(el)&&!ownText(el))b.wrap=true;o.push(b);}return o;}
var fh=0,ph=0,grew=0,held=0;
function height(){var h=document.documentElement.scrollHeight,f=innerHeight;if(held)return held;if(fh&&f>fh&&h-ph>=0.9*(f-fh)){if(++grew>=2){held=ph;return held;}}else if(f!==fh)grew=0;fh=f;ph=h;return h;}
function send(){queued=false;try{parent.postMessage({__lvtPreview:true,type:'size',height:height()},'*');parent.postMessage({__lvtPreview:true,type:'blocks',blocks:rects()},'*');}catch(e){}}
var queued=false;function post(){if(queued)return;queued=true;setTimeout(send,32);}
if(document.readyState!=='loading')post();else document.addEventListener('DOMContentLoaded',post);
addEventListener('load',post);addEventListener('scroll',post,true);
try{new ResizeObserver(post).observe(document.documentElement);}catch(e){}
[0,250,700,1500,3000].forEach(function(d){setTimeout(post,d);});
})();</script>`
