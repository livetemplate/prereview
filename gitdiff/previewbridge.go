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
// Mirrors internal/proxy/proxy.go's proxyBeaconJS (the --external live-site
// bridge): same {__lvt…:true,type,…} envelope and the re-announce-on-delays
// trick — the parent directive attaches only after the deferred client connects
// and postMessage is not queued for listeners that attach after a post.
//
// It is injected as a literal <script> string into the srcdoc, then escaped by
// html/template's attribute autoescaper when placed in srcdoc="…" (same path as
// the <base>/<style> in headInject); the browser reconstitutes and runs it.
const previewBridgeJS = `<script>(function(){
function rects(){var o=[],e=document.querySelectorAll('[data-from]');for(var i=0;i<e.length;i++){var el=e[i],r=el.getBoundingClientRect(),f=parseInt(el.getAttribute('data-from'),10),t=parseInt(el.getAttribute('data-to'),10);if(!(f>0))continue;o.push({from:f,to:(t>=f?t:f),left:r.left,top:r.top,right:r.right,bottom:r.bottom});}return o;}
function post(){try{parent.postMessage({__lvtPreview:true,type:'size',height:document.documentElement.scrollHeight},'*');parent.postMessage({__lvtPreview:true,type:'blocks',blocks:rects()},'*');}catch(e){}}
if(document.readyState!=='loading')post();else document.addEventListener('DOMContentLoaded',post);
addEventListener('load',post);addEventListener('scroll',post,true);
try{new ResizeObserver(post).observe(document.documentElement);}catch(e){}
[0,250,700,1500,3000].forEach(function(d){setTimeout(post,d);});
})();</script>`
