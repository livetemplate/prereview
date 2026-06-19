// Mermaid diagram renderer for prereview. This is the only client-rendered
// element in an otherwise server-rendered markdown pipeline: a graph
// definition can't be laid out in Go, only by mermaid.js in the browser. The
// server emits each ```mermaid fence as `.md-mermaid > pre.mermaid`
// (lvt-ignore, so morphdom leaves the injected SVG alone). mermaid.min.js is
// fetched lazily — only when a page actually contains a diagram — and kept off
// every other view.
//
// Served as a standalone file (not inlined in prereview.tmpl) so it stays out
// of livetemplate's template tree: a large inline <script> would be diffed as
// template content on every render.
(function () {
  var idSeq = 0;
  var mermaidPromise = null;

  // Lazy-load + initialise mermaid exactly once. securityLevel 'strict' and
  // htmlLabels:false are deliberate: this is the one place we run JS over
  // untrusted repo content, so we sanitise labels and disable click handlers,
  // matching the rest of the pipeline's refusal to pass raw HTML through.
  // suppressErrorRendering stops mermaid drawing its own error graphic — we
  // render the chosen fallback (raw code + banner) ourselves.
  function loadMermaid() {
    if (mermaidPromise) return mermaidPromise;
    mermaidPromise = new Promise(function (resolve, reject) {
      var s = document.createElement('script');
      s.src = '/mermaid.min.js';
      s.onload = function () {
        try {
          window.mermaid.initialize({
            startOnLoad: false,
            securityLevel: 'strict',
            suppressErrorRendering: true,
            flowchart: { htmlLabels: false }
          });
          resolve(window.mermaid);
        } catch (e) { reject(e); }
      };
      s.onerror = function () { reject(new Error('failed to load /mermaid.min.js')); };
      document.head.appendChild(s);
    });
    return mermaidPromise;
  }

  // Failure fallback chosen for review: keep the raw definition visible and
  // commentable (the <pre> is revealed via .md-mermaid-failed) and prepend a
  // one-line banner so the reviewer knows it was meant to be a diagram.
  function showFallback(pre) {
    var box = pre.closest('.md-mermaid') || pre.parentElement;
    if (!box || box.querySelector('.md-mermaid-error')) return;
    box.classList.add('md-mermaid-failed');
    var banner = document.createElement('div');
    banner.className = 'md-mermaid-error';
    banner.textContent = '⚠ diagram failed to render';
    box.insertBefore(banner, box.firstChild);
  }

  function renderOne(mermaid, pre) {
    var def = pre.textContent;
    var id = 'mmd-' + (idSeq++);
    var done;
    try {
      done = mermaid.render(id, def);
    } catch (e) { showFallback(pre); return; }
    Promise.resolve(done).then(function (out) {
      var host = document.createElement('div');
      host.className = 'md-mermaid-svg';
      // out.svg is sanitised by mermaid itself under securityLevel:'strict'
      // (HTML labels stripped, no event handlers) — that strict-mode
      // sanitisation is why this innerHTML assignment is safe and why we don't
      // pull in a separate sanitiser dependency.
      host.innerHTML = out.svg;
      pre.replaceWith(host);
    }).catch(function () { showFallback(pre); });
  }

  // Render every not-yet-processed diagram. data-mmd-claimed makes the sweep
  // idempotent: the initial SSR sweep and each post-navigation lvt:updated
  // sweep can both run without double-rendering a diagram.
  function sweep() {
    var pending = document.querySelectorAll('pre.mermaid:not([data-mmd-claimed])');
    if (!pending.length) return;
    pending.forEach(function (pre) { pre.setAttribute('data-mmd-claimed', ''); });
    loadMermaid().then(function (mermaid) {
      pending.forEach(function (pre) { renderOne(mermaid, pre); });
    }).catch(function () {
      pending.forEach(function (pre) { showFallback(pre); });
    });
  }

  // prereview is a single-page app: it swaps files without a full reload, so a
  // one-shot DOMContentLoaded would only catch diagrams in the first view.
  // livetemplate fires lvt:updated on its wrapper after every DOM patch;
  // capture-phase catches it even though the event doesn't bubble. The initial
  // sweep handles diagrams already present in the server-rendered first paint.
  document.addEventListener('lvt:updated', sweep, true);
  sweep();
})();
