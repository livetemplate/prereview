/**
 * lvt-fx:resize — make an element edge-draggable to resize, updating a CSS
 * custom property live and (optionally) persisting it to localStorage. Pure
 * client-side: no server round-trip. Modeled on the area-select directive's
 * pointer-capture + arm/sweep/cleanup lifecycle.
 *
 * Usage:
 *   <aside lvt-fx:resize="--pr-drawer-w"
 *          data-resize-handle=".resize-handle"   // selector for the grab handle
 *          data-resize-min="180" data-resize-max="560"  // px clamp
 *          data-resize-axis="x"                   // x (default) | y
 *          data-resize-edge="end"                 // end (default) | start
 *          data-resize-store="prereview.drawerW"> // localStorage key (optional)
 *     …
 *     <div class="resize-handle"></div>
 *   </aside>
 *
 * The handle element needs `touch-action: none` in CSS — onPointerMove calls
 * preventDefault(), so without it the drag silently interferes with page scroll
 * on touch devices (iOS/Android).
 *
 * The CSS variable is set on document.documentElement (:root) rather than the
 * host so it survives morphdom re-renders — the host element is inside the
 * livetemplate-managed subtree and its inline style would be diffed away, but
 * :root sits outside it and is never patched. The stylesheet reads the var,
 * e.g. `width: var(--pr-drawer-w)`.
 */

interface ResizeEntry {
  cleanup: () => void;
}

const resizeArmed = new WeakMap<Element, ResizeEntry>();
// Tracked so teardownResizeForRoot can sweep without a live DOM query.
const resizeElements = new Set<Element>();
// varName -> the host that currently owns that :root custom property. Only the
// owner clears the property on cleanup, so two hosts sharing a var name don't
// nuke it for each other; a conflict is warned about at arm time.
const armedVars = new Map<string, Element>();

function clamp(v: number, min: number, max: number): number {
  return Math.min(max, Math.max(min, v));
}

function readNumberAttr(el: Element, name: string, fallback: number): number {
  const raw = el.getAttribute(name);
  if (raw == null || raw.trim() === "") return fallback;
  const n = parseFloat(raw);
  return Number.isFinite(n) ? n : fallback;
}

/**
 * Set the CSS variable on :root. Kept on the document element so it is never
 * touched by the morphdom pass that patches the livetemplate wrapper subtree.
 */
function setVar(varName: string, px: number): void {
  document.documentElement.style.setProperty(varName, `${px}px`);
}

