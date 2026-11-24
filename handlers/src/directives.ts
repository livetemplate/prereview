// prereview's attribute handlers: area/region/text selection, the viewport
// report, the live-site and HTML-preview bridges, the URL hash mirror and
// auto-click. They lived in @livetemplate/client until #473 Phase 4 moved them
// here, their only consumer. index.ts registers them through the client's
// public API; nothing here imports the client at runtime.

// Active timers for `lvt-fx:auto-click="<delay-ms>:<button-name>"`. Stored
// keyed by element so we can detect: (a) element re-renders unchanged →
// don't re-arm, (b) spec changed → cancel + re-arm, (c) element removed →
// cancel. Strong Map (not WeakMap) so we can iterate for the disconnected-
// element sweep on each render pass.
//
// Leak management: cleanup of stale entries depends on render continuity —
// `handleAutoClickDirectives` runs the sweep on every render pass, so an
// element removed between renders is collected on the next one. If the
// page stops rendering entirely (e.g. websocket dies and is never
// reconnected) while elements still have timers, those entries hold
// strong refs until tear-down. In practice the timer's own fire-time
// `isConnected` guard makes any leftover harmless — it would skip the
// `.click()` for a disconnected node — but the Map itself remains.
const autoClickTimers = new Map<
  Element,
  { timer: ReturnType<typeof setTimeout>; spec: string }
>();

/**
 * Cancel and forget all active `lvt-fx:auto-click` timers. Called from
 * the LiveTemplate client's `disconnect()` so per-session timer state
 * doesn't survive across a session boundary (the next session re-arms
 * fresh on its first render pass). Safe to call when no timers exist.
 *
 * Multi-client scope warning: the timer Map is module-level (matching
 * the existing `animatedElements` / `scrollResetPriors` pattern), so
 * this teardown cancels timers across every `LiveTemplateClient`
 * instance on the page. If two clients coexist (e.g. a layout client
 * and a widget client), disconnect order matters — the surviving
 * client's pending auto-clicks are cancelled along with the
 * disconnecting client's. Surviving clients re-arm on their next
 * render pass, but any auto-click that was about to fire mid-window
 * is lost. Per-instance scoping would solve this but is a larger
 * refactor (the existing two singletons would need the same
 * treatment) and is deferred until a real multi-client use case
 * appears.
 */
export function teardownAutoClickTimers(): void {
  for (const { timer } of autoClickTimers.values()) clearTimeout(timer);
  autoClickTimers.clear();
}

/**
 * Apply auto-click directives. `lvt-fx:auto-click="<delay-ms>:<button-name>"`
 * arms a timer when the element is first seen with this spec; on fire, the
 * directive locates a descendant `[name=<button-name>]` and synthesizes a
 * click on it — funneling through the existing event-delegation pipeline
 * so the server-side action runs identically to a user click. Use case:
 * auto-dismiss a toast/banner after N ms by clicking its existing dismiss
 * button (which already fires the dismissBanner server action).
 *
 * Idempotent across renders: an element that re-appears with the same
 * spec keeps its existing timer. A spec change cancels and re-arms. An
 * element that disappears has its timer canceled on the next render's
 * sweep (and even if the sweep doesn't run first, the fire-time
 * isConnected check skips the click).
 */
export function handleAutoClickDirectives(rootElement: Element): void {
  // Fast path: nothing armed and no matching elements → no work to do.
  // `querySelector` returns on the first hit, so this is cheaper than
  // the `querySelectorAll` below when there are no matches at all
  // (the common case for pages that don't use this directive).
  if (
    autoClickTimers.size === 0 &&
    rootElement.querySelector("[lvt-fx\\:auto-click]") === null
  ) {
    return;
  }

  // Sweep: cancel timers for elements that have disconnected OR whose
  // attribute was cleared while they remain in the DOM (e.g. the server
  // resolved the toast's dismiss state without removing the element).
  // Without this, the Map grows unbounded across renders, and a stale
  // timer could fire `.click()` on a button whose owning element no
  // longer wants the auto-action.
  for (const [element, entry] of Array.from(autoClickTimers)) {
    if (
      !element.isConnected ||
      !element.hasAttribute("lvt-fx:auto-click")
    ) {
      clearTimeout(entry.timer);
      autoClickTimers.delete(element);
    }
  }

  rootElement.querySelectorAll("[lvt-fx\\:auto-click]").forEach((element) => {
    const spec = element.getAttribute("lvt-fx:auto-click");
    if (!spec) return;

    const existing = autoClickTimers.get(element);
    if (existing && existing.spec === spec) return;
    if (existing) clearTimeout(existing.timer);

    const colonIdx = spec.indexOf(":");
    const delayStr = colonIdx > 0 ? spec.slice(0, colonIdx) : "";
    // Pre-validate as a pure integer string: parseInt is lenient and
    // would accept "200abc" as 200, silently masking a typo in the
    // attribute. Authors expect "<delay>:<name>" — anything else warns.
    const delayMs = /^\d+$/.test(delayStr) ? parseInt(delayStr, 10) : NaN;
    const name = colonIdx > 0 ? spec.slice(colonIdx + 1) : "";
    // `delayMs === 0` is intentionally allowed: it means "click on the
    // next tick after this element first appears" — a useful primitive
    // for "auto-execute action on render" patterns. Authors who want
    // visible debounce should pass a non-zero value.
    //
    // Name is restricted to characters that cannot escape the CSS
    // attribute-selector interpolation below (no quotes, brackets,
    // whitespace, or backslashes). Word characters and hyphens cover
    // every valid HTML name attribute we expect to encounter — including
    // digit-prefixed names — while keeping the selector safe. JavaScript
    // `\w` is ASCII-only (`[A-Za-z0-9_]`), so a Unicode button name
    // would warn here; revisit if i18n button naming becomes a real
    // requirement.
    if (
      !Number.isFinite(delayMs) ||
      delayMs < 0 ||
      !name ||
      !/^[\w-]+$/.test(name)
    ) {
      console.warn(
        `lvt-fx:auto-click expects "<delay-ms>:<button-name>", got: ${spec}`
      );
      // Reached when an element's spec changes from valid to malformed
      // mid-life. The valid-spec branch above already cleared its
      // existing timer; this delete removes the now-stale map entry so
      // the next render doesn't see a phantom prior spec.
      autoClickTimers.delete(element);
      return;
    }

    const timer = setTimeout(() => {
      // Intentionally NOT deleting the map entry here. Doing so would
      // make the next render pass see "no entry, attribute still set"
      // and re-arm a fresh timer, firing `.click()` a second time —
      // reachable whenever a render lands between fire and the server
      // removing the element. Leave the entry in place; the next
      // sweep cleans it up when the element disconnects or the
      // attribute is cleared. The fired timeout itself is now a no-op
      // (clearTimeout on a fired handle is harmless).
      if (!element.isConnected) return;
      // Scoped to <button>: clicking an arbitrary [name=…] match (e.g.
      // a checkbox, a text input) would have surprising side effects
      // unrelated to the action-submission semantic this directive
      // promises. Buttons are the only correct target.
      const button = element.querySelector(
        `button[name="${name}"]`
      ) as HTMLElement | null;
      if (button) button.click();
    }, delayMs);
    autoClickTimers.set(element, { timer, spec });
  });
}

// areaSelectArmed tracks the cleanup callback for every element that
// currently has a `lvt-fx:area-select` handler attached. Map (not
// WeakMap) because the sweep needs to iterate to detect elements whose
// attribute was removed by a server diff — without iteration those
// elements would keep their listeners and silently dispatch the old
// action on subsequent drags. Detached elements are cleaned up via
// the same sweep (isConnected check).
const areaSelectArmed = new Map<Element, AreaSelectEntry>();

// areaSelectWarnedParents dedupes the "parent not positioned" dev-warn
// so a user who repeatedly drags on a mis-configured element gets a
// single console message instead of one per pointerdown. WeakSet so
// detached parents don't leak.
//
// Known limitation: once a parent is in the set, the warn never fires
// again on that DOM node — even if the developer subsequently adds
// `position: relative` to fix the issue. The WeakSet is per-object
// (different DOM node = different entry), so re-mounting the parent
// resets the dedupe; in-place CSS fixes do not. Fine in practice
// (the user already saw the warn once, on the broken render).
const areaSelectWarnedParents = new WeakSet<Element>();

interface AreaSelectEntry {
  action: string;
  cleanup: () => void;
  // updateSend lets the idempotent re-arm path swap the captured
  // send callback without tearing down + rebuilding listeners. The
  // listeners close over a mutable `send` variable inside
  // attachAreaSelect; updateSend reassigns it.
  updateSend: (send: AreaSelectSendFn) => void;
}

type AreaSelectSendFn = (
  message: { action: string; data: Record<string, unknown> }
) => void;

// MIN_AREA_FRACTION filters accidental click-style gestures where the
// user meant to click, not drag. 2% of the element's rendered size is
// big enough to be intentional on touch + mouse but small enough that
// anyone seriously trying to annotate a tiny region can still do it.
const MIN_AREA_FRACTION = 0.02;

/**
 * Apply area-select directives. `lvt-fx:area-select="<actionName>"` on
 * an element (typically an `<img>` inside a positioned parent) lets
 * the user drag a rectangle locally — a `<div>` overlay tracks the
 * gesture in real time without a server round-trip — and on
 * `pointerup` dispatches a single livetemplate action with the final
 * `{x, y, w, h}` as 0..1 fractions of the element's rendered bounding
 * rect. The image's intrinsic dimensions don't matter for the
 * fractions: any uniform scale (zoom, responsive layout) preserves
 * the fraction. The consumer scales to pixels using the natural size
 * if it needs them.
 *
 * Contract:
 *  - Host's `parentElement` must establish a positioning context
 *    (`position: relative` / `absolute` / `fixed`). The overlay is
 *    `position: absolute` inside that parent so it follows the host
 *    on scroll / reflow.
 *  - Consumers usually pair this with `touch-action: none` on the
 *    host so iOS Safari doesn't interpret the drag as a pinch/scroll.
 *  - `<img>` and other natively-draggable hosts work automatically:
 *    the directive calls `preventDefault()` on `dragstart` so the
 *    browser's native drag (which would otherwise steal the gesture)
 *    is suppressed.
 *  - On pointer-cancel (e.g. system gesture, app switch), the overlay
 *    is removed and no action is dispatched — same effect as cancelling
 *    a click on `mouseleave`.
 *  - Drags smaller than `MIN_AREA_FRACTION` in BOTH dimensions are
 *    dropped — a click on the host still fires normal `click`
 *    handlers via the compatibility mouse events.
 *  - For text-bearing hosts, set `user-select: none` (the directive
 *    deliberately does NOT call `preventDefault()` on `pointerdown`
 *    so click handlers still receive the gesture; that means the
 *    browser's default text-selection-on-drag behaviour also fires
 *    unless the host opts out via CSS).
 *  - The overlay uses `z-index: var(--lvt-area-select-z-index, 9999)`.
 *    9999 is high enough for most use cases but can collide with
 *    portals / modals / drawers that also sit at a high z-index.
 *    Set `--lvt-area-select-z-index` on the host (or any ancestor)
 *    to override. Color + fill follow the same pattern via
 *    `--lvt-area-select-color` and `--lvt-area-select-fill`.
 *  - **No keyboard equivalent.** Pointer-only by design (a keyboard-
 *    selected rectangle requires a different UX — focus + arrow keys
 *    to position + arrow keys to size). Consumers needing a11y for
 *    area selection should provide a parallel form-based affordance.
 *
 * Idempotent across renders: an element re-armed with the same action
 * keeps its existing listeners. A different action causes a tear-down
 * and re-arm. Disconnected elements (and elements whose attribute was
 * cleared by a server diff) get their listeners cleaned up by the
 * sweep at the top of every call — we use a regular Map (not WeakMap)
 * specifically so the sweep can iterate.
 *
 * Module-level singleton: `areaSelectArmed` is shared across all
 * LiveTemplateClient instances in the same window. If two clients
 * ever arm the same element with different actions, the second wins
 * and the first client's send() is orphaned. Single-client use is
 * unaffected.
 */
export function handleAreaSelectDirectives(
  rootElement: Element,
  send: (message: { action: string; data: Record<string, unknown> }) => void
): void {
  // Sweep stale entries before processing the current match set:
  // disconnected elements AND elements where the attribute was
  // removed by a server diff. Without this, a previously-armed
  // element whose lvt-fx:area-select was cleared would keep its
  // listeners and silently dispatch the old action on subsequent
  // drags. Iterate via Array.from so cleanup()'s delete() doesn't
  // disturb the iterator.
  for (const [element, entry] of Array.from(areaSelectArmed)) {
    if (!element.isConnected || !element.hasAttribute("lvt-fx:area-select")) {
      entry.cleanup();
    }
  }

  const matches = rootElement.querySelectorAll<HTMLElement>(
    "[lvt-fx\\:area-select]"
  );
  if (matches.length === 0) return;

  for (const el of matches) {
    const action = el.getAttribute("lvt-fx:area-select");
    // Empty attribute → consumer almost certainly typoed; warn and
    // skip rather than dispatching to a blank action name.
    if (!action) {
      console.warn(
        `lvt-fx:area-select requires an action name, got: ${JSON.stringify(action)}`
      );
      continue;
    }
    const existing = areaSelectArmed.get(el);
    if (existing && existing.action === action) {
      // Idempotent re-arm: keep the listeners + WeakMap entry, but
      // update the captured send so a subsequent drag dispatches
      // through the latest callback (e.g. after a WebSocket
      // reconnect rebuilt the transport).
      existing.updateSend(send);
      continue;
    }
    if (existing) existing.cleanup();
    areaSelectArmed.set(el, attachAreaSelect(el, action, send));
  }
}

/**
 * Cancel area-select listeners for every armed element under root.
 * Mirrors teardownAutoClickTimers: meant for the client's disconnect /
 * destroy lifecycle so the module-level singleton doesn't outlive a
 * client that was torn down without a subsequent
 * handleAreaSelectDirectives call (e.g. network error closed the
 * socket while an element was armed). Without this, a SPA that mounts
 * + tears down livetemplate trees would leak listeners across mounts.
 */
