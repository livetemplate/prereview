// Draws the diagrams gitdiff.MermaidKeys emits; its doc has the markup and
// keying contract.
import type { DeclarativeHandler, ElementContext } from "@livetemplate/client";

interface Mermaid {
  initialize(config: Record<string, unknown>): void;
  render(id: string, definition: string): Promise<{ svg: string }>;
}

declare global {
  interface Window {
    mermaid?: Mermaid;
  }
}

let mermaidPromise: Promise<Mermaid> | null = null;
let idSeq = 0;

// mermaid.min.js is 3MB, so it is fetched only when a page has a diagram.
// securityLevel 'strict' and htmlLabels:false sanitise labels and disable click
// handlers: this is the one place prereview runs JS over repo content.
// suppressErrorRendering leaves the fallback to showFallback.
//
// A rejection clears the cache, so the next diagram armed tries the load again
// instead of every later diagram inheriting one network error.
function loadMermaid(): Promise<Mermaid> {
  if (mermaidPromise) return mermaidPromise;
  mermaidPromise = new Promise<Mermaid>((resolve, reject) => {
    const s = document.createElement("script");
    s.src = "/mermaid.min.js";
    s.onload = () => {
      try {
        const mermaid = window.mermaid!;
        mermaid.initialize({
          startOnLoad: false,
          securityLevel: "strict",
          suppressErrorRendering: true,
          flowchart: { htmlLabels: false },
        });
        resolve(mermaid);
      } catch (e) {
        reject(e);
      }
    };
    s.onerror = () => reject(new Error("failed to load /mermaid.min.js"));
    document.head.appendChild(s);
  });
  mermaidPromise.catch(() => {
    mermaidPromise = null;
  });
  return mermaidPromise;
}

// Keeps the definition visible and commentable (.md-mermaid-failed reveals the
// <pre>) under a one-line banner saying it was meant to be a diagram.
function showFallback(box: Element, err: unknown): void {
  console.warn(`prereview: mermaid diagram ${box.getAttribute("data-key")} failed to render`, err);
  box.classList.add("md-mermaid-failed");
  const banner = document.createElement("div");
  banner.className = "md-mermaid-error";
  banner.textContent = "⚠ diagram failed to render";
  box.insertBefore(banner, box.firstChild);
}

async function render(box: Element, ctx: ElementContext): Promise<void> {
  const pre = box.querySelector("pre.mermaid");
  if (!pre) return;
  try {
    const mermaid = await loadMermaid();
    if (ctx.signal.aborted) return;
    const { svg } = await mermaid.render(`mmd-svg-${idSeq++}`, pre.textContent ?? "");
    if (ctx.signal.aborted) return;
    const host = document.createElement("div");
    host.className = "md-mermaid-svg";
    // Safe: mermaid sanitises its own output under securityLevel 'strict'.
    host.innerHTML = svg;
    pre.replaceWith(host);
  } catch (err) {
    if (!ctx.signal.aborted) showFallback(box, err);
  }
}

export const mermaidHandler: DeclarativeHandler = {
  attribute: "lvt-x:mermaid",
  onElementAdded: (el, ctx) => {
    void render(el, ctx);
  },
};