function attachResize(host: HTMLElement, varName: string): ResizeEntry {
  const handleSel = host.getAttribute("data-resize-handle");
  let handle: HTMLElement = host;
  if (handleSel) {
    const found = host.querySelector<HTMLElement>(handleSel);
    if (found) {
      handle = found;
    } else {
      // Selector set but matched nothing — almost certainly a template typo.
      // Falling back to the whole host as the drag target is surprising, so warn.
      console.warn(
        `lvt-fx:resize: data-resize-handle "${handleSel}" matched no element; using the host as the drag target.`
      );
    }
  }
  const axis = (host.getAttribute("data-resize-axis") || "x").toLowerCase();
  const edge = (host.getAttribute("data-resize-edge") || "end").toLowerCase();
  const min = readNumberAttr(host, "data-resize-min", 0);
  const max = readNumberAttr(host, "data-resize-max", Number.MAX_SAFE_INTEGER);
  const storeKey = host.getAttribute("data-resize-store");

  // Restore a persisted width once on arm. Applying it to :root before the
  // first paint keeps the drawer at the user's chosen width without a flash.
  if (storeKey) {
    try {
      const saved = window.localStorage.getItem(storeKey);
      if (saved != null) {
        const px = parseFloat(saved);
        if (Number.isFinite(px)) setVar(varName, clamp(px, min, max));
      }
    } catch {
      /* localStorage may be unavailable (private mode / disabled) — ignore. */
    }
  }

  let startPos = 0;
  let startSize = 0;
  let activePointer: number | null = null;

  const onPointerMove = (e: PointerEvent) => {
    if (activePointer === null || e.pointerId !== activePointer) return;
    const pos = axis === "y" ? e.clientY : e.clientX;
    // "start" edge grows when dragging toward the origin; "end" edge grows
    // when dragging away from it.
    const delta = edge === "start" ? startPos - pos : pos - startPos;
    setVar(varName, clamp(startSize + delta, min, max));
    e.preventDefault();
  };

  const onPointerUp = (e: PointerEvent) => {
    if (activePointer === null || e.pointerId !== activePointer) return;
    activePointer = null;
    handle.removeEventListener("pointermove", onPointerMove);
    handle.removeEventListener("pointerup", onPointerUp);
    handle.removeEventListener("pointercancel", onPointerUp);
    try {
      handle.releasePointerCapture(e.pointerId);
    } catch {
      /* capture may already be lost (e.g. element removed mid-drag). */
    }
    host.removeAttribute("data-resizing");
    if (storeKey) {
      // Persist the value we actually wrote (the :root custom property), not
      // getComputedStyle(host) — the host's used width can differ from the var
      // (border-box, padding, calc(), flex), which would restore a wrong size.
      // This is also a single synchronous read, no forced layout.
      const px = parseFloat(
        document.documentElement.style.getPropertyValue(varName)
      );
      if (Number.isFinite(px)) {
        try {
          window.localStorage.setItem(storeKey, String(Math.round(px)));
        } catch {
          /* persistence is best-effort. */
        }
      }
    }
  };

  const onPointerDown = (e: PointerEvent) => {
    // Primary button / touch / pen only; ignore secondary buttons. Also ignore
    // a second pointerdown while a drag is in flight (two-finger / pen+touch),
    // which would otherwise clobber the active drag's start state.
    if (e.button !== 0 || activePointer !== null) return;
    activePointer = e.pointerId;
    startPos = axis === "y" ? e.clientY : e.clientX;
    // Anchor the drag to the current variable value (the logical size we
    // control), not the rendered box — under content-box + padding the rect
    // differs from the var and the first drag would jump. getComputedStyle on
    // :root resolves an inline override or the stylesheet default; fall back to
    // the rendered rect only when the var is unset.
    const fromVar = parseFloat(
      getComputedStyle(document.documentElement).getPropertyValue(varName)
    );
    const rect = host.getBoundingClientRect();
    startSize = Number.isFinite(fromVar)
      ? fromVar
      : axis === "y"
        ? rect.height
        : rect.width;
    try {
      handle.setPointerCapture(e.pointerId);
    } catch {
      /* capture is an optimization; dragging still works without it. */
    }
    host.setAttribute("data-resizing", "");
    handle.addEventListener("pointermove", onPointerMove);
    handle.addEventListener("pointerup", onPointerUp);
    handle.addEventListener("pointercancel", onPointerUp);
    e.preventDefault();
  };

  handle.addEventListener("pointerdown", onPointerDown);

  return {
    cleanup: () => {
      handle.removeEventListener("pointerdown", onPointerDown);
      handle.removeEventListener("pointermove", onPointerMove);
      handle.removeEventListener("pointerup", onPointerUp);
      handle.removeEventListener("pointercancel", onPointerUp);
      // Drop the :root override so a different element can't inherit this
      // one's width before its own attachResize runs — but only if we still
      // own the var, so a host sharing the name keeps it.
      if (armedVars.get(varName) === host) {
        document.documentElement.style.removeProperty(varName);
        armedVars.delete(varName);
      }
      resizeArmed.delete(host);
      resizeElements.delete(host);
    },
  };
}

/**
 * Arm every [lvt-fx:resize] element under root, and sweep entries whose element
 * was disconnected or had the attribute removed by a server diff. Idempotent:
 * re-running keeps already-armed elements (so an in-flight drag survives a
 * re-render) and only attaches newly-appeared ones.
 */
export function handleResizeDirectives(rootElement: Element): void {
  for (const element of Array.from(resizeElements)) {
    if (!element.isConnected || !element.hasAttribute("lvt-fx:resize")) {
      resizeArmed.get(element)?.cleanup();
    }
  }

  const matches = rootElement.querySelectorAll<HTMLElement>("[lvt-fx\\:resize]");
  for (const el of matches) {
    const varName = el.getAttribute("lvt-fx:resize");
    if (!varName) {
      console.warn(
        `lvt-fx:resize requires a CSS variable name, got: ${JSON.stringify(varName)}`
      );
      continue;
    }
    // Must be a custom property — otherwise we'd set a real CSS property like
    // `width` on :root, a surprising footgun.
    if (!varName.startsWith("--")) {
      console.warn(
        `lvt-fx:resize value must be a CSS custom property (--name), got: ${JSON.stringify(varName)}`
      );
      continue;
    }
    if (resizeArmed.has(el)) continue; // already armed — keep listeners
    const owner = armedVars.get(varName);
    if (owner && owner !== el && owner.isConnected) {
      console.warn(
        `lvt-fx:resize: "${varName}" is already controlled by another connected element; they will fight over it.`
      );
    }
    const entry = attachResize(el, varName);
    resizeArmed.set(el, entry);
    resizeElements.add(el);
    armedVars.set(varName, el);
  }
}

/** Cancel resize listeners for every armed element under root (disconnect/destroy). */
export function teardownResizeForRoot(root: Element): void {
  // Only sweep elements under THIS root. resizeElements is a module-level
  // singleton shared across clients, so cleaning up by !isConnected here would
  // let one client tear down nodes another client briefly detached mid-render.
  // Disconnected hosts are reclaimed by the handleResizeDirectives sweep.
  for (const element of Array.from(resizeElements)) {
    if (root.contains(element)) {
      resizeArmed.get(element)?.cleanup();
    }
  }
}