export function teardownAreaSelectForRoot(rootElement: Element): void {
  // `contains` returns true for the node itself, so this also handles
  // the (today-impossible) case of rootElement being armed directly.
  for (const [element, entry] of Array.from(areaSelectArmed)) {
    if (rootElement.contains(element)) {
      entry.cleanup();
    }
  }
}

// PreviewBlock is one rendered-HTML block's source line range plus its rect in
// the PREVIEW IFRAME's own viewport coordinates, as posted out by the in-iframe
// bridge beacon (previewBridgeJS, prereview/gitdiff/previewbridge.go). The
// preview iframe is an opaque-origin sandbox (`sandbox="allow-scripts"`) so it
// can run the page's own scripts (e.g. the Tailwind Play CDN that generates the
// CSS) without ever reaching the parent — which also means the parent can no
// longer read its `contentDocument`. region-select reads these posted rects
// instead of the iframe DOM.
//
// Contract with the beacon (keep in sync):
//   - from/to are 1-BASED source line numbers (the iframe's data-from/data-to);
//     attachPreviewBridge drops any block with from <= 0.
//   - left/top/right/bottom are VIEWPORT-relative (getBoundingClientRect), so
//     lineRangeFromBlocks shifts them by the iframe element's own viewport
//     offset — NOT document/scroll-relative coords.
//   - wrap (optional) marks a block whose content is all in other posted
//     blocks (a page wrapper). lineRangeFromBlocks uses wrappers only when a
//     box hits no content block, so a drag over empty space between content
//     still lands somewhere — the smallest wrapper around it.
export type PreviewBlock = {
  from: number;
  to: number;
  left: number;
  top: number;
  right: number;
  bottom: number;
  wrap?: boolean;
};

// previewBlockRects caches the latest blocks posted by each preview iframe's
// bridge, keyed per iframe. A Map (not WeakMap) lets the directive sweep clear
// a stale entry when its iframe is detached/replaced (a file switch re-creates
// the iframe via its data-key). Multiple previews never coexist today, but
// keying per iframe keeps region-select correct if they ever do.
const previewBlockRects = new Map<HTMLIFrameElement, PreviewBlock[]>();

// previewBridgeArmed tracks armed `lvt-fx:preview-bridge` <iframe> hosts.
// Map-not-WeakMap for the same sweep reasoning as areaSelectArmed.
const previewBridgeArmed = new Map<Element, PreviewBridgeEntry>();

interface PreviewBridgeEntry {
  cleanup: () => void;
  // sync re-applies the last posted height after every server render, because
  // morphdom diffs the iframe against the template (which has no inline style)
  // and would otherwise wipe the imperatively-set height.
  sync: () => void;
}

/**
 * Apply `lvt-fx:preview-bridge` directives. prereview's HTML preview is an
 * opaque-origin `sandbox="allow-scripts"` frame (so a previewed page's own JS
 * runs but can never touch the parent app), and the parent therefore can't read
 * `contentDocument`. The in-iframe beacon posts two message types out:
 *
 *   - `{type:"size", height}`  → set the iframe height (auto-height).
 *   - `{type:"blocks", blocks}` → cache the `[data-from]` block rects so the
 *     parent-document region-select overlay can hit-test a drawn box without
 *     reading the iframe DOM (see resolveRegion's "html" branch).
 *
 * Idempotent, and swept like area-select.
 */
export function handlePreviewBridgeDirectives(rootElement: Element): void {
  for (const [element, entry] of Array.from(previewBridgeArmed)) {
    if (!element.isConnected || !element.hasAttribute("lvt-fx:preview-bridge")) {
      entry.cleanup();
    }
  }
  const matches = rootElement.querySelectorAll<HTMLIFrameElement>(
    "iframe[lvt-fx\\:preview-bridge]"
  );
  for (const el of matches) {
    const existing = previewBridgeArmed.get(el);
    if (existing) {
      existing.sync();
      continue;
    }
    previewBridgeArmed.set(el, attachPreviewBridge(el));
  }
}

/**
 * Cancel preview-bridge listeners for every armed iframe under root, for the
 * client disconnect lifecycle.
 */
export function teardownPreviewBridgeForRoot(rootElement: Element): void {
  for (const [element, entry] of Array.from(previewBridgeArmed)) {
    if (rootElement.contains(element)) {
      entry.cleanup();
    }
  }
}

function attachPreviewBridge(iframe: HTMLIFrameElement): PreviewBridgeEntry {
  let lastHeight = 0;
  const applyHeight = () => {
    if (lastHeight > 0) iframe.style.height = `${lastHeight}px`;
  };
  // placeMarkers puts each commented block's box (the stage's
  // `.preview-pins .area-overlay[data-from]`) over the posted rect with the same
  // source range — the outermost one when a wrapper shares its child's range.
  // The preview has no scroll of its own (it is auto-heighted), so the posted
  // viewport rects are its document rects; the iframe's offset places them in
  // the stage. Inline styles are what morphdom wipes on every render, so sync()
  // re-applies them like the height.
  const placeMarkers = () => {
    const layer = iframe.parentElement?.querySelector<HTMLElement>(".preview-pins");
    if (!layer) return;
    const area = (b: PreviewBlock) => (b.right - b.left) * (b.bottom - b.top);
    const byRange = new Map<string, PreviewBlock>();
    for (const b of previewBlockRects.get(iframe) ?? []) {
      const key = `${b.from}-${b.to}`;
      const seen = byRange.get(key);
      if (!seen || area(b) > area(seen)) byRange.set(key, b);
    }
    // Read the iframe's offset once, before any write: reading it after a
    // style write would force a layout per marker.
    const ox = iframe.offsetLeft;
    const oy = iframe.offsetTop;
    for (const box of layer.querySelectorAll<HTMLElement>(".area-overlay[data-from]")) {
      const best = byRange.get(`${box.dataset.from}-${box.dataset.to}`);
      if (!best) {
        box.style.visibility = "hidden";
        continue;
      }
      box.style.left = `${ox + best.left}px`;
      box.style.top = `${oy + best.top}px`;
      box.style.width = `${best.right - best.left}px`;
      box.style.height = `${best.bottom - best.top}px`;
      box.style.visibility = "visible";
    }
  };

  const onMessage = (e: MessageEvent) => {
    // The preview iframe is an opaque-origin srcdoc, so e.origin is "null" and
    // there is no iframe.src to resolve a trusted origin from. Authenticate by
    // SOURCE WINDOW IDENTITY instead: only this iframe's own contentWindow.
    // Everything else (including the page's own postMessage traffic to other
    // targets) is ignored.
    if (e.source !== iframe.contentWindow) return;
    const d = e.data;
    if (!d || d.__lvtPreview !== true) return;

    if (d.type === "size") {
      const h = Number(d.height);
      if (Number.isFinite(h) && h > 0) {
        lastHeight = h;
        applyHeight();
      }
      return;
    }
    if (d.type === "blocks") {
      const raw = Array.isArray(d.blocks) ? d.blocks : [];
      const clean: PreviewBlock[] = [];
      for (const b of raw) {
        const from = Number(b?.from);
        const to = Number(b?.to);
        const left = Number(b?.left);
        const top = Number(b?.top);
        const right = Number(b?.right);
        const bottom = Number(b?.bottom);
        if (
          ![from, to, left, top, right, bottom].every((n) =>
            Number.isFinite(n)
          ) ||
          from <= 0
        ) {
          continue;
        }
        const block: PreviewBlock = { from, to: to >= from ? to : from, left, top, right, bottom };
        if (b?.wrap === true) block.wrap = true;
        clean.push(block);
      }
      previewBlockRects.set(iframe, clean);
      placeMarkers();
    }
  };
  window.addEventListener("message", onMessage);

  return {
    cleanup: () => {
      window.removeEventListener("message", onMessage);
      previewBlockRects.delete(iframe);
      previewBridgeArmed.delete(iframe);
    },
    sync: () => {
      applyHeight();
      placeMarkers();
    },
  };
}

// urlHashArmed tracks `lvt-fx:url-hash` listeners and their last-
// mirrored hash. Same Map-not-WeakMap reasoning as area-select: the
// sweep iterates to detect elements whose attribute was removed by a
// server diff, and detached elements are cleaned up via the same
// sweep (isConnected check).
//
// Module-level singleton: shared across all LiveTemplateClient
// instances in the window. Two clients arming DIFFERENT elements
// each get their own entry; the shared window hashchange listener
// iterates the map and dispatches through every armed entry's own
// send, so a multi-client page sees each client receive its hash
// event. Teardown is scoped per root via teardownURLHashForRoot, so
// clients don't tear down each other's listeners.
//
// Same-element multi-arm is "last writer wins": the Map key is the
// element, so a second client arming the same element runs the
// existing entry's cleanup() and replaces it. The first client's
// send is orphaned. This matches area-select's behavior and is fine
// for the documented single-arm-per-element contract.
const urlHashArmed = new Map<Element, URLHashEntry>();

// urlHashWindowListener is the single window-level `hashchange`
// listener shared across all armed elements. Registered on first arm,
// removed when the armed map becomes empty. Per-element listeners
// would multi-fire for the (rare) case of multiple armed elements;
// one shared listener iterating the armed map keeps the dispatch
// count deterministic.
let urlHashWindowListener: ((e: HashChangeEvent) => void) | null = null;

interface URLHashEntry {
  action: string;
  // send is stored on the entry so the shared window listener can
  // dispatch each armed entry's action through its own transport. The
  // idempotent re-arm path mutates this field directly (via
  // updateSend), so a reconnect that rebuilt the transport is picked
  // up on the next hashchange.
  send: URLHashSendFn;
  cleanup: () => void;
  updateSend: (send: URLHashSendFn) => void;
  // currentDataHash is the last `data-lvt-url-hash` value we mirrored
  // into `location.hash` (or the value we observed on a user-initiated
  // hashchange). Comparing against the next render's data-attr lets us
  // no-op when the server re-rendered with the same hash — avoids
  // extra history entries on every keystroke.
  currentDataHash: string;
}

type URLHashSendFn = (
  message: { action: string; data: Record<string, unknown> }
) => void;

/**
 * Apply url-hash directives. `lvt-fx:url-hash="<actionName>"` plus a
 * `data-lvt-url-hash="<hash>"` attribute on an element (typically the
 * `<body>`) wires a two-way bridge between server state and
 * `location.hash`:
 *
 *  - **State → URL** (every render): if `data-lvt-url-hash` differs
 *    from `location.hash`, mirror the data-attr into the URL via
 *    `history.pushState` when the path component changed (everything
 *    before the first `:`) or `history.replaceState` when only the
 *    target (line range / anchor) changed. Replace is the right
 *    default for line scrolls so the back-button cycles between files,
 *    not between every clicked line.
 *  - **URL → State** (on `hashchange` AND initial arm): dispatch
 *    `{action: <actionName>, data: {hash: <hash>}}` so the server can
 *    parse the hash and update its state (which then renders back as
 *    a matching data-attr — closing the loop).
 *
 * The directive uses `history.pushState`/`replaceState` (not
 * `location.hash = ...`) for the state→URL direction precisely so
 * those writes do NOT fire `hashchange` — only true user-initiated
 * navigation (anchor click, address-bar edit, back-button) reaches
 * the URL→state listener. This avoids the obvious infinite loop.
 *
 * Idempotent across renders: same action → keep listener + update
 * send. Different action → cleanup + re-arm. Detached / attribute-
 * removed elements are swept on every call (same pattern as
 * area-select). The window listener is registered on first arm and
 * removed when the armed map becomes empty.
 *
 * Coexistence with `setupHashLink`: prereview-style hashes
 * (`README.md:L4`, `foo/bar.html:h-anchor`) never match a
 * `document.getElementById(...)`, so the existing dialog/popover/
 * details hash machinery silently no-ops. If a deep-link hash
 * happens to collide with an element id, both handlers will fire —
 * the server is expected to no-op on hashes that don't resolve to a
 * known file.
 *
 * **Pre-encoding contract**: `data-lvt-url-hash` must hold the hash
 * value already in URL-encoded form. The directive writes the
 * attribute verbatim into `history.pushState`/`replaceState`, so a
 * value containing spaces, `[`, `]`, `%`, or other reserved
 * characters needs to be percent-encoded by the server. The hash
 * sent to the action on `hashchange` is also passed through unmodified
 * (no decoding) — both directions are byte-exact mirrors of what's
 * in `location.hash`.
 *
 * **URL/state divergence after a non-deep-link initial load**: if
 * the user lands with a native-anchor hash (`#hero`) AND the server
 * has a selected file, the directive leaves the URL on `#hero` (case
 * b) — URL and server state diverge until the user navigates. This
 * is intentional: popovers/anchors aren't ours to overwrite. The
 * next user action that triggers a server render will re-sync only
 * once URL and state share a deep-link hash.
 *
 * **Path-only deep links require an extension**: the
 * `looksLikeDeepLinkHash` heuristic dispatches only hashes
 * containing `:`, `/`, or `.`. Extension-less root files
 * (`#Makefile`, `#Dockerfile`, `#LICENSE`) won't dispatch as
 * path-only deep links — use the line form (`#Makefile:L1`)
 * instead. The trade-off favours not clobbering native-anchor
 * machinery for single-token hashes.
 */
export function handleURLHashDirective(
  rootElement: Element,
  send: (message: { action: string; data: Record<string, unknown> }) => void
): void {
  // Sweep stale entries first — disconnected hosts AND hosts whose
  // attribute was removed by a server diff. Iterate via Array.from so
  // cleanup()'s delete() doesn't disturb the iterator.
  for (const [element, entry] of Array.from(urlHashArmed)) {
    if (
      !element.isConnected ||
      !element.hasAttribute("lvt-fx:url-hash")
    ) {
      entry.cleanup();
    }
  }

  // Match the root itself, descendants, AND the document body. The
  // url-hash directive is typically placed on `<body>`, but livetemplate
  // auto-injects its `<div data-lvt-id>` INSIDE body, so the rootElement
  // passed by the client is the wrapper div — a strict descendant of
  // body. Without the body check, a directive on `<body>` would never
  // arm. We accept body placement because URL hash is page-global
  // anyway; the directive's lifecycle is still tied to the wrapper via
  // teardownURLHashForRoot (called on disconnect of the wrapper).
  const matches: HTMLElement[] = [];
  if (
    rootElement instanceof HTMLElement &&
    rootElement.hasAttribute("lvt-fx:url-hash")
  ) {
    matches.push(rootElement);
  }
  const body = rootElement.ownerDocument?.body;
  if (
    body &&
    body !== rootElement &&
    body.hasAttribute("lvt-fx:url-hash") &&
    !matches.includes(body)
  ) {
    matches.push(body);
  }
  rootElement
    .querySelectorAll<HTMLElement>("[lvt-fx\\:url-hash]")
    .forEach((el) => {
      if (!matches.includes(el)) matches.push(el);
    });
  if (matches.length === 0) return;

  for (const el of matches) {
    const action = el.getAttribute("lvt-fx:url-hash");
    if (!action) {
      console.warn(
        `lvt-fx:url-hash requires an action name, got: ${JSON.stringify(action)}`
      );
      continue;
    }
    const dataHash = el.getAttribute("data-lvt-url-hash") || "";
    const existing = urlHashArmed.get(el);
    if (existing && existing.action === action) {
      existing.updateSend(send);
      mirrorDataAttrToLocation(existing, dataHash);
      continue;
    }
    if (existing) existing.cleanup();
    const entry = attachURLHash(el, action, send);
    urlHashArmed.set(el, entry);
    // First-arm sync: three cases, in priority order.
    const initialLocation = window.location.hash.replace(/^#/, "");
    if (
      initialLocation &&
      initialLocation !== dataHash &&
      looksLikeDeepLinkHash(initialLocation)
    ) {
      // (a) URL has a deep-link hash that differs from server state.
      // URL "wins" on initial load — dispatch so the server can
      // reconcile, and seed currentDataHash so the converging render
      // doesn't try to mirror over the user's URL.
      entry.currentDataHash = initialLocation;
      send({ action, data: { hash: initialLocation } });
    } else if (initialLocation && !looksLikeDeepLinkHash(initialLocation)) {
      // (b) URL has a non-deep-link hash (e.g. `#hero` opening a
      // popover, or a native heading anchor). Leave it alone — it
      // belongs to other machinery (setupHashLink, native scroll).
      // Seed currentDataHash so a later mirror sees the data-attr
      // as the baseline to compare against, and only writes when
      // the user navigates away from the popover/anchor.
      entry.currentDataHash = dataHash;
    } else {
      // (c) URL is empty (or already matches the server). Mirror the
      // server's hash into the URL if any.
      mirrorDataAttrToLocation(entry, dataHash);
    }
  }
}

/**
 * Cancel url-hash listeners for every armed element under root. Same
 * lifecycle role as teardownAreaSelectForRoot.
 */
export function teardownURLHashForRoot(rootElement: Element): void {
  // Includes body when body is an ancestor of rootElement and body is
  // armed — the directive accepts body placement (see the matcher in
  // handleURLHashDirective), so teardown must symmetrically clean up
  // both directions.
  //
  // Multi-client caveat: a body-armed entry is shared across all
  // LiveTemplateClient instances (Map key is the element, so only one
  // entry per body). Tearing down client A's root will therefore also
  // tear down a body listener that client B armed last — there's no
  // "owner" tracked. Acceptable for the single-client case (the
  // common deployment) and matches the same-element-multi-arm
  // last-writer-wins behavior in attachURLHash. A "fix" that
  // restricted the body-cleanup branch to client A would leak
  // client A's own body listener — don't do that without also
  // tracking entry ownership.
  const body = rootElement.ownerDocument?.body;
  for (const [element, entry] of Array.from(urlHashArmed)) {
    if (rootElement.contains(element)) {
      entry.cleanup();
      continue;
    }
    if (body && element === body && body.contains(rootElement)) {
      entry.cleanup();
    }
  }
}

// mirrorDataAttrToLocation pushes `dataHash` into `location.hash` if
// it differs from what's already in the URL. Chooses push vs replace
// by comparing the path component (everything before the first `:`)
// against the current location.hash's path: a path change is a "file
// switch" (user-meaningful back-button entry) and gets pushState;
// any other change is a target-only update (line scroll / anchor
// scroll) and gets replaceState. Updates entry.currentDataHash so a
// subsequent render with the same data-attr no-ops.
//
// Initial-mirror special case: if the URL was empty when we're
// mirroring (no prior hash to compare against), use replaceState even
// though the path-component comparison would say "changed". An empty
// URL → first server hash isn't a "navigation" — we're establishing
// the initial state. Using pushState here would let Back land the
// user on `url-without-hash`, which re-triggers the same arm and
// pushes the same hash again. Loop.
//
// Empty-dataHash special case: if the server transitions FROM a
// selected file TO no-selection (state.URLHash() returns ""), we
// would otherwise wipe location.hash entirely — including hashes the
// directive doesn't own (a popover #hero the user opened during the
// session). To stay safe, only clear when the URL currently holds a
// deep-link-shaped hash; non-deep-link hashes are left alone.
function mirrorDataAttrToLocation(entry: URLHashEntry, dataHash: string): void {
  if (entry.currentDataHash === dataHash) return;
  const currentLocation = window.location.hash.replace(/^#/, "");
  if (currentLocation === dataHash) {
    entry.currentDataHash = dataHash;
    return;
  }
  if (currentLocation !== "" && !looksLikeDeepLinkHash(currentLocation)) {
    // URL is on something not ours (popover id, native anchor) —
    // don't clobber it, regardless of what the server's data-attr
    // says. This covers BOTH the server-clears case (dataHash="")
    // and the rarer server-changes-selection-while-popover-open
    // case (dataHash transitions from one file to another while
    // the URL is parked on a non-deep-link hash).
    entry.currentDataHash = dataHash;
    return;
  }
  warnIfUnencodedHash(dataHash);
  const targetURL = dataHash ? `#${dataHash}` : window.location.pathname + window.location.search;
  const oldPath = currentLocation.split(":")[0];
  const newPath = dataHash.split(":")[0];
  // Preserve existing history.state — passing `null` would clobber
  // anything other SPA-like code on the page stores there (scroll
  // position, modal flag, etc.). The state object is independent of
  // the URL we're rewriting, so carrying it forward is the right
  // default.
  const currentState = window.history.state;
  // Empty currentLocation means we're establishing the URL from a
  // blank slate (initial render with no prior URL hash) — that's NOT
  // a back-button-meaningful navigation, so always replaceState.
  // Otherwise: a path change is a file switch (push), a target-only
  // change is a line/anchor scroll (replace).
  if (currentLocation !== "" && oldPath !== newPath) {
    window.history.pushState(currentState, "", targetURL);
  } else {
    window.history.replaceState(currentState, "", targetURL);
  }
  entry.currentDataHash = dataHash;
}

// warnIfUnencodedHash flags `data-lvt-url-hash` values containing
// characters that should be percent-encoded (raw space, `<`, `>`,
// `"`, ``` ` ```, `#`, `[`, `]`, `%`). The directive writes the
// hash verbatim into `pushState`/`replaceState`, so an unencoded
// value will silently produce a malformed URL — `location.hash`
// reads back differently from what was set. Cheap dev-time guard
// against a server-side contract slip; dedupes by value to avoid
// log spam.
//
// `%` is included because a raw `%` not followed by two hex digits
// is itself a percent-encoding error. The check is a heuristic
// (won't catch every malformed escape), but covers the common
// "forgot to encode" cases.
const urlHashUnencodedWarned = new Set<string>();

/**
 * Test-only: reset the per-page dedupe Set that suppresses repeated
 * `warnIfUnencodedHash` calls for the same hash value. Production
 * code shouldn't need this — the Set is bounded by the number of
 * unique malformed hashes — but tests that re-use the same hash
 * across cases need to clear it or the second test won't see the
 * warning. Mirrors `__resetAnimatedElementsForTesting`.
 */
export function __resetURLHashUnencodedWarnedForTesting(): void {
  urlHashUnencodedWarned.clear();
}

function warnIfUnencodedHash(hash: string): void {
  if (!hash || urlHashUnencodedWarned.has(hash)) return;
  if (/[ <>"`#\[\]]/.test(hash) || /%(?![0-9A-Fa-f]{2})/.test(hash)) {
    urlHashUnencodedWarned.add(hash);
    console.warn(
      `lvt-fx:url-hash: data-lvt-url-hash="${hash}" contains characters that should be percent-encoded. The directive writes it verbatim into history.pushState/replaceState; malformed URLs result. Server-side FormatHash (or equivalent) should percent-escape path segments and target ids before serialization.`
    );
  }
}

// looksLikeDeepLinkHash discriminates URL hashes the prereview deep-
// link grammar can produce (file path with optional :L<n> or :h-id)
// from hashes that belong to other native machinery (HTML element
// anchors, dialog/popover/details ids, etc.). Deep-link hashes always
// contain at least one of: `:` (target separator), `/` (nested path),
// or `.` (file extension). Empty → false.
//
// False positives are possible but cheap. A heading id like
// `#v1.0.0`, `#menu/item`, or `#key:value` matches this heuristic
// and will dispatch the action — but the consuming server is
// expected to no-op on hashes whose path doesn't resolve to a known
// file (prereview's SetURLHash does, via the loadDiffCached failure
// path). The cost is one wasted roundtrip per false positive, which
// is acceptable for the alternative of missing real deep links.
//
// False negatives: extension-less filenames at the repo root —
// `#Makefile`, `#Dockerfile`, `#LICENSE` — don't match this
// heuristic and won't be dispatched as path-only deep links. The
// workaround is the line-form (`#Makefile:L1`), which always
// dispatches. This trade-off is deliberate: a heuristic that
// matched single-token hashes would also clobber every native
// anchor / popover id, which is a much worse default. Consumers
// that need extension-less file deep links can build a richer
// directive on top.
function looksLikeDeepLinkHash(hash: string): boolean {
  if (!hash) return false;
  return hash.includes(":") || hash.includes("/") || hash.includes(".");
}

function attachURLHash(
  el: HTMLElement,
  action: string,
  initialSend: URLHashSendFn
): URLHashEntry {
  const entry: URLHashEntry = {
    action,
    send: initialSend,
    cleanup: () => {
      urlHashArmed.delete(el);
      if (urlHashArmed.size === 0 && urlHashWindowListener) {
        window.removeEventListener("hashchange", urlHashWindowListener);
        urlHashWindowListener = null;
      }
    },
    updateSend: (s) => {
      entry.send = s;
    },
    currentDataHash: "",
  };

  if (!urlHashWindowListener) {
    urlHashWindowListener = () => {
      const hash = window.location.hash.replace(/^#/, "");
      // Only dispatch hashes that look like deep-link targets — they
      // contain `:` (target separator), `/` (nested path), or `.`
      // (file extension). Plain element-id hashes like `#hero` or
      // `#confirm-delete-xyz` belong to the native anchor / dialog /
      // popover / details machinery (setupHashLink handles those) and
      // would otherwise be dispatched here, prompt a server no-op,
      // then get clobbered by the mirror step when the server's
      // data-attr (unchanged) doesn't match.
      //
      // Empty hash (user cleared the URL bar) is also intentionally
      // ignored. The directive treats the server as the source of
      // truth for "what's selected"; an empty URL is "user navigated
      // away from a hash" but not "deselect everything". If the user
      // wants to deselect, they use the in-app affordance
      // (clearSelection / Escape) which makes the server emit an
      // empty data-attr — at which point the mirror step propagates
      // the empty hash back to the URL.
      if (!looksLikeDeepLinkHash(hash)) return;
      // Iterate via Array.from in case a dispatched action triggers a
      // render that mutates the armed map (e.g. tears down this
      // element). Each armed entry dispatches through its OWN send +
      // action so multi-arm is deterministic — typically the body is
      // the only armed element so this is one iteration.
      for (const e of Array.from(urlHashArmed.values())) {
        // Record the user-driven hash as the new baseline so the
        // next render's mirror step doesn't immediately revert it.
        e.currentDataHash = hash;
        e.send({ action: e.action, data: { hash } });
      }
    };
    window.addEventListener("hashchange", urlHashWindowListener);
  }

  return entry;
}

// attachAreaSelect captures `send` in a mutable local so the
// idempotent re-arm path (same element, same action) can swap it via
// the returned `updateSend` callback without tearing down + rebuilding
// listeners. Listeners reference the closure-captured `send` variable
// directly, so reassigning it propagates instantly. This guards
// against the stale-closure trap a caller would hit if their `send`
// reference changed across renders — e.g. a reconnect rebuilt the
// transport.
// BoxDragResult is the final rectangle handed to a box-drag consumer when
// a real drag (above the click threshold) completes inside the host.
interface BoxDragResult {
  // Clamped drag rectangle in VIEWPORT coordinates (host-bounded). Used by
  // hit-test consumers (region-select) that compare the box against other
  // elements' getBoundingClientRect().
  left: number;
  top: number;
  right: number;
  bottom: number;
  // The same rectangle as 0..1 fractions of the host's rendered rect. Used
  // by fraction consumers (area-select on an image).
  x: number;
  y: number;
  w: number;
  h: number;
}

// attachBoxDragSelect wires the pointer-drag gesture + live overlay on a
// host element and invokes onComplete with the final rectangle when the
// user finishes a drag big enough to count as a selection (not a click).
// It owns every pointer-capture / re-entrancy / multi-touch / parent-
// positioning / off-edge-clamping subtlety; consumers differ ONLY in what
// they do with the result — area-select sends {x,y,w,h} fractions, region-
// select hit-tests the viewport box to a source line range. This is the
// shared spine; do not duplicate it.
function attachBoxDragSelect(
  el: HTMLElement,
  warnedParents: WeakSet<Element>,
  attrName: string,
  onComplete: (result: BoxDragResult) => void
): { cleanup: () => void } {
  let overlay: HTMLDivElement | null = null;
  let startClientX = 0;
  let startClientY = 0;
  let pointerId = -1;
  // pointerType + lastMove let us recover a TOUCH drag that iOS Safari
  // cancels mid-gesture. iOS fires pointercancel / yanks pointer capture on
  // long, fast touch-drags (it re-interprets them as a scroll/swipe), which
  // would otherwise discard a large region the user deliberately drew. For
  // touch we finalize WITH the last-known position instead of dropping it;
  // mouse/pen keep the strict discard. (The area threshold in finalize still
  // rejects an accidental tiny box, so a stray cancel can't create a region.)
  let pointerType = "";
  let lastMove: PointerEvent | null = null;
  // Capture the parent at pointerdown time so a server diff that moves
  // the host to a NEW parent mid-drag doesn't split the drag across
  // two positioning contexts. updateOverlay positions against this
  // cached parent for the lifetime of the gesture; the overlay itself
  // stays a child of the parent we appended it to (overlay removal
  // uses overlay.parentElement, which is independent).
  let dragParent: HTMLElement | null = null;
  // Cache the host's rect at pointerdown — startClientX/Y are captured
  // in the SAME frame, so the start corner is meaningful only against
  // the rect that existed then. If a server diff repositions the host
  // mid-drag, finalize would otherwise clamp the (old-coord-system)
  // startClientX against the new rect and silently produce wrong
  // fractions. Anchoring to the start-rect keeps the dispatched
  // rectangle pinned to the visual region the user actually dragged.
  let startRect: DOMRect | null = null;

  const removeOverlay = () => {
    if (overlay) {
      // Element.remove() is a no-op if the node isn't in the DOM,
      // so we don't need the parent-null guard the older two-step
      // pattern needed.
      overlay.remove();
    }
    overlay = null;
  };

  const finalize = (e: PointerEvent | null, dispatch: boolean) => {
    if (pointerId === -1) return;
    // CRITICAL ORDER: reset pointerId + dragParent + startRect BEFORE
    // calling releasePointerCapture. Chromium fires lostpointercapture
    // SYNCHRONOUSLY during releasePointerCapture, which lands in
    // onLostCapture → finalize(null, false). Without the early reset,
    // the nested finalize sees pointerId still matching and runs to
    // completion (clearing startRect), then the outer finalize
    // resumes with startRect == null and silently drops the
    // dispatched action. Resetting first makes the nested call
    // return at the `pointerId === -1` guard, leaving outer state
    // intact.
    const capturedPointerId = pointerId;
    const rect = startRect;
    pointerId = -1;
    pointerType = "";
    dragParent = null;
    startRect = null;
    lastMove = null;
    try {
      el.releasePointerCapture(capturedPointerId);
    } catch {
      // Capture may already be gone (e.g. pointercancel) — ignore.
    }
    // Remove the per-gesture pointerleave fallback so a NEXT drag
    // doesn't inherit a stale listener from this one. {once: true}
    // only auto-removes if it fires; a stuck drag never fired it.
    el.removeEventListener("pointerleave", onPointerLeaveCancel);
    if (!dispatch || !e || !rect) {
      removeOverlay();
      return;
    }
    if (rect.width <= 0 || rect.height <= 0) {
      removeOverlay();
      return;
    }
    // Clamp the two corners to the rect BEFORE computing fractions so
    // a drag that escapes the element still yields a rectangle inside
    // it (x ∈ [0,1], w ∈ [0,1-x]). Otherwise a far-off-rect endpoint
    // would push w past 1 even with x already > 0.
    const rectRight = rect.left + rect.width;
    const rectBottom = rect.top + rect.height;
    const x0 = clampRange(Math.min(startClientX, e.clientX), rect.left, rectRight);
    const y0 = clampRange(Math.min(startClientY, e.clientY), rect.top, rectBottom);
    const x1 = clampRange(Math.max(startClientX, e.clientX), rect.left, rectRight);
    const y1 = clampRange(Math.max(startClientY, e.clientY), rect.top, rectBottom);
    const x = (x0 - rect.left) / rect.width;
    const y = (y0 - rect.top) / rect.height;
    const w = (x1 - x0) / rect.width;
    const h = (y1 - y0) / rect.height;
    removeOverlay();
    // Reject zero-area rectangles outright. The MIN_AREA_FRACTION
    // check below uses `&&` (drop only when BOTH dims are small) so
    // a wide-but-thin selection is preserved — but a literal
    // 60%×0 (or 0×60%) collapses to no region, can't be rendered
    // sensibly, and would divide by zero in any pixel-space
    // conversion downstream. Drop independently of the threshold.
    if (w <= 0 || h <= 0) return;
    // Drop when BOTH dimensions are below the threshold (intentional
    // `&&` — NOT `||`). A wide-but-thin drag (e.g. an underline across
    // an annotated row) or a tall-but-thin drag (e.g. a vertical
    // highlight) is a real selection in this directive's contract,
    // not an accidental click. `||` would drop those legitimate
    // gestures. The click-vs-drag boundary lives in "the rect has
    // basically no area" — that's both dims below the threshold.
    if (w < MIN_AREA_FRACTION && h < MIN_AREA_FRACTION) {
      // Treat as a click, not a drag. Don't dispatch; let normal click
      // handlers (if any) run via the platform.
      return;
    }
    // Hand the consumer both the clamped viewport box (x0,y0,x1,y1) and
    // the host-relative fractions; it decides the payload.
    onComplete({ left: x0, top: y0, right: x1, bottom: y1, x, y, w, h });
  };

  const onPointerLeaveCancel = (e: PointerEvent) => {
    // Fallback for the rare case where setPointerCapture failed: without
    // capture, pointermove + pointerup stop arriving once the pointer
    // leaves the host, freezing the overlay. Treating pointerleave as
    // a cancel keeps the overlay from getting stuck on screen.
    // Guard on pointerId — in multi-touch, a SECONDARY pointer's
    // leave shouldn't cancel the primary drag.
    if (e.pointerId !== pointerId) return;
    finalize(null, false);
  };

  // Chromium fires `dragstart` on an <img> after the first mousemove
  // following mousedown, yanking the gesture away from pointer events
  // before pointerup arrives — the overlay flashes and capture is
  // lost. preventDefault on dragstart suppresses the native image
  // drag without breaking pointer events. Cheap to attach on every
  // element type (non-img hosts simply never fire dragstart).
  const onDragStart = (e: DragEvent) => e.preventDefault();

  const onPointerDown = (e: PointerEvent) => {
    // Only primary button (left mouse / single touch / pen tip). Modifier
    // keys passed through so the server-side handler can decide what to
    // do with them via subsequent renders.
    if (!e.isPrimary || e.button !== 0) return;
    // Re-entrancy guard: if a prior drag never finished (e.g. capture
    // failed silently, then pointer left the element with no pointerup
    // ever delivered), the closed-over pointerId variable would still
    // hold the stale id. Cancel the prior drag — removing its overlay
    // and listeners — before starting a fresh one.
    if (pointerId !== -1) finalize(null, false);
    const parent = el.parentElement;
    if (!parent) return; // overlay needs a positioned container
    // Dev-time check: if the parent doesn't establish a positioning
    // context, the overlay's `position: absolute` will resolve against
    // the nearest positioned ANCESTOR — a distant element with no
    // visible relationship to the host. Result: overlay paints in
    // the wrong place with no error, just a confusing visual.
    // Check against the positive list of positioned values; the
    // default "static" and an unset/empty value both fail it (jsdom
    // returns "" for unset position). Dedupe via WeakSet so a user
    // dragging repeatedly on the same mis-configured parent gets ONE
    // console message, not one per pointerdown.
    if (!warnedParents.has(parent)) {
      const parentPos = window.getComputedStyle(parent).position;
      if (
        parentPos !== "relative" &&
        parentPos !== "absolute" &&
        parentPos !== "fixed" &&
        parentPos !== "sticky"
      ) {
        console.warn(
          `${attrName}: parentElement has no positioning context; the drag overlay will be mis-positioned. ` +
            "Add position:relative (or absolute/fixed/sticky) to the parent.",
          parent
        );
        warnedParents.add(parent);
      }
    }
    startClientX = e.clientX;
    startClientY = e.clientY;
    pointerId = e.pointerId;
    pointerType = e.pointerType;
    lastMove = e;
    dragParent = parent;
    startRect = el.getBoundingClientRect();
    let captureOk = false;
    try {
      el.setPointerCapture(pointerId);
      captureOk = true;
    } catch {
      // Capture failure is non-fatal — without it, leaving the element
      // mid-drag will lose pointermove. Fall back to pointerleave as
      // the cancel signal so the overlay can't get stuck.
    }
    if (!captureOk) {
      el.addEventListener("pointerleave", onPointerLeaveCancel, { once: true });
    }
    overlay = document.createElement("div");
    overlay.className = "lvt-area-select-overlay";
    overlay.setAttribute("aria-hidden", "true");
    // Inline styles so the directive doesn't depend on a CSS class
    // shipped by the consumer. Consumers can override via the class
    // selector if they want a different look.
    overlay.style.cssText =
      "position:absolute;pointer-events:none;border:2px solid var(--lvt-area-select-color,#4cc2ff);" +
      "background:var(--lvt-area-select-fill,rgba(76,194,255,0.18));box-sizing:border-box;" +
      "z-index:var(--lvt-area-select-z-index,9999);";
    parent.appendChild(overlay);
    updateOverlay(e);
    // NOT calling e.preventDefault() here: doing so on pointerdown
    // suppresses the compatibility mouse events (mousedown → mouseup
    // → click), so a small-rect drag (which finalize() treats as a
    // click) would never reach the host's click handlers. The
    // directive's contract promises clicks still bubble. Text-
    // selection during drag is the consumer's responsibility — set
    // `user-select: none` on the host (the contract docs this).
  };

  const updateOverlay = (e: PointerEvent) => {
    if (!overlay) return;
    // Use the parent captured at pointerdown — if a server diff
    // moved `el` to a new parent mid-drag, re-fetching el.parentElement
    // here would compute against the new container while the overlay
    // lives in the old, paint at the wrong place for the rest of the
    // gesture.
    const parent = dragParent;
    if (!parent) return;
    const elRect = el.getBoundingClientRect();
    const parentRect = parent.getBoundingClientRect();
    // Convert viewport coords (clientX/Y) to position:absolute CSS
    // offsets inside the parent. Three corrections, all subtracted
    // from / added to the same way for every value we compute:
    //
    //   1. parentRect.left/top — getBoundingClientRect is in viewport
    //      coords; CSS offsets are relative to the parent's box.
    //   2. parent.clientLeft/Top — position:absolute is measured from
    //      the padding box; getBoundingClientRect returns the border
    //      box. A parent with a CSS border would otherwise shift the
    //      overlay by the border width.
    //   3. parent.scrollLeft/Top — when the parent is scrolled, an
    //      element at viewport_x = parentRect.left has CSS_left =
    //      parent.scrollLeft (not 0). Without adding scroll back in,
    //      the overlay paints offset by the scroll amount.
    const borderL = parent.clientLeft;
    const borderT = parent.clientTop;
    const scrollL = parent.scrollLeft;
    const scrollT = parent.scrollTop;
    const toCSSLeft = (vx: number) => vx - parentRect.left - borderL + scrollL;
    const toCSSTop = (vy: number) => vy - parentRect.top - borderT + scrollT;
    const left = toCSSLeft(Math.min(startClientX, e.clientX));
    const top = toCSSTop(Math.min(startClientY, e.clientY));
    const width = Math.abs(e.clientX - startClientX);
    const height = Math.abs(e.clientY - startClientY);
    // Clamp to the host's rendered rect (in the same CSS coord space)
    // so a drag that runs off the edge doesn't paint outside the host.
    const minLeft = toCSSLeft(elRect.left);
    const minTop = toCSSTop(elRect.top);
    const maxRight = minLeft + elRect.width;
    const maxBottom = minTop + elRect.height;
    const clampedLeft = Math.max(minLeft, Math.min(left, maxRight));
    const clampedTop = Math.max(minTop, Math.min(top, maxBottom));
    const clampedRight = Math.max(minLeft, Math.min(left + width, maxRight));
    const clampedBottom = Math.max(minTop, Math.min(top + height, maxBottom));
    overlay.style.left = `${clampedLeft}px`;
    overlay.style.top = `${clampedTop}px`;
    overlay.style.width = `${Math.max(0, clampedRight - clampedLeft)}px`;
    overlay.style.height = `${Math.max(0, clampedBottom - clampedTop)}px`;
  };

  const onPointerMove = (e: PointerEvent) => {
    if (e.pointerId !== pointerId) return;
    // Host removed from the DOM mid-drag (e.g. server diff replaced it).
    // Without this, the overlay would be left orphaned under the parent
    // because the host's cleanup never runs.
    if (!el.isConnected) {
      finalize(null, false);
      return;
    }
    lastMove = e;
    updateOverlay(e);
  };

  const onPointerUp = (e: PointerEvent) => {
    if (e.pointerId !== pointerId) return;
    if (!el.isConnected) {
      finalize(null, false);
      return;
    }
    finalize(e, true);
  };

  const onPointerCancel = (e: PointerEvent) => {
    if (e.pointerId !== pointerId) return;
    // iOS cancels long touch-drags it re-reads as a swipe; keep the region
    // the user drew rather than dropping it (mouse/pen still discard).
    finalize(e, pointerType === "touch");
  };
  // lostpointercapture handles the rare case where the platform yanks
  // capture (OS gesture, another setPointerCapture call). Guard on
  // pointerId — another code path could call setPointerCapture for a
  // DIFFERENT pointer on the same element, and we mustn't cancel
  // our in-progress drag because of an unrelated release.
  const onLostCapture = (e: PointerEvent) => {
    if (e.pointerId !== pointerId) return;
    // Same touch-recovery as pointercancel: finalize from the last move so a
    // capture yank mid-touch-drag keeps the region instead of dropping it.
    // (lastMove is non-null whenever a drag is active — set on pointerdown —
    // and finalize tolerates a null event anyway, so no extra guard is needed.)
    const touch = pointerType === "touch";
    finalize(touch ? lastMove : null, touch);
  };

  el.addEventListener("pointerdown", onPointerDown);
  el.addEventListener("pointermove", onPointerMove);
  el.addEventListener("pointerup", onPointerUp);
  el.addEventListener("pointercancel", onPointerCancel);
  el.addEventListener("lostpointercapture", onLostCapture);
  el.addEventListener("dragstart", onDragStart);

  const cleanup = () => {
    el.removeEventListener("pointerdown", onPointerDown);
    el.removeEventListener("pointermove", onPointerMove);
    el.removeEventListener("pointerup", onPointerUp);
    el.removeEventListener("pointercancel", onPointerCancel);
    el.removeEventListener("lostpointercapture", onLostCapture);
    el.removeEventListener("pointerleave", onPointerLeaveCancel);
    el.removeEventListener("dragstart", onDragStart);
    finalize(null, false);
  };

  return { cleanup };
}

// attachAreaSelect wires lvt-fx:area-select on an image host: a drawn box
// dispatches the action with {x,y,w,h} as 0..1 fractions of the host's
// rendered rect (image bytes don't reflow, so a pixel rect is stable).
function attachAreaSelect(
  el: HTMLElement,
  action: string,
  initialSend: AreaSelectSendFn
): AreaSelectEntry {
  let send = initialSend;
  const handle = attachBoxDragSelect(
    el,
    areaSelectWarnedParents,
    "lvt-fx:area-select",
    (r) => send({ action, data: { x: r.x, y: r.y, w: r.w, h: r.h } })
  );
  return {
    action,
    cleanup: () => {
      handle.cleanup();
      areaSelectArmed.delete(el);
    },
    updateSend: (s) => {
      send = s;
    },
  };
}

// region-select reuses the area-select entry shape (action + cleanup +
// updateSend); the alias names that intent so the map type doesn't read
// as "a region map holding area entries".
type BoxDragEntry = AreaSelectEntry;

// regionSelectArmed / regionSelectWarnedParents mirror the area-select
// singletons (see those for the Map-not-WeakMap reasoning).
const regionSelectArmed = new Map<Element, BoxDragEntry>();
const regionSelectWarnedParents = new WeakSet<Element>();

/**
 * Apply region-select directives. `lvt-fx:region-select="<actionName>"` on
 * a transparent overlay (parent light DOM) laid over a rendered-HTML
 * preview iframe or a code view lets the user DRAW A BOX to comment on a
 * region — the same touch-capable drag as area-select, but the drawn box
 * is hit-tested to a SOURCE LINE RANGE instead of a pixel rect, so the
 * comment survives responsive reflow and round-trips with the raw view +
 * CSV (issue #26 region comments).
 *
 * The overlay's `data-surface` selects the hit-test:
 *   - "html": the box is intersected against the sibling iframe's
 *     contentDocument `[data-from]`/`[data-to]` blocks, offset by the
 *     iframe's viewport position (the auto-height iframe doesn't scroll).
 *   - "code": the box is intersected against the parent's `[data-line]`
 *     rows in the same document.
 * Either resolves to `{from, to[, side]}` and dispatches the action; a box
 * that hits no line-bearing element dispatches nothing.
 *
 * The overlay lives in the PARENT document on purpose: iOS Safari does not
 * deliver events that happen inside an iframe to a parent listener, so the
 * old in-iframe tap never arrived on a phone. A parent overlay is the same
 * event path as the file list, which works.
 *
 * Idempotent + swept exactly like area-select. The overlay's parent must
 * establish a positioning context (the shared drag spine warns if not).
 * Images keep using lvt-fx:area-select (pixel rects are stable).
 */
export function handleRegionSelectDirectives(
  rootElement: Element,
  send: AreaSelectSendFn
): void {
  for (const [element, entry] of Array.from(regionSelectArmed)) {
    if (!element.isConnected || !element.hasAttribute("lvt-fx:region-select")) {
      entry.cleanup();
    }
  }
  const matches = rootElement.querySelectorAll<HTMLElement>(
    "[lvt-fx\\:region-select]"
  );
  if (matches.length === 0) return;
  for (const el of matches) {
    const action = el.getAttribute("lvt-fx:region-select");
    if (!action) {
      console.warn(
        `lvt-fx:region-select requires an action name, got: ${JSON.stringify(action)}`
      );
      continue;
    }
    const existing = regionSelectArmed.get(el);
    if (existing && existing.action === action) {
      existing.updateSend(send);
      continue;
    }
    if (existing) existing.cleanup();
    regionSelectArmed.set(el, attachRegionSelect(el, action, send));
  }
}

/**
 * Cancel region-select listeners for every armed element under root.
 * Mirror of teardownAreaSelectForRoot for the client disconnect lifecycle.
 */
export function teardownRegionSelectForRoot(rootElement: Element): void {
  for (const [element, entry] of Array.from(regionSelectArmed)) {
    if (rootElement.contains(element)) {
      entry.cleanup();
    }
  }
}

// ── text-select ────────────────────────────────────────────────────────────
// lvt-fx:text-select="<action>" on a container captures a NATIVE text selection
// (window.getSelection) over line-bearing children and dispatches a CHARACTER
// range to the server, so a reviewer can comment on a word / phrase / multi-line
// span — not just whole lines. Unlike area/region-select (draw a box, hit-test
// rects), it reads the browser's own selection, so it works with mouse, touch,
// AND keyboard from one code path.
//
// Contract (kept generic — no host-app class names):
//   - each line is an element carrying `data-line` (1-based source line) and,
//     optionally, `data-side="old"` (a diff's deleted side; anything else is the
//     "new" side) — the SAME markers region-select's data-surface="code" reads.
//   - within a line, the selectable text lives in a descendant marked
//     `data-line-text` (so a gutter / line-number prefix is excluded from the
//     column count); if absent, the line element itself is the text container.
//
// Offsets are RUNE counts (Unicode code points) into the text container's
// textContent, which the host must keep equal to the raw source line so the
// server can re-locate the span by splitting the raw line at [fromCol, toCol).
//
// Trigger: a single debounced document `selectionchange` — uniform across
// mouse / touch / keyboard (pointerup/keyup miss touch-handle drags and
// keyboard selection). Non-collapsed + settled selection inside the host →
// show a floating "Comment" button at the selection; collapsed → hide it.
interface TextSelectEntry {
  action: string;
  cleanup: () => void;
  updateSend: (send: AreaSelectSendFn) => void;
  // sync re-draws the block caret after each render (morphdom wipes the overlay
  // and the is-cursor line may have moved).
  sync: () => void;
}

const textSelectArmed = new Map<Element, TextSelectEntry>();

// A character range within (or across) source lines. side undefined == "new".
type TextRange = {
  fromLine: number;
  fromCol: number;
  toLine: number;
  toCol: number;
  side?: string;
  text: string;
};

const TEXT_SELECT_BUTTON_ATTR = "data-lvt-text-select-button";
const TEXT_SELECT_DEBOUNCE_MS = 150;

// runeLen counts Unicode code points, not UTF-16 code units, so it matches
// Go's []rune length: a surrogate-pair char (e.g. an emoji) is one column on
// both the client and the server.
function runeLen(s: string): number {
  return [...s].length;
}

// lineTextContainer walks up from a selection-boundary node to its
// `[data-line]` row and returns the row plus its text container (the
// `[data-line-text]` descendant, or the row itself). Returns null when the
// boundary is outside the host, outside any line, or inside a line but outside
// its text container (e.g. a drag that started in the gutter) — the caller then
// bails rather than compute a column against the wrong element.
function lineTextContainer(
  node: Node | null,
  host: Element
): { line: Element; content: Element } | null {
  const start: Element | null =
    node && node.nodeType === Node.TEXT_NODE
      ? node.parentElement
      : (node as Element | null);
  if (!start || !host.contains(start)) return null;
  const line = start.closest("[data-line]");
  if (!line || !host.contains(line)) return null;
  const content = line.querySelector("[data-line-text]") ?? line;
  // `contains` is reflexive (an element contains itself), so this also holds
  // when the boundary sits directly on the content element.
  if (!content.contains(start)) return null;
  return { line, content };
}

// colWithin returns the rune offset of (node, offset) from the start of
// content, or -1 if the boundary is not inside content.
function colWithin(content: Element, node: Node, offset: number): number {
  const r = document.createRange();
  r.selectNodeContents(content);
  try {
    r.setEnd(node, offset);
  } catch {
    return -1;
  }
  return runeLen(r.toString());
}

// blockRangeFromSelection resolves a native selection over a RENDERED surface
// (Markdown/HTML preview, host data-surface="block") to a SOURCE line range +
// the selected text. Rendered text can't map to source columns (`**b**` renders
// as `b`), so columns are 0 and the range is the [data-from, data-to] span of
// the blocks the selection touches — with the exact rendered phrase captured as
// `text` (quoted in the comment and used for drift/context). Rendered content is
// always the new side.
function blockRangeFromSelection(sel: Selection, host: Element): TextRange | null {
  const range = sel.getRangeAt(0); // doc-ordered: start precedes end
  const blockOf = (node: Node | null): Element | null => {
    const start = node && node.nodeType === Node.TEXT_NODE ? node.parentElement : (node as Element | null);
    if (!start || !host.contains(start)) return null;
    const b = start.closest("[data-from]");
    return b && host.contains(b) ? b : null;
  };
  const startBlock = blockOf(range.startContainer);
  const endBlock = blockOf(range.endContainer);
  if (!startBlock || !endBlock) return null;
  const from = parseInt(startBlock.getAttribute("data-from") || "", 10);
  const to = parseInt(endBlock.getAttribute("data-to") || "", 10);
  if (!Number.isFinite(from) || !Number.isFinite(to) || from < 1 || to < from) return null;
  const text = range.toString();
  if (text.length === 0) return null;
  return { fromLine: from, fromCol: 0, toLine: to, toCol: 0, side: "new", text };
}

// textOffsetsFromSelection resolves a native selection to a source range + the
// selected text, or null when it can't be represented. On the default "code"
// surface it yields a character range (rune columns) against [data-line] rows;
// on a "block" surface (host data-surface="block", rendered Markdown/HTML) it
// yields a line range against [data-from]/[data-to] blocks. Exported so unit
// tests exercise the real resolver rather than a copy.
export function textOffsetsFromSelection(
  sel: Selection | null,
  host: Element
): TextRange | null {
  if (!sel || sel.rangeCount === 0 || sel.isCollapsed) return null;
  if (host.getAttribute("data-surface") === "block") {
    return blockRangeFromSelection(sel, host);
  }
  const range = sel.getRangeAt(0); // normalized: start precedes end in doc order
  const startCtx = lineTextContainer(range.startContainer, host);
  const endCtx = lineTextContainer(range.endContainer, host);
  if (!startCtx || !endCtx) return null;
  const startRow = readCodeRange(startCtx.line);
  const endRow = readCodeRange(endCtx.line);
  if (!startRow || !endRow) return null;
  // A diff's old and new sides are separate content streams; a single
  // character range can't span them. (ctx + add lines are both "new", so only
  // a del/new boundary crosses — a rare drag, safe to drop.)
  if ((startRow.side ?? "new") !== (endRow.side ?? "new")) return null;
  const fromCol = colWithin(startCtx.content, range.startContainer, range.startOffset);
  const toCol = colWithin(endCtx.content, range.endContainer, range.endOffset);
  if (fromCol < 0 || toCol < 0) return null;
  const text = range.toString();
  if (text.length === 0) return null;
  return {
    fromLine: startRow.from,
    fromCol,
    toLine: endRow.from,
    toCol,
    side: startRow.side,
    text,
  };
}

// selectionInsideHost reports whether the current selection's common ancestor
// is within host (so a selection that starts in the diff but ends in a comment
// card, or lives elsewhere on the page, is ignored).
function selectionInsideHost(sel: Selection | null, host: Element): boolean {
  if (!sel || sel.rangeCount === 0) return false;
  return host.contains(sel.getRangeAt(0).commonAncestorContainer);
}

function attachTextSelect(
  el: HTMLElement,
  action: string,
  initialSend: AreaSelectSendFn
): TextSelectEntry {
  let send = initialSend;
  // Surface: "code" (diff/raw — character ranges + a keyboard block caret) or
  // "block" (rendered Markdown/HTML — line ranges, no caret, arrows left alone).
  const surface = el.getAttribute("data-surface") === "block" ? "block" : "code";
  let button: HTMLButtonElement | null = null;
  // iOS dismisses a non-editable selection when you tap outside it, and the
  // page can't stop it, so tapping the button collapses the very selection it
  // is for. Hence the button commits `pending`, the range it was shown for,
  // and while a touch or pen press is on it (`pressing`) a collapse doesn't
  // hide it, or the release would land on whatever is underneath.
  let pending: TextRange | null = null;
  let pressing = false;
  let debounce: ReturnType<typeof setTimeout> | null = null;
  // Keyboard block caret — a client-only visual overlay over one character.
  // Its LINE follows the server's is-cursor line (Up/Down move it via the
  // existing CursorUp/Down); its COLUMN is client-owned (Left/Right). Selection
  // (Shift+Arrow) extends from the caret via getSelection().modify() on the
  // non-editable text — no contenteditable needed.
  let caretEl: HTMLElement | null = null;
  let caretCol = 0; // current rune column of the caret
  let goalCol = 0; // remembered column for vertical (line) moves
  let selecting = false; // a Shift-selection is in progress (caret hidden)
  let lastLine: Element | null = null; // detect line changes to restore goalCol

  const hideButton = () => {
    pending = null;
    pressing = false;
    if (button) {
      button.remove();
      button = null;
    }
  };

  // ── caret geometry helpers ──
  const runesOf = (s: string): string[] => [...s];
  const lineContentOf = (line: Element): HTMLElement =>
    line.querySelector<HTMLElement>("[data-line-text]") ?? (line as HTMLElement);
  const lineRuneLen = (content: Element): number =>
    runesOf(content.textContent ?? "").length;

  // activeCursorLine is the line the caret sits on: the server is-cursor line
  // when present, else the first line — so a caret is visible from page load.
  const activeCursorLine = (): Element | null =>
    el.querySelector("[data-line].is-cursor") ?? el.querySelector("[data-line]");

  // nodePointAtRune maps a rune column within content to a DOM (node, offset)
  // point (offset is UTF-16, as the DOM requires), clamped to the content end.
  const nodePointAtRune = (
    content: Element,
    col: number
  ): { node: Node; offset: number } => {
    const walker = document.createTreeWalker(content, NodeFilter.SHOW_TEXT);
    let acc = 0;
    let last: Text | null = null;
    let n: Node | null;
    while ((n = walker.nextNode())) {
      const t = n as Text;
      last = t;
      const runes = runesOf(t.data);
      if (acc + runes.length >= col) {
        return { node: t, offset: runes.slice(0, col - acc).join("").length };
      }
      acc += runes.length;
    }
    return last ? { node: last, offset: last.length } : { node: content, offset: 0 };
  };

  // caretCharRect is the viewport rect of the character cell at col (the block
  // the caret paints over), or the whole (empty) content box for a blank line.
  const caretCharRect = (content: Element, col: number, len: number): DOMRect | null => {
    const r = document.createRange();
    if (len === 0) {
      // Empty line: no character to sit on — a collapsed range rect can be
      // zero-height, so use the content box and paint a thin bar at its start.
      const cr = content.getBoundingClientRect();
      return new DOMRect(cr.left, cr.top, 2, cr.height || 16);
    }
    const a = nodePointAtRune(content, col);
    const b = nodePointAtRune(content, col + 1);
    try {
      r.setStart(a.node, a.offset);
      r.setEnd(b.node, b.offset);
    } catch {
      return null;
    }
    const rects = r.getClientRects();
    return rects.length ? rects[0] : r.getBoundingClientRect();
  };

  const removeCaret = () => {
    if (caretEl) {
      caretEl.remove();
      caretEl = null;
    }
  };

  // renderCaret (re)draws the block caret over the character at (active line,
  // caretCol). Called on every render (sync) and after each horizontal move.
  // Hidden while a selection is in progress — the native highlight shows then.
  const renderCaret = () => {
    const line = activeCursorLine();
    if (!line || selecting) {
      removeCaret();
      lastLine = line;
      return;
    }
    const content = lineContentOf(line);
    const len = lineRuneLen(content);
    if (line !== lastLine) {
      // Arrived on a new line (Up/Down): restore the remembered goal column.
      caretCol = Math.min(goalCol, Math.max(len - 1, 0));
    }
    lastLine = line;
    caretCol = Math.max(0, Math.min(caretCol, Math.max(len - 1, 0)));
    const rect = caretCharRect(content, caretCol, len);
    if (!rect) {
      removeCaret();
      return;
    }
    if (!caretEl) {
      caretEl = document.createElement("div");
      caretEl.setAttribute("data-lvt-text-caret", "");
      caretEl.setAttribute("aria-hidden", "true");
    }
    if (caretEl.parentElement !== el) el.appendChild(caretEl);
    const host = el.getBoundingClientRect();
    caretEl.style.cssText =
      "position:absolute;pointer-events:none;z-index:var(--lvt-text-caret-z-index,4);" +
      "border-radius:2px;background:var(--lvt-text-caret-bg,rgba(31,111,235,0.4));" +
      "box-shadow:var(--lvt-text-caret-shadow,0 0 0 1px var(--lvt-text-caret-border,#1f6feb));" +
      `left:${rect.left - host.left + el.scrollLeft}px;` +
      `top:${rect.top - host.top + el.scrollTop}px;` +
      `width:${Math.max(rect.width, 2)}px;height:${rect.height}px;`;
  };

  // collapseSelectionAtCaret puts a collapsed selection at the caret so
  // modify("extend", …) starts the selection FROM the caret (not line start).
  const collapseSelectionAtCaret = () => {
    const line = activeCursorLine();
    if (!line) return;
    const p = nodePointAtRune(lineContentOf(line), caretCol);
    const sel = window.getSelection();
    if (!sel) return;
    const r = document.createRange();
    r.setStart(p.node, p.offset);
    r.collapse(true);
    sel.removeAllRanges();
    sel.addRange(r);
  };

  // colOfCaret reads a collapsed selection's column back as a rune offset (used
  // after a word move, where the browser's engine found the boundary).
  const colOfCaret = (content: Element): number => {
    const sel = window.getSelection();
    if (!sel || sel.rangeCount === 0) return -1;
    const r = document.createRange();
    r.selectNodeContents(content);
    try {
      r.setEnd(sel.getRangeAt(0).startContainer, sel.getRangeAt(0).startOffset);
    } catch {
      return -1;
    }
    return runesOf(r.toString()).length;
  };

  const isEditableFocus = (): boolean => {
    const a = document.activeElement as HTMLElement | null;
    if (!a) return false;
    const tag = a.tagName;
    return tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT" || a.isContentEditable;
  };

  // moveCaret shifts the caret one character/word and re-renders it; any active
  // selection collapses.
  const moveCaret = (dir: "forward" | "backward", granularity: "character" | "word") => {
    selecting = false;
    hideButton();
    const line = activeCursorLine();
    if (!line) return;
    const content = lineContentOf(line);
    const len = lineRuneLen(content);
    if (granularity === "word") {
      collapseSelectionAtCaret();
      const sel = window.getSelection();
      if (sel && typeof sel.modify === "function") {
        sel.modify("move", dir, "word");
        const c = colOfCaret(content);
        if (c >= 0) caretCol = c;
      }
      window.getSelection()?.removeAllRanges();
    } else {
      caretCol += dir === "forward" ? 1 : -1;
    }
    caretCol = Math.max(0, Math.min(caretCol, Math.max(len - 1, 0)));
    goalCol = caretCol;
    renderCaret();
  };

  // extendSelection grows a native selection from the caret with modify() — the
  // browser's text engine handles word boundaries — over the NON-editable text.
  const extendSelection = (
    dir: "forward" | "backward",
    granularity: "character" | "word"
  ) => {
    if (!activeCursorLine()) return;
    if (!selecting) {
      collapseSelectionAtCaret(); // anchor at the caret
      selecting = true;
      removeCaret(); // native highlight replaces the block caret
    }
    const sel = window.getSelection();
    if (sel && typeof sel.modify === "function") {
      sel.modify("extend", dir, granularity);
    }
    // selectionchange → evaluate → the Comment button appears.
  };

  // confirmSelection dispatches a text comment and resets — shared by the
  // floating button (`pending`) and Enter (the live selection: a fast
  // Shift+Arrow, Enter can beat the debounce that refreshes `pending`).
  const confirmSelection = (data: TextRange | null) => {
    if (data) send({ action, data });
    hideButton();
    selecting = false;
    window.getSelection()?.removeAllRanges();
    // The caret re-renders after the server's render (via sync()).
  };

  // commitButton sends `pending` once: hideButton clears it, so the click that
  // follows a touch release is a no-op.
  const commitButton = () => {
    if (pending) confirmSelection(pending);
  };

  // Touch devices show a NATIVE selection callout (iOS "Copy | Look Up …") in
  // an OS layer that beats any z-index, placed above or below the selection
  // depending on where it sits on screen, which the page can't know. So on a
  // coarse pointer the button is pinned to the bottom centre of the viewport,
  // out of that callout's reach. A mouse keeps it just above the selection.
  // One live MediaQueryList per host; `.matches` tracks device changes.
  const coarsePointer =
    typeof window.matchMedia === "function" ? window.matchMedia("(pointer: coarse)") : null;

  const showButton = (range: Range) => {
    if (!button) {
      const b = document.createElement("button");
      button = b;
      b.type = "button";
      b.setAttribute(TEXT_SELECT_BUTTON_ATTR, "");
      b.textContent = "Comment";
      // preventDefault on pointerdown keeps a desktop selection alive (a default
      // mousedown would move focus and collapse it), so a mouse press needs no
      // `pressing` and commits on click, as before.
      b.addEventListener("pointerdown", (e) => {
        e.preventDefault();
        if (e.pointerType === "mouse") return;
        pressing = true;
        // Refresh `pending` from the live selection if it's still intact: a
        // handle drag within the debounce hasn't reached evaluate yet.
        const live = textOffsetsFromSelection(window.getSelection(), el);
        if (live) pending = live;
        // Touch is captured implicitly; capture pen too, so its release always
        // reaches the button even after sliding off.
        try {
          b.setPointerCapture?.(e.pointerId);
        } catch {
          // no active pointer with that id (synthetic event): nothing to capture
        }
      });
      // Touch and pen commit on a release INSIDE the button, so the tap works
      // even if the platform drops the click after dismissing the selection;
      // sliding off cancels. Either way the press is over: re-evaluate.
      const release = (e: PointerEvent) => {
        if (e.pointerType === "mouse") return;
        pressing = false;
        const r = b.getBoundingClientRect();
        const inside =
          e.type === "pointerup" &&
          e.clientX >= r.left && e.clientX <= r.right && e.clientY >= r.top && e.clientY <= r.bottom;
        if (inside) commitButton();
        else evaluate();
      };
      b.addEventListener("pointerup", release);
      b.addEventListener("pointercancel", release);
      b.addEventListener("click", (e) => {
        e.preventDefault();
        e.stopPropagation();
        commitButton();
      });
      document.body.appendChild(b);
    }
    // Fixed to the viewport (placement: see coarsePointer). Visual look is a
    // sensible default overridable via --lvt-text-select-* custom props
    // (mirrors area-select's --lvt-area-select-* convention). The overlay lives
    // on document.body (outside the themed subtree), so it can't inherit host
    // theme tokens — hence concrete fallbacks here.
    let position: string;
    if (coarsePointer?.matches) {
      position =
        "left:50%;bottom:calc(env(safe-area-inset-bottom, 0px) + var(--lvt-text-select-bottom, 16px));transform:translateX(-50%);";
    } else {
      const box = range.getBoundingClientRect(); // only a beside-the-selection button needs layout
      position = `left:${Math.round(box.left + box.width / 2)}px;top:${Math.round(box.top)}px;transform:translate(-50%,calc(-100% - 6px));`;
    }
    button.style.cssText =
      "position:fixed;z-index:var(--lvt-text-select-z-index,9999);margin:0;" +
      "padding:var(--lvt-text-select-pad,10px 20px);cursor:pointer;white-space:nowrap;" +
      "font:var(--lvt-text-select-font,600 16px/1.2 system-ui,-apple-system,sans-serif);" +
      "color:var(--lvt-text-select-fg,#fff);background:var(--lvt-text-select-bg,#1f6feb);" +
      "border:var(--lvt-text-select-border,0);border-radius:var(--lvt-text-select-radius,9px);" +
      "box-shadow:var(--lvt-text-select-shadow,0 4px 16px rgba(0,0,0,0.35));" +
      position;
  };

  const evaluate = () => {
    const sel = window.getSelection();
    const range = selectionInsideHost(sel, el) ? textOffsetsFromSelection(sel, el) : null;
    if (!range) {
      if (!pressing) hideButton(); // mid-press, the release decides
      return;
    }
    pending = range;
    showButton(sel!.getRangeAt(0));
  };

  const onSelectionChange = () => {
    if (debounce !== null) clearTimeout(debounce);
    debounce = setTimeout(evaluate, TEXT_SELECT_DEBOUNCE_MS);
  };

  // Capture-phase click guard: when a non-collapsed selection lives inside the
  // host, swallow the click so the delegated (document-bubble) lvt-on:click on
  // a line never fires — a drag-select's completing click would otherwise
  // select the whole line. Capture on the host runs before the document-bubble
  // delegation, and stopPropagation prevents the bubble entirely, so the line
  // action is suppressed while the selection (and our button) survive.
  const onClickCapture = (e: MouseEvent) => {
    const sel = window.getSelection();
    if (sel && !sel.isCollapsed && selectionInsideHost(sel, el)) {
      e.stopPropagation();
    }
  };

  // Single window-capture key handler — fires before the document-bubble
  // lvt-on:keydown="selectLine" AND regardless of focus, so the caret works
  // without stealing focus:
  //   - Left/Right → move the block caret (Ctrl/Alt = word); Up/Down fall
  //     through to the server's CursorUp/Down (is-cursor line move; the caret
  //     re-syncs after that render).
  //   - Shift+Left/Right → extend a selection from the caret.
  //   - Enter with a live selection → commit it (never selectLine).
  //   - Escape → drop the selection, restore the caret.
  // While a text field (composer / filter) is focused, arrows are left to it.
  const onKeydown = (e: KeyboardEvent) => {
    // Bail entirely while a text field (composer / filter) is focused: this is a
    // window-CAPTURE listener, so without this an Enter meant as a textarea
    // newline — or an Escape meant to dismiss a field — would be hijacked to
    // commit/cancel a still-live document selection (e.g. select via Shift+
    // Arrow, then Tab into the composer without clearing the selection).
    if (isEditableFocus()) return;
    // Scope to interactions that belong to THIS diff. Since it's a window-
    // capture listener, without this it would swallow Left/Right/Enter/Escape
    // for the whole page (breaking an unrelated arrow-key widget — dropdown,
    // carousel — the user has focused). Handle only when nothing else has focus
    // (page-load / body state, so the on-load caret is still keyboard-drivable)
    // or focus is within the host.
    const a = document.activeElement;
    const scoped =
      !a || a === document.body || a === document.documentElement || el.contains(a);
    if (!scoped) return;
    const sel = window.getSelection();
    const haveSel = !!sel && !sel.isCollapsed && selectionInsideHost(sel, el);
    if (e.key === "Enter" && haveSel) {
      e.preventDefault();
      e.stopPropagation();
      confirmSelection(textOffsetsFromSelection(sel, el));
      return;
    }
    if (e.key === "Escape" && (selecting || haveSel)) {
      e.preventDefault();
      e.stopPropagation();
      selecting = false;
      window.getSelection()?.removeAllRanges();
      hideButton();
      renderCaret();
      return;
    }
    // Arrow-key caret is a code-surface affordance only. On a rendered "block"
    // surface there's no caret, so leave arrows to the browser (scroll) rather
    // than hijacking them.
    if (surface !== "code") return;
    const dir =
      e.key === "ArrowRight" ? "forward" : e.key === "ArrowLeft" ? "backward" : null;
    if (!dir) return; // Up/Down → server cursor; everything else ignored
    const granularity: "character" | "word" = e.ctrlKey || e.altKey ? "word" : "character";
    e.preventDefault();
    e.stopPropagation();
    if (e.shiftKey) extendSelection(dir, granularity);
    else moveCaret(dir, granularity);
  };

  document.addEventListener("selectionchange", onSelectionChange);
  el.addEventListener("click", onClickCapture, true);
  // NOTE: caret state (activeCursorLine/caretCol/selecting) is per-instance, and
  // each armed element adds its own window keydown listener. This assumes ONE
  // armed text-select host at a time (the diff view's single `.code`). If two are
  // ever armed simultaneously, each keystroke would drive both carets, since
  // stopPropagation doesn't stop sibling listeners on the same window target.
  window.addEventListener("keydown", onKeydown, true);

  return {
    action,
    cleanup: () => {
      document.removeEventListener("selectionchange", onSelectionChange);
      el.removeEventListener("click", onClickCapture, true);
      window.removeEventListener("keydown", onKeydown, true);
      if (debounce !== null) clearTimeout(debounce);
      removeCaret();
      hideButton();
      textSelectArmed.delete(el);
    },
    sync: renderCaret,
    updateSend: (s) => {
      send = s;
    },
  };
}

/**
 * Apply text-select directives. `lvt-fx:text-select="<actionName>"` arms a
 * container so a native text selection over its line-bearing children shows a
 * floating "Comment" button that dispatches the character range on click.
 * Idempotent + swept exactly like area/region-select (Map, not WeakMap, so the
 * sweep can iterate to tear down elements whose attribute a server diff
 * removed).
 */
export function handleTextSelectDirectives(
  rootElement: Element,
  send: AreaSelectSendFn
): void {
  for (const [element, entry] of Array.from(textSelectArmed)) {
    if (!element.isConnected || !element.hasAttribute("lvt-fx:text-select")) {
      entry.cleanup();
    }
  }
  const matches = rootElement.querySelectorAll<HTMLElement>("[lvt-fx\\:text-select]");
  if (matches.length === 0) return;
  for (const el of matches) {
    const action = el.getAttribute("lvt-fx:text-select");
    if (!action) {
      console.warn(
        `lvt-fx:text-select requires an action name, got: ${JSON.stringify(action)}`
      );
      continue;
    }
    const existing = textSelectArmed.get(el);
    if (existing && existing.action === action) {
      existing.updateSend(send);
      existing.sync(); // re-draw the caret after this render
      continue;
    }
    if (existing) existing.cleanup();
    const entry = attachTextSelect(el, action, send);
    textSelectArmed.set(el, entry);
    entry.sync(); // draw the caret on first mount (visible from load)
  }
}

/**
 * Cancel text-select listeners for every armed element under root.
 * Mirror of teardownRegionSelectForRoot for the client disconnect lifecycle.
 */
export function teardownTextSelectForRoot(rootElement: Element): void {
  for (const [element, entry] of Array.from(textSelectArmed)) {
    if (rootElement.contains(element)) {
      entry.cleanup();
    }
  }
}

// ─── viewport-report (read-progress reporting) ──────────────────────────────
// `lvt-fx:viewport-report="<action>"` on a SCROLL CONTAINER reports, debounced,
// the `data-key` of the topmost and bottommost tracked descendant currently
// intersecting the container's viewport, so the SERVER can track how far the
// user has read and where they left off. Deliberately DUMB — it only reports
// keys; all logic (read-range accumulation, high-water mark, restore target)
// lives server-side, which keeps it a general primitive and makes the behaviour
// unit-testable without a browser.
//
// Which descendants count is chosen by `data-lvt-viewport-items` (a CSS
// selector, default "[data-key]"); each must carry a `data-key`. A report fires
// on scroll (debounced), once on attach (the initial viewport), and on resize.
// The payload is `{ topKey, bottomKey }`; a report is suppressed while the
// visible extremes are unchanged.
type ViewportReportEntry = {
  action: string;
  cleanup: () => void;
  updateSend: (send: AreaSelectSendFn) => void;
};
const viewportReportArmed = new Map<Element, ViewportReportEntry>();
const VIEWPORT_REPORT_DEBOUNCE_MS = 250;

function attachViewportReport(
  container: HTMLElement,
  action: string,
  initialSend: AreaSelectSendFn
): ViewportReportEntry {
  let send = initialSend;
  const itemSelector =
    container.getAttribute("data-lvt-viewport-items") || "[data-key]";
  let debounceTimer: ReturnType<typeof setTimeout> | null = null;
  let lastTop = "";
  let lastBottom = "";

  const compute = () => {
    const view = container.getBoundingClientRect();
    let topKey = "";
    let bottomKey = "";
    // Items are in document order, so the FIRST with any vertical overlap is the
    // topmost visible and the LAST is the bottommost.
    container.querySelectorAll<HTMLElement>(itemSelector).forEach((it) => {
      const key = it.getAttribute("data-key");
      if (!key) return;
      const r = it.getBoundingClientRect();
      if (r.bottom <= view.top || r.top >= view.bottom) return; // fully off-screen
      if (topKey === "") topKey = key;
      bottomKey = key;
    });
    if (topKey === "" && bottomKey === "") return; // nothing tracked in view yet
    if (topKey === lastTop && bottomKey === lastBottom) return; // no change
    lastTop = topKey;
    lastBottom = bottomKey;
    send({ action, data: { topKey, bottomKey } });
  };

  const schedule = () => {
    if (debounceTimer !== null) clearTimeout(debounceTimer);
    debounceTimer = setTimeout(compute, VIEWPORT_REPORT_DEBOUNCE_MS);
  };

  container.addEventListener("scroll", schedule, { passive: true });
  window.addEventListener("resize", schedule, { passive: true });
  schedule(); // capture the initial viewport once layout settles

  return {
    action,
    updateSend: (s) => {
      send = s;
    },
    cleanup: () => {
      if (debounceTimer !== null) clearTimeout(debounceTimer);
      container.removeEventListener("scroll", schedule);
      window.removeEventListener("resize", schedule);
      viewportReportArmed.delete(container);
    },
  };
}

/**
 * Apply `lvt-fx:viewport-report` directives: arm each matching scroll container
 * once (idempotent across re-renders — an existing entry with the same action
 * just refreshes its send callback), and sweep entries whose element left the
 * DOM or lost the attribute.
 */
export function handleViewportReportDirectives(
  rootElement: Element,
  send: AreaSelectSendFn
): void {
  for (const [element, entry] of Array.from(viewportReportArmed)) {
    if (
      !element.isConnected ||
      !element.hasAttribute("lvt-fx:viewport-report")
    ) {
      entry.cleanup();
    }
  }
  rootElement
    .querySelectorAll<HTMLElement>("[lvt-fx\\:viewport-report]")
    .forEach((el) => {
      const action = el.getAttribute("lvt-fx:viewport-report");
      if (!action) {
        console.warn("lvt-fx:viewport-report requires an action name");
        return;
      }
      const existing = viewportReportArmed.get(el);
      if (existing && existing.action === action) {
        existing.updateSend(send);
        return;
      }
      if (existing) existing.cleanup();
      viewportReportArmed.set(el, attachViewportReport(el, action, send));
    });
}

/** Cancel viewport-report listeners for every armed element under root. */
export function teardownViewportReportForRoot(rootElement: Element): void {
  for (const [element, entry] of Array.from(viewportReportArmed)) {
    if (rootElement.contains(element)) entry.cleanup();
  }
}

// proxyBridgeArmed mirrors the area/region-select singletons: one entry per
// armed `lvt-fx:proxy-bridge` element, swept on disconnect. sync runs on every
// render to re-apply the pin-layer transform morphdom wipes + relay a "locate
// this annotation" request to the iframe.
type ProxyBridgeEntry = BoxDragEntry & { sync: () => void };
const proxyBridgeArmed = new Map<Element, ProxyBridgeEntry>();

/**
 * Apply proxy-bridge directives — the `--external` live-site bridge.
 * `lvt-fx:proxy-bridge="<actionName>"` on the preview stage (the element
 * wrapping the proxied `<iframe>` + the `[data-pin-layer]` overlay) listens
 * for the beacon the proxy injects into the cross-origin page (see
 * proxy.go). Each beacon message carries the page's scroll + document size:
 *
 *   - ANY message updates the shared PageMetrics (so the "page" region
 *     resolver can convert a drawn box to document fractions) and re-pins the
 *     saved annotations by sizing the pin layer to the document and
 *     translating it by -scroll — a pure CSS transform, no server round-trip.
 *   - a `nav` message additionally dispatches `<actionName>` with `{url}` so
 *     the server swaps the visible annotation set for the new page.
 *
 * This is the only thing that reaches across the cross-origin boundary, and
 * it's read-only (postMessage in, transform out) — we never touch the
 * proxied page's DOM. Messages from any origin other than the iframe's are
 * ignored.
 *
 * Idempotent + swept exactly like area/region-select.
 */
export function handleProxyBridgeDirectives(
  rootElement: Element,
  send: AreaSelectSendFn
): void {
  for (const [element, entry] of Array.from(proxyBridgeArmed)) {
    if (!element.isConnected || !element.hasAttribute("lvt-fx:proxy-bridge")) {
      entry.cleanup();
    }
  }
  const matches = rootElement.querySelectorAll<HTMLElement>(
    "[lvt-fx\\:proxy-bridge]"
  );
  if (matches.length === 0) return;
  for (const el of matches) {
    const action = el.getAttribute("lvt-fx:proxy-bridge");
    if (!action) {
      console.warn(
        `lvt-fx:proxy-bridge requires an action name, got: ${JSON.stringify(action)}`
      );
      continue;
    }
    const existing = proxyBridgeArmed.get(el);
    if (existing && existing.action === action) {
      existing.updateSend(send);
      existing.sync();
      continue;
    }
    if (existing) existing.cleanup();
    // Enforce exactly one bridge: pageBridgeMetrics is a module-level singleton,
    // so a second bridge would share it and the first cleanup() would null it,
    // blinding the other. External mode renders one stage; a second is a bug
    // (e.g. a transient double-render) — warn and ignore it rather than collide.
    if (proxyBridgeArmed.size > 0) {
      console.warn(
        "lvt-fx:proxy-bridge: external mode expects exactly one bridge; ignoring an additional one (shared page metrics would collide)."
      );
      continue;
    }
    const entry = attachProxyBridge(el, action, send);
    proxyBridgeArmed.set(el, entry);
    entry.sync();
  }
}

/**
 * Cancel proxy-bridge listeners for every armed element under root.
 * Mirror of teardownRegionSelectForRoot for the client disconnect lifecycle.
 */
export function teardownProxyBridgeForRoot(rootElement: Element): void {
  for (const [element, entry] of Array.from(proxyBridgeArmed)) {
    if (rootElement.contains(element)) {
      entry.cleanup();
    }
  }
}

function attachProxyBridge(
  el: HTMLElement,
  action: string,
  initialSend: AreaSelectSendFn
): ProxyBridgeEntry {
  let send = initialSend;
  let lastURL: string | null = null;
  let lastFocusToken: string | null = null;
  const iframe = el.querySelector<HTMLIFrameElement>("iframe");
  const pinLayer = el.querySelector<HTMLElement>("[data-pin-layer]");

  // Only trust messages from the proxied iframe's own origin.
  let expectedOrigin = "";
  try {
    if (iframe && iframe.src) expectedOrigin = new URL(iframe.src).origin;
  } catch {
    expectedOrigin = "";
  }
  // Fail closed: if the origin can't be resolved we can't trust any sender, so
  // reject every inbound message (onMessage) and never broadcast outbound focus
  // (sync). An unresolved origin should be impossible — the server always
  // renders an absolute src — so warn if it happens.
  if (!expectedOrigin) {
    console.warn(
      "lvt-fx:proxy-bridge: could not resolve the iframe origin; ignoring all postMessage traffic"
    );
  }

  // applyPinLayer sizes the pin layer to the document and slides it by -scroll
  // so each marker (percentage-positioned by the server within this layer)
  // tracks its spot on the page. These are INLINE styles set imperatively, so
  // they must be re-applied after every server render too — morphdom diffs the
  // pin-layer against the template (which has no inline style) and would
  // otherwise wipe them, collapsing the layer until the next scroll. See sync().
  const applyPinLayer = () => {
    const m = pageBridgeMetrics;
    if (pinLayer && m && m.docW > 0 && m.docH > 0) {
      pinLayer.style.width = m.docW + "px";
      pinLayer.style.height = m.docH + "px";
      pinLayer.style.transform = `translate(${-m.scrollX}px, ${-m.scrollY}px)`;
    }
  };

  const onMessage = (e: MessageEvent) => {
    if (!expectedOrigin || e.origin !== expectedOrigin) return;
    const d = e.data;
    if (!d || d.__prereview !== true) return;

    pageBridgeMetrics = {
      scrollX: Number(d.scrollX) || 0,
      scrollY: Number(d.scrollY) || 0,
      docW: Number(d.docW) || 0,
      docH: Number(d.docH) || 0,
    };
    applyPinLayer();

    if (d.type === "nav") {
      const url = typeof d.url === "string" ? d.url : "";
      if (url !== lastURL) {
        lastURL = url;
        send({ action, data: { url } });
      }
    }
  };
  window.addEventListener("message", onMessage);

  // sync runs on EVERY server render (after morphdom). It (1) re-applies the
  // pin-layer size/transform that morphdom just wiped, so a saved pin doesn't
  // vanish until the next scroll, and (2) relays a "locate this annotation"
  // request to the iframe: the server stamps el with data-focus-token (bumps
  // each tap), data-focus-url (the annotation's page) and data-focus-y (0..1
  // document fraction); on a changed token we postMessage the iframe, whose
  // beacon navigates there if needed and scrolls the region into view. Posting
  // INTO the iframe is allowed cross-origin (only reads are blocked).
  const sync = () => {
    applyPinLayer();
    const token = el.getAttribute("data-focus-token");
    if (!token || token === lastFocusToken) return;
    lastFocusToken = token;
    const win = iframe?.contentWindow;
    // No "*" fallback — only post to the resolved proxied origin (fail closed).
    if (!win || !expectedOrigin) return;
    const url = el.getAttribute("data-focus-url") || "";
    const y = parseFloat(el.getAttribute("data-focus-y") || "");
    try {
      win.postMessage(
        { __prereviewFocus: true, url, y: Number.isFinite(y) ? y : 0 },
        expectedOrigin
      );
    } catch {
      // contentWindow gone mid-navigation — the next render retries.
    }
  };

  return {
    action,
    cleanup: () => {
      window.removeEventListener("message", onMessage);
      proxyBridgeArmed.delete(el);
      // Intentionally drop the shared metrics on teardown — keep this here:
      // a stale rect from a torn-down bridge must not leak into a new one (the
      // next beacon re-populates it). Do NOT hoist this out to "simplify".
      pageBridgeMetrics = null;
    },
    updateSend: (s) => {
      send = s;
    },
    sync,
  };
}

function attachRegionSelect(
  el: HTMLElement,
  action: string,
  initialSend: AreaSelectSendFn
): BoxDragEntry {
  let send = initialSend;
  const handle = attachBoxDragSelect(
    el,
    regionSelectWarnedParents,
    "lvt-fx:region-select",
    (r) => {
      const data = resolveRegion(el, r);
      if (data) send({ action, data });
    }
  );
  return {
    action,
    cleanup: () => {
      handle.cleanup();
      regionSelectArmed.delete(el);
    },
    updateSend: (s) => {
      send = s;
    },
  };
}

type LineRange = { from: number; to: number; side?: string };
// A region on a live (cross-origin) proxied page: a rectangle in 0..1
// DOCUMENT fractions (not the host-rect fractions the drag spine yields, and
// not a line range — a live app has no source markers). Survives scroll
// because it's anchored to the document, not the viewport.
type RectPayload = { x: number; y: number; w: number; h: number };
type RegionPayload = LineRange | RectPayload | (LineRange & { bx?: number; by?: number; bw?: number; bh?: number });

// PageMetrics is the proxied page's scroll + document size, reported by the
// injected beacon (proxy.go) via postMessage. External mode frames exactly
// one proxied iframe, so a single module-level record is enough; the proxy
// bridge writes it and the "page" region resolver reads it to convert a
// drawn box (viewport space) into document fractions.
type PageMetrics = {
  scrollX: number;
  scrollY: number;
  docW: number;
  docH: number;
};
let pageBridgeMetrics: PageMetrics | null = null;

// previewIframeFor finds the iframe a region overlay covers. The overlay
// is a sibling of its iframe inside a positioned wrapper and is rendered
// immediately AFTER it, so the preceding sibling is the target. Prefer
// that over `wrapper.querySelector("iframe")`, which would silently pick
// the wrong element if the wrapper ever held more than one iframe; fall
// back to the first iframe in the wrapper only if the sibling isn't one.
function previewIframeFor(overlay: HTMLElement): HTMLIFrameElement | null {
  const prev = overlay.previousElementSibling;
  if (prev instanceof HTMLIFrameElement) return prev;
  return (
    overlay.parentElement?.querySelector<HTMLIFrameElement>("iframe") ?? null
  );
}

// resolveRegion hit-tests a drawn box (viewport coords) against the line-
// bearing elements of the overlay's target surface, returning the source
// line-range payload or null when the box hit nothing.
function resolveRegion(el: HTMLElement, box: BoxDragResult): RegionPayload | null {
  const surface = el.getAttribute("data-surface");
  if (surface === "html") {
    const iframe = previewIframeFor(el);
    if (!iframe) return null;
    // Opaque-origin sandbox: we can't read the iframe DOM, so hit-test the
    // block rects the in-iframe bridge posted out (cached by preview-bridge).
    // Null until the first beacon arrives — a too-early drag is a harmless
    // no-op, mirroring the cross-origin "page" branch below.
    const blocks = previewBlockRects.get(iframe);
    if (!blocks || blocks.length === 0) return null;
    const fr = iframe.getBoundingClientRect();
    const range = lineRangeFromBlocks(blocks, box, fr.left, fr.top);
    if (!range) return null;
    // The drawn box rides along so the preview can show where you pointed.
    return { ...range, ...previewBoxFraction(box, fr) };
  }
  if (surface === "code") {
    const root: ParentNode = el.parentElement || document;
    return lineRangeFromBox(
      root.querySelectorAll("[data-line]"),
      box,
      0,
      0,
      readCodeRange
    );
  }
  if (surface === "page") {
    return regionRectFromBox(el, box);
  }
  console.warn(
    `lvt-fx:region-select: unknown data-surface ${JSON.stringify(surface)} (expected "html", "code", or "page")`
  );
  return null;
}

// regionRectFromBox converts a box drawn over the live-site overlay (its
// x/y/w/h are 0..1 fractions of the overlay's rendered rect, which equals the
// iframe's VIEWPORT) into 0..1 fractions of the proxied page's DOCUMENT,
// using the beacon-reported scroll + document size. The overlay is
// cross-origin, so we can't read the iframe's DOM — these metrics are the
// only bridge. Returns null when no metrics have arrived yet (no scroll/nav
// beacon) or the overlay has no size, so a too-early drag is a harmless
// no-op. Exported so unit tests exercise the real conversion math.
export function regionRectFromBox(
  el: HTMLElement,
  box: BoxDragResult
): RectPayload | null {
  const m = pageBridgeMetrics;
  if (!m || m.docW <= 0 || m.docH <= 0) return null;
  const rect = el.getBoundingClientRect();
  if (rect.width <= 0 || rect.height <= 0) return null;
  // The drag spine only ever hands us a positive box, but this is exported —
  // reject a non-positive box rather than letting clampUnit collapse it to 0.
  if (box.w <= 0 || box.h <= 0) return null;
  // box.x/y/w/h are OVERLAY-RELATIVE fractions (0..1 of the overlay's own
  // rect), not viewport-absolute — so the overlay's position on the page
  // doesn't enter the math. The overlay covers the iframe's viewport, so
  // box.x*rect.width is the in-viewport pixel offset; adding the beacon's
  // scroll lifts it into iframe-DOCUMENT pixels, then ÷ doc size → fraction.
  const docLeft = m.scrollX + box.x * rect.width;
  const docTop = m.scrollY + box.y * rect.height;
  return {
    x: clampUnit(docLeft / m.docW),
    y: clampUnit(docTop / m.docH),
    w: clampUnit((box.w * rect.width) / m.docW),
    h: clampUnit((box.h * rect.height) / m.docH),
  };
}

function clampUnit(n: number): number {
  if (!Number.isFinite(n) || n < 0) return 0;
  return n > 1 ? 1 : n;
}

// readHTMLRange reads a rendered-HTML block's source span from its
// data-from / data-to attributes (data-to defaults to data-from).
// Exported so unit tests exercise the real reader, not a copy.
export function readHTMLRange(el: Element): LineRange | null {
  const from = parseInt(el.getAttribute("data-from") || "", 10);
  if (!Number.isFinite(from) || from <= 0) return null;
  const to = parseInt(el.getAttribute("data-to") || "", 10);
  return { from, to: Number.isFinite(to) && to >= from ? to : from };
}

// readCodeRange reads a code row's source line from data-line and its diff
// side from data-side ("old" rows carry it; everything else is "new").
// Exported so unit tests exercise the real reader, not a copy.
export function readCodeRange(el: Element): LineRange | null {
  const n = parseInt(el.getAttribute("data-line") || "", 10);
  if (!Number.isFinite(n) || n <= 0) return null;
  return el.getAttribute("data-side") === "old"
    ? { from: n, to: n, side: "old" }
    : { from: n, to: n };
}

/**
 * lineRangeFromBox intersects a drawn box (viewport coords) with a set of
 * line-bearing candidate elements and returns a single-side line range of
 * those that overlap it — `from` = smallest line, `to` = largest.
 *
 * The side of the TOPMOST intersecting row fixes the range's side, and
 * rows of any OTHER side are then excluded. This matters on a diff: a
 * `del` row's data-line is its OLD line number, an `add`/context row's is
 * its NEW number — two different coordinate systems. A box that strays
 * across the add/del boundary must NOT union those into one range (it
 * would mis-anchor). Restricting to one side mirrors SelectLine's locked-
 * side invariant. Rendered HTML has no sides (all undefined), so every
 * overlapping block contributes.
 *
 * offsetX/offsetY shift each candidate's rect into the box's coordinate
 * space (the iframe's viewport origin for the html surface; 0 for code).
 * Returns null when no candidate overlaps. Exported for unit testing.
 */
export function lineRangeFromBox(
  candidates: ArrayLike<Element>,
  box: { left: number; top: number; right: number; bottom: number },
  offsetX: number,
  offsetY: number,
  readRange: (el: Element) => LineRange | null
): LineRange | null {
  // First pass: collect overlapping rows with their viewport top so the
  // topmost can fix the side before we union (a box can cross the diff's
  // add/del boundary).
  const hits: { top: number; range: LineRange }[] = [];
  for (const el of Array.from(candidates)) {
    const r = el.getBoundingClientRect();
    const top = r.top + offsetY;
    const bottom = r.bottom + offsetY;
    const left = r.left + offsetX;
    const right = r.right + offsetX;
    // Skip candidates the box doesn't overlap at all.
    if (
      right < box.left ||
      left > box.right ||
      bottom < box.top ||
      top > box.bottom
    ) {
      continue;
    }
    const range = readRange(el);
    if (range) hits.push({ top, range });
  }
  return unionHits(hits);
}

/**
 * lineRangeFromBlocks is the cross-origin twin of lineRangeFromBox for the HTML
 * preview: instead of reading the iframe DOM (impossible under an opaque-origin
 * sandbox), it hit-tests the drawn box against block rects POSTED OUT of the
 * iframe by its bridge beacon (PreviewBlock, in iframe-viewport coords).
 * offsetX/offsetY shift each block into the box's coordinate space (the iframe's
 * viewport origin on the parent page). Same overlap + union semantics as
 * lineRangeFromBox; rendered HTML has no diff sides, so every overlap unions.
 * Blocks flagged `wrap` (pure wrappers) don't join the union: when the box hits
 * no content block, the smallest wrapper it touches is the answer instead, so a
 * drag over empty space still resolves. Returns null when nothing overlaps.
 */
// previewBoxFraction converts a box drawn over an HTML preview (viewport
// pixels) into fractions of the preview page as bx/by/bw/bh. The iframe is
// sized to its content, so its rect is the whole page. Exported for tests.
export function previewBoxFraction(
  box: { left: number; top: number; right: number; bottom: number },
  frame: { left: number; top: number; width: number; height: number }
): { bx: number; by: number; bw: number; bh: number } | Record<string, never> {
  if (frame.width <= 0 || frame.height <= 0) return {};
  const x1 = clampUnit((box.left - frame.left) / frame.width);
  const y1 = clampUnit((box.top - frame.top) / frame.height);
  const x2 = clampUnit((box.right - frame.left) / frame.width);
  const y2 = clampUnit((box.bottom - frame.top) / frame.height);
  if (x2 <= x1 || y2 <= y1) return {};
  return { bx: x1, by: y1, bw: x2 - x1, bh: y2 - y1 };
}

export function lineRangeFromBlocks(
  blocks: PreviewBlock[],
  box: { left: number; top: number; right: number; bottom: number },
  offsetX: number,
  offsetY: number
): LineRange | null {
  const hits: { top: number; range: LineRange }[] = [];
  // The smallest wrapper the box touches: the fallback when it hits no content.
  let wrap: { area: number; range: LineRange } | null = null;
  for (const b of blocks) {
    const top = b.top + offsetY;
    const bottom = b.bottom + offsetY;
    const left = b.left + offsetX;
    const right = b.right + offsetX;
    if (
      right < box.left ||
      left > box.right ||
      bottom < box.top ||
      top > box.bottom
    ) {
      continue;
    }
    if (b.wrap) {
      // Smallest area wins; on a tie, the narrower line span (deterministic).
      const area = (right - left) * (bottom - top);
      const span = b.to - b.from;
      if (!wrap || area < wrap.area || (area === wrap.area && span < wrap.range.to - wrap.range.from)) {
        wrap = { area, range: { from: b.from, to: b.to } };
      }
      continue;
    }
    hits.push({ top, range: { from: b.from, to: b.to } });
  }
  if (hits.length === 0 && wrap) {
    return wrap.range;
  }
  return unionHits(hits);
}

/**
 * unionHits collapses the overlapping line-bearing hits into a single-side
 * range: the TOPMOST hit fixes the side (a diff's del rows carry OLD line
 * numbers, add/context rows NEW — two coordinate systems that must not union),
 * then only same-side hits contribute `from`=min / `to`=max. Rendered HTML has
 * no sides (all undefined), so every hit contributes. Shared by lineRangeFromBox
 * (element rects) and lineRangeFromBlocks (posted rects). Null when no hits.
 */
function unionHits(hits: { top: number; range: LineRange }[]): LineRange | null {
  if (hits.length === 0) return null;
  hits.sort((a, b) => a.top - b.top);
  const side = hits[0].range.side;
  let from = Infinity;
  let to = -Infinity;
  for (const { range } of hits) {
    if (range.side !== side) continue; // exclude the other diff side
    if (range.from < from) from = range.from;
    if (range.to > to) to = range.to;
  }
  return side ? { from, to, side } : { from, to };
}

function clampRange(n: number, lo: number, hi: number): number {
  if (!Number.isFinite(n) || n < lo) return lo;
  if (n > hi) return hi;
  return n;
}
