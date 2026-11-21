import {
  handleAreaSelectDirectives,
  handleAutoClickDirectives,
  handleRegionSelectDirectives,
  teardownRegionSelectForRoot,
  handleProxyBridgeDirectives,
  teardownProxyBridgeForRoot,
  regionRectFromBox,
  lineRangeFromBox,
  readHTMLRange,
  readCodeRange,
  handleURLHashDirective,
  teardownAreaSelectForRoot,
  teardownURLHashForRoot,
  teardownAutoClickTimers,
  __resetURLHashUnencodedWarnedForTesting,
} from "../src/directives";

describe("handleAutoClickDirectives", () => {
  beforeEach(() => {
    document.body.innerHTML = "";
    jest.useFakeTimers();
    jest.spyOn(console, "warn").mockImplementation(() => {});
    teardownAutoClickTimers();
  });

  afterEach(() => {
    teardownAutoClickTimers();
    jest.useRealTimers();
    jest.restoreAllMocks();
  });

  it("fires a click on the named descendant button after the delay", () => {
    document.body.innerHTML = `
      <div class="toast" lvt-fx:auto-click="5000:dismissBanner">
        Saved
        <button name="dismissBanner">×</button>
      </div>
    `;
    const btn = document.querySelector(
      'button[name="dismissBanner"]'
    )! as HTMLButtonElement;
    const clickSpy = jest.spyOn(btn, "click");

    handleAutoClickDirectives(document.body);
    expect(clickSpy).not.toHaveBeenCalled();

    jest.advanceTimersByTime(4999);
    expect(clickSpy).not.toHaveBeenCalled();

    jest.advanceTimersByTime(1);
    expect(clickSpy).toHaveBeenCalledTimes(1);
  });

  it("does not re-arm on subsequent renders with the same spec", () => {
    document.body.innerHTML = `
      <div id="toast" lvt-fx:auto-click="100:dismiss">
        <button name="dismiss">×</button>
      </div>
    `;
    const btn = document.querySelector(
      'button[name="dismiss"]'
    )! as HTMLButtonElement;
    const clickSpy = jest.spyOn(btn, "click");

    handleAutoClickDirectives(document.body);
    jest.advanceTimersByTime(50);
    handleAutoClickDirectives(document.body); // re-render mid-wait
    handleAutoClickDirectives(document.body);
    jest.advanceTimersByTime(50);

    expect(clickSpy).toHaveBeenCalledTimes(1);
  });

  it("does not re-fire when element stays in DOM after timer fires", () => {
    // Race: timer fires (sends action via .click()), but the server
    // hasn't yet removed the toast element. A render pass landing
    // before the response would re-arm a fresh timer if the post-fire
    // map entry were cleared, causing a second .click() — silently
    // double-firing the action. Map entry must persist past fire.
    document.body.innerHTML = `
      <div id="toast" lvt-fx:auto-click="100:dismiss">
        <button name="dismiss">×</button>
      </div>
    `;
    const btn = document.querySelector(
      'button[name="dismiss"]'
    )! as HTMLButtonElement;
    const clickSpy = jest.spyOn(btn, "click");

    handleAutoClickDirectives(document.body);
    jest.advanceTimersByTime(100); // fires
    expect(clickSpy).toHaveBeenCalledTimes(1);

    // Server hasn't responded yet — re-render passes happen.
    handleAutoClickDirectives(document.body);
    handleAutoClickDirectives(document.body);
    jest.advanceTimersByTime(500);
    expect(clickSpy).toHaveBeenCalledTimes(1);
  });

  it("cancels timer when the element is removed before firing", () => {
    document.body.innerHTML = `
      <div id="toast" lvt-fx:auto-click="100:dismiss">
        <button name="dismiss">×</button>
      </div>
    `;
    const toast = document.getElementById("toast")!;
    const btn = toast.querySelector(
      'button[name="dismiss"]'
    )! as HTMLButtonElement;
    const clickSpy = jest.spyOn(btn, "click");

    handleAutoClickDirectives(document.body);
    toast.remove();
    jest.advanceTimersByTime(500);

    expect(clickSpy).not.toHaveBeenCalled();
  });

  it("cancels timer when attribute is removed while element stays connected", () => {
    // Server resolves the toast's state (e.g. clears DoneWritten) without
    // removing the wrapper element. Without the attribute sweep, the
    // pending timer would still fire and click a button on a banner the
    // server already considers dismissed.
    document.body.innerHTML = `
      <div id="toast" lvt-fx:auto-click="100:dismiss">
        <button name="dismiss">×</button>
      </div>
    `;
    const toast = document.getElementById("toast")!;
    const btn = toast.querySelector(
      'button[name="dismiss"]'
    )! as HTMLButtonElement;
    const clickSpy = jest.spyOn(btn, "click");

    handleAutoClickDirectives(document.body);
    toast.removeAttribute("lvt-fx:auto-click"); // server cleared intent
    handleAutoClickDirectives(document.body); // next render's sweep
    jest.advanceTimersByTime(500);

    expect(clickSpy).not.toHaveBeenCalled();
  });

  it("re-arms with new spec when delay or button-name changes", () => {
    document.body.innerHTML = `
      <div id="toast" lvt-fx:auto-click="1000:dismiss">
        <button name="dismiss">×</button>
      </div>
    `;
    const toast = document.getElementById("toast")!;
    const btn = toast.querySelector(
      'button[name="dismiss"]'
    )! as HTMLButtonElement;
    const clickSpy = jest.spyOn(btn, "click");

    handleAutoClickDirectives(document.body);
    jest.advanceTimersByTime(500);

    toast.setAttribute("lvt-fx:auto-click", "200:dismiss");
    handleAutoClickDirectives(document.body);

    jest.advanceTimersByTime(199);
    expect(clickSpy).not.toHaveBeenCalled();

    jest.advanceTimersByTime(1);
    expect(clickSpy).toHaveBeenCalledTimes(1);
  });

  it("warns on malformed spec", () => {
    const malformed = [
      "just-text",       // no colon
      "abc:dismiss",     // non-numeric delay
      "100:",            // empty name
      "200abc:dismiss",  // trailing junk on delay — parseInt would lenient-accept 200
      "12.5:dismiss",    // float — \d+ rejects the dot
      "-100:dismiss",    // negative — \d+ rejects the sign
    ];
    for (const spec of malformed) {
      (console.warn as jest.Mock).mockClear();
      document.body.innerHTML = `<div lvt-fx:auto-click="${spec}"></div>`;
      handleAutoClickDirectives(document.body);
      expect(console.warn).toHaveBeenCalledWith(
        expect.stringContaining("lvt-fx:auto-click expects")
      );
    }
  });

  it("ignores when no descendant matches the named selector", () => {
    document.body.innerHTML = `<div lvt-fx:auto-click="100:noSuchButton"></div>`;
    handleAutoClickDirectives(document.body);

    expect(() => jest.advanceTimersByTime(500)).not.toThrow();
  });

  it("accepts digit-prefixed button names", () => {
    // HTML permits names like "1-dismiss". The regex was tightened to
    // `^[\w-]+$` so this no longer fails.
    document.body.innerHTML = `
      <div lvt-fx:auto-click="100:1-dismiss">
        <button name="1-dismiss">×</button>
      </div>
    `;
    const btn = document.querySelector(
      'button[name="1-dismiss"]'
    )! as HTMLButtonElement;
    const clickSpy = jest.spyOn(btn, "click");

    handleAutoClickDirectives(document.body);
    jest.advanceTimersByTime(100);

    expect(clickSpy).toHaveBeenCalledTimes(1);
  });

  it("teardownAutoClickTimers cancels all armed timers", () => {
    document.body.innerHTML = `
      <div id="t1" lvt-fx:auto-click="100:dismiss">
        <button name="dismiss">×</button>
      </div>
      <div id="t2" lvt-fx:auto-click="200:dismiss">
        <button name="dismiss">×</button>
      </div>
    `;
    const btns = Array.from(
      document.querySelectorAll('button[name="dismiss"]')
    ) as HTMLButtonElement[];
    const spies = btns.map((b) => jest.spyOn(b, "click"));

    handleAutoClickDirectives(document.body);
    teardownAutoClickTimers();
    jest.advanceTimersByTime(500);

    for (const spy of spies) expect(spy).not.toHaveBeenCalled();
  });

  it("does not match non-button elements with the same name", () => {
    // The selector is scoped to `button[name=...]` — a checkbox or text
    // input with the same name would otherwise get .click()ed with
    // surprising side effects (toggle / focus) unrelated to the
    // action-submission semantic the directive promises.
    document.body.innerHTML = `
      <div lvt-fx:auto-click="100:dismiss">
        <input type="checkbox" name="dismiss">
      </div>
    `;
    const cb = document.querySelector(
      'input[name="dismiss"]'
    )! as HTMLInputElement;
    const clickSpy = jest.spyOn(cb, "click");

    handleAutoClickDirectives(document.body);
    jest.advanceTimersByTime(500);

    expect(clickSpy).not.toHaveBeenCalled();
  });
});

describe("handleAreaSelectDirectives", () => {
  // The module-level areaSelectArmed map is cleared lazily — the sweep
  // only fires when handleAreaSelectDirectives runs. Without an explicit
  // afterEach, tests that don't call it would inherit armed elements
  // from prior tests. teardownAreaSelectForRoot(document.body) wipes
  // every entry so each test gets a fresh slate.
  afterEach(() => {
    teardownAreaSelectForRoot(document.body);
  });

  // jsdom-friendly helper: configure the target element so it has a
  // non-trivial bounding rect (jsdom returns zeros by default) and a
  // positioned parent so the overlay has somewhere to land. Returns
  // [target, parent] for the assertions.
  function mountTarget(
    targetTag: "img" | "div",
    attrs: Record<string, string>,
    rect: { left: number; top: number; width: number; height: number }
  ): [HTMLElement, HTMLElement] {
    document.body.innerHTML = `
      <div id="parent" style="position:relative;">
        <${targetTag} id="target"></${targetTag}>
      </div>
    `;
    const target = document.getElementById("target") as HTMLElement;
    const parent = document.getElementById("parent") as HTMLElement;
    for (const [k, v] of Object.entries(attrs)) target.setAttribute(k, v);
    target.getBoundingClientRect = jest.fn(() => ({
      x: rect.left,
      y: rect.top,
      left: rect.left,
      top: rect.top,
      right: rect.left + rect.width,
      bottom: rect.top + rect.height,
      width: rect.width,
      height: rect.height,
      toJSON: () => ({}),
    }));
    parent.getBoundingClientRect = jest.fn(() => ({
      x: rect.left,
      y: rect.top,
      left: rect.left,
      top: rect.top,
      right: rect.left + rect.width,
      bottom: rect.top + rect.height,
      width: rect.width,
      height: rect.height,
      toJSON: () => ({}),
    }));
    // jsdom doesn't implement pointer-capture; stub so the directive's
    // try/catch around it doesn't matter, but the test still asserts
    // the contract.
    (target as any).setPointerCapture = jest.fn();
    (target as any).releasePointerCapture = jest.fn();
    return [target, parent];
  }

  function dispatchPointer(
    el: HTMLElement,
    type: "pointerdown" | "pointermove" | "pointerup" | "pointercancel" | "lostpointercapture" | "pointerleave",
    clientX: number,
    clientY: number,
    pointerId = 1
  ): void {
    const e = new MouseEvent(type, { clientX, clientY, button: 0, bubbles: true });
    // PointerEvent isn't fully supported in jsdom but the directive
    // only reads pointerId / isPrimary / button / clientX / clientY.
    Object.defineProperty(e, "pointerId", { value: pointerId });
    Object.defineProperty(e, "isPrimary", { value: true });
    el.dispatchEvent(e);
  }

  beforeEach(() => {
    document.body.innerHTML = "";
    jest.spyOn(console, "warn").mockImplementation(() => {});
  });
  afterEach(() => {
    jest.restoreAllMocks();
  });

  it("dispatches the action with 0..1 fraction coords on pointerup", () => {
    const [target] = mountTarget(
      "img",
      { "lvt-fx:area-select": "selectImageArea" },
      { left: 100, top: 50, width: 200, height: 200 }
    );
    const send = jest.fn();
    handleAreaSelectDirectives(document.body, send);

    // Drag from (120, 80) → (220, 150) inside the rect.
    // x = (120 - 100) / 200 = 0.10
    // y = (80 - 50)   / 200 = 0.15
    // w = (220 - 120) / 200 = 0.50
    // h = (150 - 80)  / 200 = 0.35
    dispatchPointer(target, "pointerdown", 120, 80);
    dispatchPointer(target, "pointermove", 220, 150);
    dispatchPointer(target, "pointerup", 220, 150);

    expect(send).toHaveBeenCalledTimes(1);
    const msg = send.mock.calls[0][0];
    expect(msg.action).toBe("selectImageArea");
    expect(msg.data.x).toBeCloseTo(0.10, 5);
    expect(msg.data.y).toBeCloseTo(0.15, 5);
    expect(msg.data.w).toBeCloseTo(0.50, 5);
    expect(msg.data.h).toBeCloseTo(0.35, 5);
  });

  it("filters drags smaller than the min-fraction threshold", () => {
    const [target] = mountTarget(
      "img",
      { "lvt-fx:area-select": "selectImageArea" },
      { left: 0, top: 0, width: 1000, height: 1000 }
    );
    const send = jest.fn();
    handleAreaSelectDirectives(document.body, send);

    // Drag is 10×10 px on a 1000×1000 rect → 1% fraction in both dims.
    // 1% < MIN_AREA_FRACTION (2%) → must drop.
    dispatchPointer(target, "pointerdown", 100, 100);
    dispatchPointer(target, "pointermove", 110, 110);
    dispatchPointer(target, "pointerup", 110, 110);

    expect(send).not.toHaveBeenCalled();
    // Overlay should have been cleaned up after the failed drag.
    expect(document.querySelectorAll(".lvt-area-select-overlay").length).toBe(0);
  });

  it("paints an overlay during the drag and removes it on release", () => {
    const [, parent] = mountTarget(
      "img",
      { "lvt-fx:area-select": "selectImageArea" },
      { left: 0, top: 0, width: 200, height: 200 }
    );
    const target = parent.querySelector("img")! as HTMLElement;
    const send = jest.fn();
    handleAreaSelectDirectives(document.body, send);

    dispatchPointer(target, "pointerdown", 10, 10);
    expect(parent.querySelector(".lvt-area-select-overlay")).not.toBeNull();

    dispatchPointer(target, "pointermove", 60, 70);
    const overlay = parent.querySelector(".lvt-area-select-overlay") as HTMLDivElement;
    expect(overlay.style.width).toBe("50px");
    expect(overlay.style.height).toBe("60px");

    dispatchPointer(target, "pointerup", 60, 70);
    expect(parent.querySelector(".lvt-area-select-overlay")).toBeNull();
  });

  it("pointercancel removes the overlay and does NOT dispatch", () => {
    const [, parent] = mountTarget(
      "img",
      { "lvt-fx:area-select": "selectImageArea" },
      { left: 0, top: 0, width: 200, height: 200 }
    );
    const target = parent.querySelector("img")! as HTMLElement;
    const send = jest.fn();
    handleAreaSelectDirectives(document.body, send);

    dispatchPointer(target, "pointerdown", 10, 10);
    dispatchPointer(target, "pointermove", 60, 60);
    dispatchPointer(target, "pointercancel", 60, 60);

    expect(send).not.toHaveBeenCalled();
    expect(parent.querySelector(".lvt-area-select-overlay")).toBeNull();
  });

  // dispatchTouch mirrors dispatchPointer but marks the event pointerType=touch
  // so the spine takes its iOS-recovery path (cancel/lost-capture → finalize).
  function dispatchTouch(
    el: HTMLElement,
    type: "pointerdown" | "pointermove" | "pointercancel" | "lostpointercapture",
    clientX: number,
    clientY: number
  ): void {
    const e = new MouseEvent(type, { clientX, clientY, button: 0, bubbles: true });
    Object.defineProperty(e, "pointerId", { value: 1 });
    Object.defineProperty(e, "isPrimary", { value: true });
    Object.defineProperty(e, "pointerType", { value: "touch" });
    el.dispatchEvent(e);
  }

  it("touch pointercancel KEEPS the drawn region (iOS long touch-drag recovery)", () => {
    const [target] = mountTarget(
      "img",
      { "lvt-fx:area-select": "selectImageArea" },
      { left: 0, top: 0, width: 200, height: 200 }
    );
    const send = jest.fn();
    handleAreaSelectDirectives(document.body, send);

    // A large touch-drag that iOS cancels mid-gesture must still capture the
    // region drawn so far — the opposite of the mouse pointercancel above.
    dispatchTouch(target, "pointerdown", 20, 20);
    dispatchTouch(target, "pointermove", 180, 180);
    dispatchTouch(target, "pointercancel", 180, 180);

    expect(send).toHaveBeenCalledTimes(1);
    const msg = send.mock.calls[0][0];
    expect(msg.data.w).toBeCloseTo(0.8, 5); // (180-20)/200
    expect(msg.data.h).toBeCloseTo(0.8, 5);
  });

  it("touch lostpointercapture KEEPS the drawn region", () => {
    const [target] = mountTarget(
      "img",
      { "lvt-fx:area-select": "selectImageArea" },
      { left: 0, top: 0, width: 200, height: 200 }
    );
    const send = jest.fn();
    handleAreaSelectDirectives(document.body, send);

    dispatchTouch(target, "pointerdown", 20, 20);
    dispatchTouch(target, "pointermove", 120, 100);
    dispatchTouch(target, "lostpointercapture", 120, 100);

    expect(send).toHaveBeenCalledTimes(1);
    expect(send.mock.calls[0][0].data.w).toBeCloseTo(0.5, 5); // (120-20)/200
  });

  it("touch lostpointercapture before any move dispatches nothing (zero-area)", () => {
    // If capture is lost before a single pointermove, lastMove is the
    // pointerdown event, so finalize sees a zero-area box — the threshold must
    // reject it, not create a degenerate region.
    const [target] = mountTarget(
      "img",
      { "lvt-fx:area-select": "selectImageArea" },
      { left: 0, top: 0, width: 200, height: 200 }
    );
    const send = jest.fn();
    handleAreaSelectDirectives(document.body, send);

    dispatchTouch(target, "pointerdown", 50, 50);
    dispatchTouch(target, "lostpointercapture", 50, 50);

    expect(send).not.toHaveBeenCalled();
  });

  it("a tiny touch cancel is still dropped (area threshold, no stray region)", () => {
    const [target] = mountTarget(
      "img",
      { "lvt-fx:area-select": "selectImageArea" },
      { left: 0, top: 0, width: 1000, height: 1000 }
    );
    const send = jest.fn();
    handleAreaSelectDirectives(document.body, send);

    // 8×8 px on a 1000×1000 host → below MIN_AREA_FRACTION in both dims.
    dispatchTouch(target, "pointerdown", 10, 10);
    dispatchTouch(target, "pointermove", 18, 18);
    dispatchTouch(target, "pointercancel", 18, 18);

    expect(send).not.toHaveBeenCalled();
  });

  it("is idempotent across renders for the same action", () => {
    const [target] = mountTarget(
      "img",
      { "lvt-fx:area-select": "selectImageArea" },
      { left: 0, top: 0, width: 200, height: 200 }
    );
    const send = jest.fn();

    handleAreaSelectDirectives(document.body, send);
    handleAreaSelectDirectives(document.body, send);
    handleAreaSelectDirectives(document.body, send);

    dispatchPointer(target, "pointerdown", 10, 10);
    dispatchPointer(target, "pointermove", 100, 100);
    dispatchPointer(target, "pointerup", 100, 100);

    // Listeners must NOT have been duplicated by repeated calls.
    expect(send).toHaveBeenCalledTimes(1);
  });

  it("re-arms with new action when the attribute value changes", () => {
    const [target] = mountTarget(
      "img",
      { "lvt-fx:area-select": "first" },
      { left: 0, top: 0, width: 200, height: 200 }
    );
    const send = jest.fn();
    handleAreaSelectDirectives(document.body, send);
    target.setAttribute("lvt-fx:area-select", "second");
    handleAreaSelectDirectives(document.body, send);

    dispatchPointer(target, "pointerdown", 10, 10);
    dispatchPointer(target, "pointermove", 100, 100);
    dispatchPointer(target, "pointerup", 100, 100);

    expect(send).toHaveBeenCalledTimes(1);
    expect(send.mock.calls[0][0].action).toBe("second");
  });

  it("warns and skips when the attribute value is empty", () => {
    const warn = console.warn as jest.Mock;
    mountTarget(
      "img",
      { "lvt-fx:area-select": "" },
      { left: 0, top: 0, width: 200, height: 200 }
    );
    const send = jest.fn();
    handleAreaSelectDirectives(document.body, send);

    expect(warn).toHaveBeenCalledWith(
      expect.stringContaining("requires an action name")
    );
  });

  it("clamps coords to 0..1 when the drag escapes the element rect", () => {
    const [target] = mountTarget(
      "img",
      { "lvt-fx:area-select": "selectImageArea" },
      { left: 100, top: 100, width: 200, height: 200 }
    );
    const send = jest.fn();
    handleAreaSelectDirectives(document.body, send);

    // Drag starts inside but ends far below-right of the rect.
    dispatchPointer(target, "pointerdown", 150, 150);
    dispatchPointer(target, "pointerup", 10000, 10000);

    expect(send).toHaveBeenCalledTimes(1);
    const data = send.mock.calls[0][0].data as Record<string, number>;
    expect(data.x).toBeGreaterThanOrEqual(0);
    expect(data.x).toBeLessThanOrEqual(1);
    expect(data.x + data.w).toBeLessThanOrEqual(1);
    expect(data.y + data.h).toBeLessThanOrEqual(1);
  });

  it("fast-path returns when no matching elements", () => {
    document.body.innerHTML = `<div><p>nothing here</p></div>`;
    const send = jest.fn();
    // Should not throw, should not dispatch.
    expect(() => handleAreaSelectDirectives(document.body, send)).not.toThrow();
    expect(send).not.toHaveBeenCalled();
  });

  it("removes the overlay when the host is detached mid-drag", () => {
    const [target, parent] = mountTarget(
      "img",
      { "lvt-fx:area-select": "selectImageArea" },
      { left: 0, top: 0, width: 200, height: 200 }
    );
    const send = jest.fn();
    handleAreaSelectDirectives(document.body, send);

    dispatchPointer(target, "pointerdown", 10, 10);
    expect(parent.querySelector(".lvt-area-select-overlay")).not.toBeNull();

    // Simulate a server diff replacing the host element.
    target.remove();

    // A late pointermove (jsdom dispatches to the detached element)
    // must clean up the overlay rather than leave it orphaned under
    // the parent.
    dispatchPointer(target, "pointermove", 60, 70);
    expect(parent.querySelector(".lvt-area-select-overlay")).toBeNull();
  });

  it("suppresses native <img> drag via dragstart preventDefault", () => {
    const [target] = mountTarget(
      "img",
      { "lvt-fx:area-select": "selectImageArea" },
      { left: 0, top: 0, width: 200, height: 200 }
    );
    handleAreaSelectDirectives(document.body, jest.fn());

    // Without the dragstart listener, Chromium would call default-
    // action (start a native image drag) and steal the gesture from
    // pointer events. The directive must call preventDefault on
    // dragstart so pointermove + pointerup keep arriving.
    const drag = new Event("dragstart", { bubbles: true, cancelable: true });
    target.dispatchEvent(drag);
    expect(drag.defaultPrevented).toBe(true);
  });

  it("positions overlay correctly when target is offset inside its parent", () => {
    // Parent at (0,0), target at (50, 25) — exercises the
    // border-box-to-padding-box offset math with a real gap.
    document.body.innerHTML = `
      <div id="parent" style="position:relative;">
        <img id="target" lvt-fx:area-select="selectImageArea">
      </div>
    `;
    const target = document.getElementById("target") as HTMLImageElement;
    const parent = document.getElementById("parent") as HTMLDivElement;
    target.getBoundingClientRect = jest.fn(() => ({
      x: 50, y: 25, left: 50, top: 25, right: 150, bottom: 125,
      width: 100, height: 100, toJSON: () => ({}),
    } as DOMRect));
    parent.getBoundingClientRect = jest.fn(() => ({
      x: 0, y: 0, left: 0, top: 0, right: 200, bottom: 200,
      width: 200, height: 200, toJSON: () => ({}),
    } as DOMRect));
    (target as any).setPointerCapture = jest.fn();
    (target as any).releasePointerCapture = jest.fn();
    handleAreaSelectDirectives(document.body, jest.fn());

    // Drag from (80, 50) → (120, 90) — inside the target's 100×100 rect.
    // Relative to the parent (and after subtracting clientLeft/clientTop=0
    // for a borderless parent), the overlay should sit at left=80, top=50.
    dispatchPointer(target, "pointerdown", 80, 50);
    dispatchPointer(target, "pointermove", 120, 90);
    const overlay = parent.querySelector(".lvt-area-select-overlay") as HTMLDivElement;
    expect(overlay).not.toBeNull();
    expect(overlay.style.left).toBe("80px");
    expect(overlay.style.top).toBe("50px");
    expect(overlay.style.width).toBe("40px");
    expect(overlay.style.height).toBe("40px");
  });

  it("pointerleave for a different pointerId does NOT cancel our capture-fallback drag", () => {
    // When setPointerCapture fails the directive attaches a
    // pointerleave fallback so the drag can clean up. In a multi-
    // touch scenario, a SECONDARY pointer leaving the element fires
    // pointerleave too — must not be mistaken for our pointer
    // leaving.
    const [, parent] = mountTarget(
      "img",
      { "lvt-fx:area-select": "selectImageArea" },
      { left: 0, top: 0, width: 200, height: 200 }
    );
    const target = parent.querySelector("img")! as HTMLElement;
    // Force capture failure so the pointerleave fallback is attached.
    (target as any).setPointerCapture = jest.fn(() => {
      throw new DOMException("no capture", "InvalidStateError");
    });
    const send = jest.fn();
    handleAreaSelectDirectives(document.body, send);

    // Primary drag with pointerId=1.
    dispatchPointer(target, "pointerdown", 10, 10, 1);
    dispatchPointer(target, "pointermove", 80, 80, 1);

    // Secondary pointer (id=42) leaves the host. Must NOT cancel
    // our id=1 drag.
    dispatchPointer(target, "pointerleave", 80, 80, 42);

    // Primary drag still alive — pointerup completes it normally.
    dispatchPointer(target, "pointerup", 100, 100, 1);
    expect(send).toHaveBeenCalledTimes(1);
  });

  it("lostpointercapture for a different pointerId does NOT cancel our drag", () => {
    // Another code path could call setPointerCapture on the same
    // element with a different pointerId and later release it,
    // firing lostpointercapture on the host. Our handler must not
    // mistake that for OUR drag being canceled.
    const [, parent] = mountTarget(
      "img",
      { "lvt-fx:area-select": "selectImageArea" },
      { left: 0, top: 0, width: 200, height: 200 }
    );
    const target = parent.querySelector("img")! as HTMLElement;
    const send = jest.fn();
    handleAreaSelectDirectives(document.body, send);

    // Our drag starts with pointerId=1.
    dispatchPointer(target, "pointerdown", 10, 10, 1);
    dispatchPointer(target, "pointermove", 80, 80, 1);

    // Unrelated lostpointercapture for pointerId=42 — must be ignored.
    dispatchPointer(target, "lostpointercapture", 80, 80, 42);

    // Our drag is still alive — pointerup dispatches normally.
    dispatchPointer(target, "pointerup", 100, 100, 1);
    expect(send).toHaveBeenCalledTimes(1);
  });

  it("releasePointerCapture firing lostpointercapture synchronously does not drop the dispatch", () => {
    // Chromium fires lostpointercapture SYNCHRONOUSLY during
    // releasePointerCapture. Without the early state-reset in
    // finalize, the nested lostpointercapture handler would see
    // pointerId still matching, run a nested finalize that clears
    // startRect, then the outer finalize would resume with rect=null
    // and silently drop the dispatched action.
    const [, parent] = mountTarget(
      "img",
      { "lvt-fx:area-select": "selectImageArea" },
      { left: 0, top: 0, width: 200, height: 200 }
    );
    const target = parent.querySelector("img")! as HTMLElement;
    // Make releasePointerCapture fire lostpointercapture synchronously
    // — the real Chromium behaviour that wasn't covered by jest.fn().
    (target as any).releasePointerCapture = jest.fn((pid: number) => {
      target.dispatchEvent(
        Object.assign(new MouseEvent("lostpointercapture", { bubbles: false }), {
          pointerId: pid,
        })
      );
    });
    const send = jest.fn();
    handleAreaSelectDirectives(document.body, send);

    dispatchPointer(target, "pointerdown", 20, 20);
    dispatchPointer(target, "pointermove", 80, 80);
    dispatchPointer(target, "pointerup", 80, 80);

    // The action must still dispatch — the early state-reset prevents
    // the nested lostpointercapture from re-entering finalize and
    // clearing the rect mid-call.
    expect(send).toHaveBeenCalledTimes(1);
  });

  it("uses pointerdown-time rect for fractions even if host moves between mousemoves", () => {
    // If a server diff repositions the host mid-drag, the rect
    // captured AT POINTERUP would clamp startClientX (which was
    // captured against the OLD position) into the wrong place.
    // The dispatched fractions must reflect the original drag,
    // measured against the rect that existed at pointerdown.
    document.body.innerHTML = `
      <div id="parent" style="position:relative;"><img id="target" lvt-fx:area-select="selectImageArea"></div>
    `;
    const parent = document.getElementById("parent") as HTMLDivElement;
    const target = document.getElementById("target") as HTMLImageElement;
    // Rect call counter: first call (pointerdown) returns the OLD
    // rect, subsequent calls return a NEW rect 500px away.
    let rectCalls = 0;
    target.getBoundingClientRect = jest.fn(() => {
      rectCalls++;
      if (rectCalls === 1) {
        return {
          x: 0, y: 0, left: 0, top: 0, right: 200, bottom: 200,
          width: 200, height: 200, toJSON: () => ({}),
        } as DOMRect;
      }
      return {
        x: 500, y: 500, left: 500, top: 500, right: 700, bottom: 700,
        width: 200, height: 200, toJSON: () => ({}),
      } as DOMRect;
    });
    parent.getBoundingClientRect = jest.fn(() => ({
      x: 0, y: 0, left: 0, top: 0, right: 1000, bottom: 1000,
      width: 1000, height: 1000, toJSON: () => ({}),
    } as DOMRect));
    (target as any).setPointerCapture = jest.fn();
    (target as any).releasePointerCapture = jest.fn();
    const send = jest.fn();
    handleAreaSelectDirectives(document.body, send);

    // Drag from (40, 60) → (140, 160) inside the OLD rect at (0..200).
    // OLD-rect fractions: x=40/200=0.2, y=60/200=0.3, w=100/200=0.5,
    // h=100/200=0.5. With the new (500..700) rect at finalize time,
    // clamping (40,60) into that rect would silently shift the start.
    dispatchPointer(target, "pointerdown", 40, 60);
    dispatchPointer(target, "pointermove", 90, 110);
    dispatchPointer(target, "pointerup", 140, 160);

    expect(send).toHaveBeenCalledTimes(1);
    const data = send.mock.calls[0][0].data as Record<string, number>;
    expect(data.x).toBeCloseTo(0.2, 5);
    expect(data.y).toBeCloseTo(0.3, 5);
    expect(data.w).toBeCloseTo(0.5, 5);
    expect(data.h).toBeCloseTo(0.5, 5);
  });

  it("uses the pointerdown-time parent even if host moves between mousemoves", () => {
    // Two positioned parents at known offsets. The host starts under
    // the first; we begin a drag, then synthetically re-parent the
    // host into the second container mid-drag. Without the parent-
    // capture fix, updateOverlay would refetch el.parentElement and
    // compute against the SECOND parent while the overlay still
    // lives in the FIRST — visual mis-positioning for the rest of
    // the drag. With the fix, the overlay tracks the FIRST parent.
    document.body.innerHTML = `
      <div id="p1" style="position:relative;"></div>
      <div id="p2" style="position:relative;"></div>
    `;
    const p1 = document.getElementById("p1") as HTMLDivElement;
    const p2 = document.getElementById("p2") as HTMLDivElement;
    const target = document.createElement("img");
    target.setAttribute("lvt-fx:area-select", "selectImageArea");
    p1.appendChild(target);
    target.getBoundingClientRect = jest.fn(() => ({
      x: 10, y: 10, left: 10, top: 10, right: 110, bottom: 110,
      width: 100, height: 100, toJSON: () => ({}),
    } as DOMRect));
    p1.getBoundingClientRect = jest.fn(() => ({
      x: 0, y: 0, left: 0, top: 0, right: 200, bottom: 200,
      width: 200, height: 200, toJSON: () => ({}),
    } as DOMRect));
    p2.getBoundingClientRect = jest.fn(() => ({
      x: 500, y: 500, left: 500, top: 500, right: 700, bottom: 700,
      width: 200, height: 200, toJSON: () => ({}),
    } as DOMRect));
    (target as any).setPointerCapture = jest.fn();
    (target as any).releasePointerCapture = jest.fn();
    handleAreaSelectDirectives(document.body, jest.fn());

    dispatchPointer(target, "pointerdown", 30, 30);
    // Server diff moves the host to p2.
    p2.appendChild(target);
    dispatchPointer(target, "pointermove", 80, 60);

    // Overlay stays in p1 (where pointerdown attached it). Position is
    // computed against p1's rect (cached at pointerdown), not p2's.
    const overlayInP1 = p1.querySelector(".lvt-area-select-overlay") as HTMLDivElement;
    const overlayInP2 = p2.querySelector(".lvt-area-select-overlay");
    expect(overlayInP1).not.toBeNull();
    expect(overlayInP2).toBeNull();
    // pointerdown at (30,30), move to (80,60) → 50×30 against p1
    // at (0,0). If updateOverlay had re-fetched el.parentElement
    // and used p2 (at 500,500), left would be -470 instead of 30.
    expect(overlayInP1.style.left).toBe("30px");
  });

  it("positions overlay correctly when parent is scrolled", () => {
    // For a scrolled positioned parent: an element at viewport_x =
    // parentRect.left actually has CSS_left = parent.scrollLeft (the
    // browser is scrolled, so what's at viewport-x-0 is parent-x-100
    // for scrollLeft=100). Without adding scrollLeft/scrollTop back
    // into the CSS coords, the overlay paints displaced by the scroll
    // amount.
    document.body.innerHTML = `
      <div id="parent" style="position:relative;"><img id="target" lvt-fx:area-select="selectImageArea"></div>
    `;
    const parent = document.getElementById("parent") as HTMLDivElement;
    const target = document.getElementById("target") as HTMLImageElement;
    target.getBoundingClientRect = jest.fn(() => ({
      x: 0, y: 0, left: 0, top: 0, right: 200, bottom: 200,
      width: 200, height: 200, toJSON: () => ({}),
    } as DOMRect));
    parent.getBoundingClientRect = jest.fn(() => ({
      x: 0, y: 0, left: 0, top: 0, right: 200, bottom: 200,
      width: 200, height: 200, toJSON: () => ({}),
    } as DOMRect));
    // jsdom: scrollLeft/Top are mutable properties; just assign.
    Object.defineProperty(parent, "scrollLeft", { value: 100, configurable: true });
    Object.defineProperty(parent, "scrollTop", { value: 50, configurable: true });
    (target as any).setPointerCapture = jest.fn();
    (target as any).releasePointerCapture = jest.fn();
    handleAreaSelectDirectives(document.body, jest.fn());

    // Drag from viewport (30, 40) to (90, 100). With the scroll
    // correction the overlay's CSS left should be
    // 30 - 0 - 0 + 100 = 130 and CSS top should be
    // 40 - 0 - 0 + 50 = 90. Without it, left=30 / top=40 (the bug).
    dispatchPointer(target, "pointerdown", 30, 40);
    dispatchPointer(target, "pointermove", 90, 100);
    const overlay = parent.querySelector(".lvt-area-select-overlay") as HTMLDivElement;
    expect(overlay).not.toBeNull();
    expect(overlay.style.left).toBe("130px");
    expect(overlay.style.top).toBe("90px");
    expect(overlay.style.width).toBe("60px");
    expect(overlay.style.height).toBe("60px");
  });

  it("warns when the parent's computed position is `static`", () => {
    // Forgetting position:relative on the parent silently mis-paints
    // the overlay against the nearest positioned ancestor. A
    // dev-time warn gives the author a chance to spot the mistake.
    const warn = console.warn as jest.Mock;
    document.body.innerHTML = `
      <div id="static-parent">
        <img id="target" lvt-fx:area-select="selectImageArea">
      </div>
    `;
    const target = document.getElementById("target") as HTMLImageElement;
    target.getBoundingClientRect = jest.fn(() => ({
      x: 0, y: 0, left: 0, top: 0, right: 200, bottom: 200,
      width: 200, height: 200, toJSON: () => ({}),
    } as DOMRect));
    (target as any).setPointerCapture = jest.fn();
    (target as any).releasePointerCapture = jest.fn();
    handleAreaSelectDirectives(document.body, jest.fn());

    dispatchPointer(target, "pointerdown", 10, 10);

    const warnings = warn.mock.calls.filter(
      (args) => typeof args[0] === "string" && args[0].includes("parentElement has no positioning context")
    );
    expect(warnings.length).toBe(1);
  });

  it("dedupes the static-parent warn across repeated drags", () => {
    // Without the WeakSet dedupe, a user repeatedly dragging on the
    // same mis-configured element would spam console.warn (and
    // re-run getComputedStyle, a style-recalc trigger) once per
    // pointerdown.
    const warn = console.warn as jest.Mock;
    document.body.innerHTML = `
      <div id="static-parent">
        <img id="target" lvt-fx:area-select="selectImageArea">
      </div>
    `;
    const target = document.getElementById("target") as HTMLImageElement;
    target.getBoundingClientRect = jest.fn(() => ({
      x: 0, y: 0, left: 0, top: 0, right: 200, bottom: 200,
      width: 200, height: 200, toJSON: () => ({}),
    } as DOMRect));
    (target as any).setPointerCapture = jest.fn();
    (target as any).releasePointerCapture = jest.fn();
    handleAreaSelectDirectives(document.body, jest.fn());

    // Three drags on the same mis-configured parent — only the FIRST
    // should warn.
    for (let i = 0; i < 3; i++) {
      dispatchPointer(target, "pointerdown", 10, 10);
      dispatchPointer(target, "pointerup", 60, 60);
    }

    const warnings = warn.mock.calls.filter(
      (args) => typeof args[0] === "string" && args[0].includes("parentElement has no positioning context")
    );
    expect(warnings.length).toBe(1);
  });

  it("pointercancel cancels the drag without dispatching", () => {
    // pointercancel fires on system gestures (OS-level swipe, app
    // switch). Like lostpointercapture / pointerleave, it must remove
    // the overlay and NOT dispatch — same contract from the user's
    // perspective.
    const [, parent] = mountTarget(
      "img",
      { "lvt-fx:area-select": "selectImageArea" },
      { left: 0, top: 0, width: 200, height: 200 }
    );
    const target = parent.querySelector("img")! as HTMLElement;
    const send = jest.fn();
    handleAreaSelectDirectives(document.body, send);

    dispatchPointer(target, "pointerdown", 10, 10);
    dispatchPointer(target, "pointermove", 80, 80);
    dispatchPointer(target, "pointercancel", 80, 80);

    expect(send).not.toHaveBeenCalled();
    expect(parent.querySelector(".lvt-area-select-overlay")).toBeNull();
  });

  it("rejects zero-area rectangles even if the threshold check would pass", () => {
    // The MIN_AREA_FRACTION check uses && so a wide-but-thin drag is
    // a legit selection — but a literal 60% × 0% drag has no area,
    // can't render sensibly, and would divide by zero downstream.
    // Drop it independently of the threshold.
    const [target] = mountTarget(
      "img",
      { "lvt-fx:area-select": "selectImageArea" },
      { left: 0, top: 0, width: 200, height: 200 }
    );
    const send = jest.fn();
    handleAreaSelectDirectives(document.body, send);

    // Drag from (40,100) to (160,100) — w=60% of 200, h=0%.
    dispatchPointer(target, "pointerdown", 40, 100);
    dispatchPointer(target, "pointermove", 160, 100);
    dispatchPointer(target, "pointerup", 160, 100);

    expect(send).not.toHaveBeenCalled();
  });

  it("teardownAreaSelectForRoot cancels armed elements under root", () => {
    // For the disconnect / destroy lifecycle: if a client tears down
    // without a subsequent handleAreaSelectDirectives call, the
    // module-level singleton would otherwise leak listeners.
    document.body.innerHTML = `
      <div id="root">
        <div id="parent" style="position:relative;">
          <img id="target" lvt-fx:area-select="selectImageArea">
        </div>
      </div>
      <div id="outside-parent" style="position:relative;">
        <img id="outside-target" lvt-fx:area-select="otherAction">
      </div>
    `;
    const root = document.getElementById("root")!;
    const target = document.getElementById("target")! as HTMLImageElement;
    const outside = document.getElementById("outside-target")! as HTMLImageElement;
    for (const el of [target, outside]) {
      el.getBoundingClientRect = jest.fn(() => ({
        x: 0, y: 0, left: 0, top: 0, right: 200, bottom: 200,
        width: 200, height: 200, toJSON: () => ({}),
      } as DOMRect));
      el.parentElement!.getBoundingClientRect = jest.fn(() => ({
        x: 0, y: 0, left: 0, top: 0, right: 200, bottom: 200,
        width: 200, height: 200, toJSON: () => ({}),
      } as DOMRect));
      (el as any).setPointerCapture = jest.fn();
      (el as any).releasePointerCapture = jest.fn();
    }
    const send = jest.fn();
    handleAreaSelectDirectives(document.body, send);

    teardownAreaSelectForRoot(root);

    // The target inside root must NOT dispatch after teardown.
    dispatchPointer(target, "pointerdown", 10, 10);
    dispatchPointer(target, "pointermove", 100, 100);
    dispatchPointer(target, "pointerup", 100, 100);
    expect(send).not.toHaveBeenCalled();

    // The target outside root must still work.
    dispatchPointer(outside, "pointerdown", 10, 10);
    dispatchPointer(outside, "pointermove", 100, 100);
    dispatchPointer(outside, "pointerup", 100, 100);
    expect(send).toHaveBeenCalledTimes(1);
    expect(send.mock.calls[0][0].action).toBe("otherAction");

    teardownAreaSelectForRoot(document.body); // clean up for next test
  });

  it("does not preventDefault on pointerdown — clicks still bubble", () => {
    // The contract promises a small-rect drag (treated as a click)
    // still reaches the host's click handlers. Calling
    // preventDefault on pointerdown would suppress the compatibility
    // mouse events that fire click — so the directive must NOT do
    // that.
    const [target] = mountTarget(
      "img",
      { "lvt-fx:area-select": "selectImageArea" },
      { left: 0, top: 0, width: 200, height: 200 }
    );
    handleAreaSelectDirectives(document.body, jest.fn());

    const down = new MouseEvent("pointerdown", {
      clientX: 50, clientY: 50, button: 0, bubbles: true, cancelable: true,
    });
    Object.defineProperty(down, "pointerId", { value: 1 });
    Object.defineProperty(down, "isPrimary", { value: true });
    target.dispatchEvent(down);

    expect(down.defaultPrevented).toBe(false);
  });

  it("stale pointerleave listener does not survive into the next gesture", () => {
    // The bug Claude flagged: when setPointerCapture fails on drag N,
    // the directive registers a pointerleave fallback. If drag N
    // gets stuck (pointer never leaves so the fallback never fires
    // and no pointerup arrives), and the user starts drag N+1, the
    // re-entrancy guard finalizes drag N but the STALE pointerleave
    // listener from N would still be attached. If capture SUCCEEDS
    // on drag N+1 (no new pointerleave registered), the stale one
    // from N would still fire if the pointer ever leaves — and
    // incorrectly cancel drag N+1. finalize() must remove the
    // pointerleave listener so it can't outlive its own gesture.
    const [target] = mountTarget(
      "img",
      { "lvt-fx:area-select": "selectImageArea" },
      { left: 0, top: 0, width: 200, height: 200 }
    );
    // Capture FAILS only on the first call (drag N), succeeds after.
    let captureCalls = 0;
    (target as any).setPointerCapture = jest.fn(() => {
      captureCalls++;
      if (captureCalls === 1) {
        throw new DOMException("no capture", "InvalidStateError");
      }
    });
    const send = jest.fn();
    handleAreaSelectDirectives(document.body, send);

    // Drag N: pointerdown attaches the pointerleave fallback, but
    // user starts drag and never releases (simulates a "stuck"
    // drag). Do NOT pointerup.
    dispatchPointer(target, "pointerdown", 10, 10);
    dispatchPointer(target, "pointermove", 80, 80);

    // Drag N+1 starts. Re-entrancy guard finalizes drag N. WITHOUT
    // the fix, drag N's pointerleave listener stays attached.
    // Capture succeeds this time so no NEW pointerleave is attached.
    dispatchPointer(target, "pointerdown", 20, 20);
    dispatchPointer(target, "pointermove", 100, 100);

    // Fire pointerleave. With the fix, no pointerleave handler is
    // attached → no-op, drag N+1 continues. WITHOUT the fix, the
    // stale listener from N would call finalize and cancel.
    target.dispatchEvent(new MouseEvent("pointerleave", { bubbles: false }));
    dispatchPointer(target, "pointerup", 100, 100);

    // Drag N+1 should have dispatched normally — the stale listener
    // must NOT have cancelled it.
    expect(send).toHaveBeenCalledTimes(1);
  });

  it("idempotent re-arm picks up the latest send callback (no stale closure)", () => {
    const [target] = mountTarget(
      "img",
      { "lvt-fx:area-select": "selectImageArea" },
      { left: 0, top: 0, width: 200, height: 200 }
    );
    const firstSend = jest.fn();
    const secondSend = jest.fn();

    handleAreaSelectDirectives(document.body, firstSend);
    // A subsequent render passes a different send (e.g. after a WS
    // reconnect rebuilt the transport). The idempotent path keeps
    // the listeners but MUST swap the captured send so the next
    // drag dispatches through the latest callback.
    handleAreaSelectDirectives(document.body, secondSend);

    dispatchPointer(target, "pointerdown", 10, 10);
    dispatchPointer(target, "pointermove", 100, 100);
    dispatchPointer(target, "pointerup", 100, 100);

    expect(firstSend).not.toHaveBeenCalled();
    expect(secondSend).toHaveBeenCalledTimes(1);
  });

  it("lostpointercapture cancels the drag without dispatching", () => {
    const [, parent] = mountTarget(
      "img",
      { "lvt-fx:area-select": "selectImageArea" },
      { left: 0, top: 0, width: 200, height: 200 }
    );
    const target = parent.querySelector("img")! as HTMLElement;
    const send = jest.fn();
    handleAreaSelectDirectives(document.body, send);

    dispatchPointer(target, "pointerdown", 10, 10);
    dispatchPointer(target, "pointermove", 80, 80);
    // Platform yanks capture (OS gesture, another setPointerCapture
    // call elsewhere). lostpointercapture should cancel like
    // pointercancel — overlay removed, no action dispatched.
    dispatchPointer(target, "lostpointercapture", 80, 80);

    expect(send).not.toHaveBeenCalled();
    expect(parent.querySelector(".lvt-area-select-overlay")).toBeNull();
  });

  it("cleans up armed elements whose attribute was removed by a server diff", () => {
    const [target] = mountTarget(
      "img",
      { "lvt-fx:area-select": "selectImageArea" },
      { left: 0, top: 0, width: 200, height: 200 }
    );
    const send = jest.fn();
    handleAreaSelectDirectives(document.body, send);

    // Server diff removes the attribute. The element stays in the DOM
    // (host alive, but no longer wants area-select). The next
    // handleAreaSelectDirectives pass MUST cancel the listeners — a
    // subsequent drag must not dispatch the old action.
    target.removeAttribute("lvt-fx:area-select");
    handleAreaSelectDirectives(document.body, send);

    dispatchPointer(target, "pointerdown", 10, 10);
    dispatchPointer(target, "pointermove", 100, 100);
    dispatchPointer(target, "pointerup", 100, 100);

    expect(send).not.toHaveBeenCalled();
  });

  it("recovers from a stuck drag on the next pointerdown (re-entrancy guard)", () => {
    const [target] = mountTarget(
      "img",
      { "lvt-fx:area-select": "selectImageArea" },
      { left: 0, top: 0, width: 200, height: 200 }
    );
    const send = jest.fn();
    handleAreaSelectDirectives(document.body, send);

    // First drag: pointerdown but no pointerup (simulates a captured
    // pointer that never released — what happens when capture silently
    // fails and pointer leaves the element).
    dispatchPointer(target, "pointerdown", 10, 10);
    dispatchPointer(target, "pointermove", 60, 60);
    // No pointerup. The drag is "stuck".

    // Second drag starts. Without the re-entrancy guard, the first
    // overlay would be orphaned. With the guard, the directive cancels
    // the stuck drag before starting the new one, and the new drag
    // completes normally.
    dispatchPointer(target, "pointerdown", 100, 100);
    dispatchPointer(target, "pointermove", 150, 150);
    dispatchPointer(target, "pointerup", 150, 150);

    expect(send).toHaveBeenCalledTimes(1);
    // The dispatched coords must come from the SECOND drag, not the
    // first. (Start=100, End=150 → x=0.5, w=0.25 on the 200-wide rect.)
    const data = send.mock.calls[0][0].data as Record<string, number>;
    expect(data.x).toBeCloseTo(0.50, 5);
    expect(data.w).toBeCloseTo(0.25, 5);
    // Exactly one overlay at most over the whole sequence (the second
    // drag's) — never two.
    expect(document.querySelectorAll(".lvt-area-select-overlay").length).toBe(0);
  });
});

describe("handleURLHashDirective", () => {
  // The directive is a module-level singleton (a Map of armed elements
  // plus a single window-level hashchange listener), so every test
  // must tear down to avoid bleed between cases.
  afterEach(() => {
    teardownURLHashForRoot(document.body);
    document.body.innerHTML = "";
    // Body persists across tests (innerHTML only resets descendants);
    // wipe attributes the previous test set on body itself.
    document.body.removeAttribute("lvt-fx:url-hash");
    document.body.removeAttribute("data-lvt-url-hash");
    // Reset URL hash without touching history (the directive uses
    // pushState/replaceState; jsdom keeps them isolated per test).
    window.history.replaceState(null, "", window.location.pathname);
    // The unencoded-hash warning dedupe Set is module-level and
    // outlives a single test; reset so a hash value reused across
    // tests still warns.
    __resetURLHashUnencodedWarnedForTesting();
    jest.restoreAllMocks();
  });

  function mountBody(dataHash: string, action = "setURLHash"): HTMLElement {
    const body = document.body;
    body.setAttribute("lvt-fx:url-hash", action);
    body.setAttribute("data-lvt-url-hash", dataHash);
    return body;
  }

  it("on first arm with empty location.hash and non-empty data-attr, mirrors data-attr into location.hash", () => {
    mountBody("README.md:L4");
    const send = jest.fn();
    handleURLHashDirective(document.body, send);
    expect(window.location.hash).toBe("#README.md:L4");
    // No dispatch because the URL didn't drive the change — the server
    // already knew the state (the data-attr came FROM the server).
    expect(send).not.toHaveBeenCalled();
  });

  it("on first arm with empty URL, uses replaceState (not pushState) — Back must not loop", () => {
    // Bug pinned: when initial URL is empty AND server has a hash,
    // the mirror step uses pushState by the path-comparison rule
    // ("" → "README.md" is a path change). That leaves a history
    // entry where Back lands the user on `url-without-hash`, which
    // re-arms the directive, which pushes the same hash again. Loop.
    // Fix: empty currentLocation → always replaceState.
    const lengthBefore = window.history.length;
    mountBody("README.md:L4");
    const send = jest.fn();
    handleURLHashDirective(document.body, send);
    expect(window.location.hash).toBe("#README.md:L4");
    expect(window.history.length).toBe(lengthBefore);
  });

  it("on first arm with non-empty location.hash differing from data-attr, dispatches the action with the URL hash", () => {
    window.history.replaceState(null, "", "#README.md:L4");
    mountBody(""); // server hasn't seen the hash yet
    const send = jest.fn();
    handleURLHashDirective(document.body, send);
    expect(send).toHaveBeenCalledTimes(1);
    expect(send.mock.calls[0][0]).toEqual({
      action: "setURLHash",
      data: { hash: "README.md:L4" },
    });
    // The URL is not rewritten — the server's next render will produce
    // the canonical data-attr, and we'll converge then.
    expect(window.location.hash).toBe("#README.md:L4");
  });

  it("on first arm with empty location.hash and empty data-attr, no-op (no dispatch, no URL write)", () => {
    mountBody("");
    const send = jest.fn();
    handleURLHashDirective(document.body, send);
    expect(send).not.toHaveBeenCalled();
    expect(window.location.hash).toBe("");
  });

  it("mirrors data-attr change to location.hash via replaceState when path component is unchanged", () => {
    mountBody("README.md:L4");
    const send = jest.fn();
    handleURLHashDirective(document.body, send);
    expect(window.location.hash).toBe("#README.md:L4");
    const lengthBefore = window.history.length;

    // Server re-render: same file, different line.
    document.body.setAttribute("data-lvt-url-hash", "README.md:L8");
    handleURLHashDirective(document.body, send);

    expect(window.location.hash).toBe("#README.md:L8");
    // replaceState keeps history depth flat: jsdom's history.length
    // increments only on pushState, not replaceState.
    expect(window.history.length).toBe(lengthBefore);
  });

  it("mirrors data-attr change to location.hash via pushState when path component changes", () => {
    mountBody("README.md:L4");
    const send = jest.fn();
    handleURLHashDirective(document.body, send);
    const lengthBefore = window.history.length;

    // Server re-render: different file.
    document.body.setAttribute("data-lvt-url-hash", "OTHER.md:L1");
    handleURLHashDirective(document.body, send);

    expect(window.location.hash).toBe("#OTHER.md:L1");
    expect(window.history.length).toBe(lengthBefore + 1);
  });

  it("on hashchange (user clicks a permalink), dispatches the action with the new hash", () => {
    mountBody("README.md:L4");
    const send = jest.fn();
    handleURLHashDirective(document.body, send);
    send.mockClear();

    // Simulate a user-driven hash change: set location.hash AND
    // synchronously fire the hashchange event. (jsdom queues
    // hashchange asynchronously when you assign location.hash, so we
    // dispatch manually to keep the test deterministic — same pattern
    // as area-select's synthetic pointer events.)
    window.history.replaceState(null, "", "#OTHER.md:L2");
    window.dispatchEvent(new Event("hashchange"));

    expect(send).toHaveBeenCalledTimes(1);
    expect(send.mock.calls[0][0]).toEqual({
      action: "setURLHash",
      data: { hash: "OTHER.md:L2" },
    });
  });

  it("idempotent re-arm with the same action does NOT add history entries", () => {
    mountBody("README.md:L4");
    const send = jest.fn();
    handleURLHashDirective(document.body, send);
    const lengthAfterArm = window.history.length;

    // Re-call with no data-attr change.
    handleURLHashDirective(document.body, send);
    handleURLHashDirective(document.body, send);
    handleURLHashDirective(document.body, send);

    expect(window.history.length).toBe(lengthAfterArm);
    expect(window.location.hash).toBe("#README.md:L4");
  });

  it("updateSend swaps the captured transport so a reconnect rebuilds dispatching", () => {
    mountBody("README.md:L4");
    const firstSend = jest.fn();
    handleURLHashDirective(document.body, firstSend);
    firstSend.mockClear();

    // Re-arm with a NEW send (simulating a reconnect that rebuilt the
    // transport).
    const secondSend = jest.fn();
    handleURLHashDirective(document.body, secondSend);

    // hashchange now should route through the second send only.
    window.history.replaceState(null, "", "#OTHER.md:L1");
    window.dispatchEvent(new Event("hashchange"));
    expect(firstSend).not.toHaveBeenCalled();
    expect(secondSend).toHaveBeenCalledTimes(1);
    expect(secondSend.mock.calls[0][0].data).toEqual({ hash: "OTHER.md:L1" });
  });

  it("teardown removes the armed element AND its hashchange listener", () => {
    mountBody("README.md:L4");
    const send = jest.fn();
    handleURLHashDirective(document.body, send);
    expect(window.location.hash).toBe("#README.md:L4");

    teardownURLHashForRoot(document.body);

    // After teardown, a hashchange does NOT dispatch — the window
    // listener was removed when the armed map emptied.
    window.history.replaceState(null, "", "#OTHER.md:L1");
    window.dispatchEvent(new Event("hashchange"));
    expect(send).not.toHaveBeenCalled();
  });

  it("teardown via a descendant root cleans up a body-armed entry", () => {
    // Pin the body-ancestor branch of teardownURLHashForRoot: in
    // production, livetemplate calls teardown with the wrapper div
    // (inside body) as the root, but the directive lives on body.
    // The teardown must clean up the body entry too — otherwise
    // disconnect/reconnect cycles leak listeners.
    mountBody("README.md:L4");
    const wrapper = document.createElement("div");
    document.body.appendChild(wrapper);
    const send = jest.fn();
    handleURLHashDirective(document.body, send);

    teardownURLHashForRoot(wrapper);

    window.history.replaceState(null, "", "#OTHER.md:L1");
    window.dispatchEvent(new Event("hashchange"));
    expect(send).not.toHaveBeenCalled();
  });

  it("sweep cleans up entries whose attribute was removed by a server diff", () => {
    mountBody("README.md:L4");
    const send = jest.fn();
    handleURLHashDirective(document.body, send);

    // Server diff removed the directive.
    document.body.removeAttribute("lvt-fx:url-hash");
    document.body.removeAttribute("data-lvt-url-hash");
    handleURLHashDirective(document.body, send);

    // The window listener should be gone now too — no dispatch.
    window.history.replaceState(null, "", "#OTHER.md:L1");
    window.dispatchEvent(new Event("hashchange"));
    expect(send).not.toHaveBeenCalled();
  });

  it("warns and skips when lvt-fx:url-hash is present but empty", () => {
    const warn = jest.spyOn(console, "warn").mockImplementation(() => {});
    document.body.setAttribute("lvt-fx:url-hash", "");
    const send = jest.fn();
    handleURLHashDirective(document.body, send);
    expect(warn).toHaveBeenCalledWith(
      expect.stringContaining("lvt-fx:url-hash requires an action name")
    );
    expect(send).not.toHaveBeenCalled();
  });

  it("ignores plain element-id hashes on initial load (no dispatch, no URL clobber)", () => {
    // Anchors like `#hero` (no `:`, no `/`, no `.`) belong to native
    // anchor / dialog / popover machinery — the directive must NOT
    // dispatch for them or it would race against setupHashLink.
    window.history.replaceState(null, "", "#hero");
    mountBody("");
    const send = jest.fn();
    handleURLHashDirective(document.body, send);
    expect(send).not.toHaveBeenCalled();
    expect(window.location.hash).toBe("#hero");
  });

  it("on initial load with non-deep-link hash AND non-empty server data-attr, leaves the URL alone", () => {
    // The browser navigated to `#hero` (a popover id, say). The
    // server happens to have selected a default file, so the data-
    // attr is non-empty. Before the fix, the else-branch mirrored
    // the server's hash into the URL and silently closed the
    // popover. After the fix, the URL is left alone — popover
    // wins.
    window.history.replaceState(null, "", "#hero");
    mountBody("README.md");
    const send = jest.fn();
    handleURLHashDirective(document.body, send);
    expect(send).not.toHaveBeenCalled();
    expect(window.location.hash).toBe("#hero");
  });

  it("server changing selection while a non-deep-link URL hash is open does NOT clobber", () => {
    // Round-8 bot edge case: case (b) leaves the URL on #hero and
    // seeds currentDataHash = dataHash. If the server then pushes a
    // DIFFERENT selection (rare in prereview — server state changes
    // only on user action — but possible via cross-tab sync or a
    // server-driven update), the mirror's path-comparison would
    // have written the new file's hash and clobbered #hero. Fixed
    // by generalising the non-deep-link-URL guard to cover any
    // dataHash, not just empty.
    window.history.replaceState(null, "", "#hero");
    mountBody("README.md");
    const send = jest.fn();
    handleURLHashDirective(document.body, send);
    expect(window.location.hash).toBe("#hero");

    // Server pushes a different selection — URL must NOT change.
    document.body.setAttribute("data-lvt-url-hash", "OTHER.md");
    handleURLHashDirective(document.body, send);
    expect(window.location.hash).toBe("#hero");
  });

  it("server clearing the data-attr DOES clear a deep-link URL hash (deliberate, symmetric)", () => {
    // Symmetric to "non-deep-link is never clobbered": if the URL
    // is a deep-link we own AND the server transitions to
    // no-selection, the URL should clear. Pin this as deliberate
    // so a future refactor doesn't accidentally make all
    // empty-dataHash mirrors no-op.
    mountBody("README.md:L4");
    const send = jest.fn();
    handleURLHashDirective(document.body, send);
    expect(window.location.hash).toBe("#README.md:L4");

    document.body.setAttribute("data-lvt-url-hash", "");
    handleURLHashDirective(document.body, send);
    expect(window.location.hash).toBe("");
  });

  it("deselection (deep-link → empty) uses pushState — Back returns to the selection (deliberate)", () => {
    // Pin the path-comparison branch's behavior for the deselect
    // case: server transitions `data-lvt-url-hash` from a selected
    // file to "", path comparison says oldPath !== newPath (one is
    // empty), the mirror uses pushState. That leaves a history
    // entry so Back returns the user to their prior selection.
    // Intentional — matches the file-switch UX.
    mountBody("README.md:L4");
    const send = jest.fn();
    handleURLHashDirective(document.body, send);
    expect(window.location.hash).toBe("#README.md:L4");
    const lengthBeforeDeselect = window.history.length;

    document.body.setAttribute("data-lvt-url-hash", "");
    handleURLHashDirective(document.body, send);

    expect(window.location.hash).toBe("");
    expect(window.history.length).toBe(lengthBeforeDeselect + 1);
  });

  it("server clearing the data-attr does NOT wipe a non-deep-link URL hash", () => {
    // Server first has README.md selected → URL becomes
    // `#README.md`. Then the user opens a popover whose id is
    // `#hero` (URL is now `#hero`). Then the server transitions to
    // no-selection (e.g. ClearSelection) and renders data-attr="".
    // The directive must not clobber the popover hash.
    mountBody("README.md");
    const send = jest.fn();
    handleURLHashDirective(document.body, send);
    expect(window.location.hash).toBe("#README.md");

    // User navigates to a popover-shaped hash (simulated).
    window.history.replaceState(null, "", "#hero");

    // Server clears state → empty data-attr.
    document.body.setAttribute("data-lvt-url-hash", "");
    handleURLHashDirective(document.body, send);

    expect(window.location.hash).toBe("#hero");
  });

  it("case-(b) + user clears URL: URL stays empty while server keeps its selection (deliberate divergence)", () => {
    // Load with a popover-shaped hash, server has a file selected.
    // Case (b) seeds entry.currentDataHash with the server's value.
    window.history.replaceState(null, "", "#hero");
    mountBody("README.md");
    const send = jest.fn();
    handleURLHashDirective(document.body, send);
    expect(window.location.hash).toBe("#hero");

    // User clears the URL bar.
    window.history.replaceState(null, "", window.location.pathname);
    window.dispatchEvent(new Event("hashchange"));
    expect(send).not.toHaveBeenCalled();

    // Server re-renders with the SAME selection (data-attr unchanged).
    // The early-exit guard (currentDataHash === dataHash) means we
    // never mirror — URL stays empty, server stays on README.md.
    // This divergence is intentional: case (b) said "the URL isn't
    // ours, leave it alone", and a user clearing the URL doesn't
    // change that — they're still navigating outside our turf.
    handleURLHashDirective(document.body, send);
    expect(window.location.hash).toBe("");
    expect(send).not.toHaveBeenCalled();

    // Convergence only happens when the user takes an in-app action
    // that changes server state (e.g. SelectFile to OTHER.md): the
    // data-attr now differs from currentDataHash and the mirror
    // step writes the new hash.
    document.body.setAttribute("data-lvt-url-hash", "OTHER.md");
    handleURLHashDirective(document.body, send);
    expect(window.location.hash).toBe("#OTHER.md");
  });

  it("user clearing the URL hash (hashchange to empty) does NOT dispatch — server stays source of truth", () => {
    // The directive treats the server as source of truth for
    // "what's selected". An empty location.hash is "user navigated
    // away from the hash" but not "deselect". Deselect happens via
    // in-app affordances that flow through the server, which then
    // emits an empty data-attr.
    mountBody("README.md:L4");
    const send = jest.fn();
    handleURLHashDirective(document.body, send);
    send.mockClear();

    // User clears the URL bar.
    window.history.replaceState(null, "", window.location.pathname);
    window.dispatchEvent(new Event("hashchange"));

    expect(send).not.toHaveBeenCalled();
  });

  it("ignores plain element-id hashes on hashchange", () => {
    mountBody("README.md:L4");
    const send = jest.fn();
    handleURLHashDirective(document.body, send);
    send.mockClear();

    // User clicks an HTML anchor link (e.g. inside the TOC overlay).
    window.history.replaceState(null, "", "#some-section");
    window.dispatchEvent(new Event("hashchange"));

    expect(send).not.toHaveBeenCalled();
  });

  it("preserves history.state across the mirror write (doesn't clobber co-tenant SPA state)", () => {
    // Co-tenant code stores something in history.state — e.g. scroll
    // position or modal flag. Our directive's push/replaceState must
    // pass that state through, not overwrite with null.
    window.history.replaceState({ scroll: 42, modal: "open" }, "", window.location.pathname);
    mountBody("README.md:L4");
    const send = jest.fn();
    handleURLHashDirective(document.body, send);
    expect(window.location.hash).toBe("#README.md:L4");
    expect(window.history.state).toEqual({ scroll: 42, modal: "open" });

    // Same on a path-change pushState path.
    document.body.setAttribute("data-lvt-url-hash", "OTHER.md");
    handleURLHashDirective(document.body, send);
    expect(window.location.hash).toBe("#OTHER.md");
    expect(window.history.state).toEqual({ scroll: 42, modal: "open" });
  });

  it("warns when data-lvt-url-hash contains characters that should be percent-encoded", () => {
    const warn = jest.spyOn(console, "warn").mockImplementation(() => {});
    // Note: jsdom's location.hash setter percent-encodes spaces, so
    // we mount the directive with a value containing a literal space
    // and expect a warn before the directive writes the URL.
    mountBody("path with space.md");
    handleURLHashDirective(document.body, jest.fn());
    expect(warn).toHaveBeenCalledWith(
      expect.stringContaining("should be percent-encoded")
    );
  });

  it.each([
    ["bracket [", "path[v1].md"],
    ["bracket ]", "list]item.md"],
    ["raw percent", "50%off.md"],
    ["raw quote", `name".md`],
    ["raw less-than", "x<y.md"],
  ])("warns on %s", (_label, hash) => {
    const warn = jest.spyOn(console, "warn").mockImplementation(() => {});
    mountBody(hash);
    handleURLHashDirective(document.body, jest.fn());
    expect(warn).toHaveBeenCalledWith(
      expect.stringContaining("should be percent-encoded")
    );
  });

  it("valid percent-encoded sequence does NOT warn", () => {
    // `%20` is a properly percent-encoded space — no warn.
    const warn = jest.spyOn(console, "warn").mockImplementation(() => {});
    mountBody("path%20with%20space.md");
    handleURLHashDirective(document.body, jest.fn());
    expect(warn).not.toHaveBeenCalledWith(
      expect.stringContaining("should be percent-encoded")
    );
  });

  it("data-attr update unchanged from last mirror is a no-op (no history pollution)", () => {
    mountBody("README.md:L4");
    const send = jest.fn();
    handleURLHashDirective(document.body, send);

    // User clicks a permalink anchor → location.hash changes to the
    // same value the data-attr already had. The hashchange dispatch
    // updates currentDataHash to the same value; subsequent renders
    // with the same data-attr should still no-op (no extra history
    // entries when the server echoes back the same hash).
    window.history.replaceState(null, "", "#OTHER.md:L9");
    window.dispatchEvent(new Event("hashchange"));
    const lengthBefore = window.history.length;

    send.mockClear();
    document.body.setAttribute("data-lvt-url-hash", "OTHER.md:L9");
    handleURLHashDirective(document.body, send);

    expect(window.history.length).toBe(lengthBefore);
    expect(window.location.hash).toBe("#OTHER.md:L9");
  });
});

describe("lineRangeFromBox", () => {
  // jsdom does no layout, so fake each candidate's geometry + attributes.
  function cand(
    rect: { left: number; top: number; right: number; bottom: number },
    attrs: Record<string, string>
  ): Element {
    return {
      getBoundingClientRect: () => rect as DOMRect,
      getAttribute: (k: string) => (k in attrs ? attrs[k] : null),
    } as unknown as Element;
  }
  it("unions the line ranges of every block the box overlaps", () => {
    const cands = [
      cand({ left: 0, top: 0, right: 100, bottom: 20 }, { "data-from": "2", "data-to": "4" }),
      cand({ left: 0, top: 20, right: 100, bottom: 40 }, { "data-from": "6", "data-to": "9" }),
      cand({ left: 0, top: 40, right: 100, bottom: 60 }, { "data-from": "11", "data-to": "12" }),
    ];
    // Box covers the first two rows (y 5..30), not the third (y 40..60).
    const range = lineRangeFromBox(
      cands,
      { left: 10, top: 5, right: 90, bottom: 30 },
      0,
      0,
      readHTMLRange
    );
    expect(range).toEqual({ from: 2, to: 9 });
  });

  it("returns null when the box overlaps no candidate", () => {
    const cands = [
      cand({ left: 0, top: 0, right: 100, bottom: 20 }, { "data-from": "2", "data-to": "4" }),
    ];
    const range = lineRangeFromBox(
      cands,
      { left: 10, top: 200, right: 90, bottom: 260 },
      0,
      0,
      readHTMLRange
    );
    expect(range).toBeNull();
  });

  it("applies the offset so iframe-content rects map into box space", () => {
    // Candidate is at content y 0..20; the iframe sits at viewport y 100.
    const cands = [
      cand({ left: 0, top: 0, right: 100, bottom: 20 }, { "data-from": "5", "data-to": "5" }),
    ];
    // Box in viewport coords overlaps only AFTER the +100 offset is applied.
    const range = lineRangeFromBox(
      cands,
      { left: 0, top: 105, right: 100, bottom: 115 },
      0,
      100,
      readHTMLRange
    );
    expect(range).toEqual({ from: 5, to: 5 });
  });

  // A box crossing a diff's add/del boundary must NOT union old-side and
  // new-side line numbers (different coordinate systems). The topmost row
  // fixes the side; the other side's rows are excluded.
  it("restricts to the topmost row's side when a box crosses the diff boundary (old on top)", () => {
    const cands = [
      cand({ left: 0, top: 0, right: 100, bottom: 20 }, { "data-line": "7", "data-side": "old" }),
      cand({ left: 0, top: 20, right: 100, bottom: 40 }, { "data-line": "8" }), // new side
    ];
    const range = lineRangeFromBox(
      cands,
      { left: 0, top: 5, right: 100, bottom: 35 },
      0,
      0,
      readCodeRange
    );
    // Only the old-side row survives; the new-side row 8 is excluded.
    expect(range).toEqual({ from: 7, to: 7, side: "old" });
  });

  it("restricts to the new side when the topmost row is new", () => {
    const cands = [
      cand({ left: 0, top: 0, right: 100, bottom: 20 }, { "data-line": "11" }), // new side, on top
      cand({ left: 0, top: 20, right: 100, bottom: 40 }, { "data-line": "12" }), // new side
      cand({ left: 0, top: 40, right: 100, bottom: 60 }, { "data-line": "3", "data-side": "old" }),
    ];
    const range = lineRangeFromBox(
      cands,
      { left: 0, top: 5, right: 100, bottom: 55 },
      0,
      0,
      readCodeRange
    );
    // Two new-side rows union; the trailing old-side row 3 is excluded.
    expect(range).toEqual({ from: 11, to: 12 });
  });

  it("skips candidates whose readRange returns null", () => {
    const cands = [
      cand({ left: 0, top: 0, right: 100, bottom: 20 }, { "data-from": "0" }), // invalid
      cand({ left: 0, top: 20, right: 100, bottom: 40 }, { "data-from": "9", "data-to": "9" }),
    ];
    const range = lineRangeFromBox(
      cands,
      { left: 0, top: 5, right: 100, bottom: 35 },
      0,
      0,
      readHTMLRange
    );
    expect(range).toEqual({ from: 9, to: 9 });
  });
});

describe("handleRegionSelectDirectives", () => {
  afterEach(() => {
    document.body.innerHTML = "";
  });

  // Build a positioned wrapper containing an overlay host (the parent
  // positioning context the drag spine warns about otherwise).
  function makeOverlay(surface: string): HTMLElement {
    const wrap = document.createElement("div");
    wrap.style.position = "relative";
    const overlay = document.createElement("div");
    overlay.setAttribute("lvt-fx:region-select", "selectBlock");
    overlay.setAttribute("data-surface", surface);
    wrap.appendChild(overlay);
    document.body.appendChild(wrap);
    return overlay;
  }

  it("arms an overlay and is idempotent across re-arms", () => {
    const send = jest.fn();
    const overlay = makeOverlay("code");
    handleRegionSelectDirectives(document.body, send);
    // Re-arm with the same action must not throw or rebind destructively.
    expect(() => handleRegionSelectDirectives(document.body, send)).not.toThrow();
    expect(overlay.isConnected).toBe(true);
  });

  it("sweeps the listener when the attribute is removed", () => {
    const send = jest.fn();
    const overlay = makeOverlay("html");
    handleRegionSelectDirectives(document.body, send);
    overlay.removeAttribute("lvt-fx:region-select");
    // Second pass should clean up without error.
    expect(() => handleRegionSelectDirectives(document.body, send)).not.toThrow();
  });

  it("teardownRegionSelectForRoot cleans armed overlays under the root", () => {
    const send = jest.fn();
    makeOverlay("code");
    handleRegionSelectDirectives(document.body, send);
    expect(() => teardownRegionSelectForRoot(document.body)).not.toThrow();
  });
});

describe("handleProxyBridgeDirectives (--external live-site bridge)", () => {
  const ORIGIN = "http://127.0.0.1:7778";

  // Build the preview stage: <stage lvt-fx:proxy-bridge="setProxyURL">
  //   <iframe src=ORIGIN> <div data-pin-layer>
  function makeStage(): {
    stage: HTMLElement;
    iframe: HTMLIFrameElement;
    pin: HTMLElement;
  } {
    const stage = document.createElement("div");
    stage.setAttribute("lvt-fx:proxy-bridge", "setProxyURL");
    const iframe = document.createElement("iframe");
    iframe.src = ORIGIN + "/";
    const pin = document.createElement("div");
    pin.setAttribute("data-pin-layer", "");
    stage.appendChild(iframe);
    stage.appendChild(pin);
    document.body.appendChild(stage);
    return { stage, iframe, pin };
  }

  function beacon(data: Record<string, unknown>, origin = ORIGIN): void {
    window.dispatchEvent(new MessageEvent("message", { data, origin }));
  }

  beforeEach(() => {
    document.body.innerHTML = "";
    jest.spyOn(console, "warn").mockImplementation(() => {});
  });
  afterEach(() => {
    teardownProxyBridgeForRoot(document.body);
    document.body.innerHTML = "";
    jest.restoreAllMocks();
  });

  it("a nav beacon dispatches setProxyURL with the page url", () => {
    const send = jest.fn();
    makeStage();
    handleProxyBridgeDirectives(document.body, send);

    beacon({ __prereview: true, type: "nav", url: "/pricing", docW: 800, docH: 4000 });
    expect(send).toHaveBeenCalledWith({ action: "setProxyURL", data: { url: "/pricing" } });
  });

  it("does not re-dispatch setProxyURL for the same url (dedup)", () => {
    const send = jest.fn();
    makeStage();
    handleProxyBridgeDirectives(document.body, send);

    beacon({ __prereview: true, type: "nav", url: "/pricing", docW: 800, docH: 4000 });
    beacon({ __prereview: true, type: "scroll", scrollY: 50, docW: 800, docH: 4000 });
    beacon({ __prereview: true, type: "nav", url: "/pricing", docW: 800, docH: 4000 });
    expect(send).toHaveBeenCalledTimes(1);

    beacon({ __prereview: true, type: "nav", url: "/dashboard", docW: 800, docH: 4000 });
    expect(send).toHaveBeenCalledTimes(2);
  });

  it("re-pins by sizing the pin layer to the document and translating by -scroll", () => {
    const send = jest.fn();
    const { pin } = makeStage();
    handleProxyBridgeDirectives(document.body, send);

    beacon({ __prereview: true, type: "scroll", scrollX: 0, scrollY: 120, docW: 800, docH: 4000 });
    expect(pin.style.width).toBe("800px");
    expect(pin.style.height).toBe("4000px");
    expect(pin.style.transform).toBe("translate(0px, -120px)");
  });

  it("ignores messages from a foreign origin", () => {
    const send = jest.fn();
    const { pin } = makeStage();
    handleProxyBridgeDirectives(document.body, send);

    beacon({ __prereview: true, type: "nav", url: "/evil", docW: 9, docH: 9 }, "http://evil.example");
    expect(send).not.toHaveBeenCalled();
    expect(pin.style.transform).toBe("");
  });

  it("ignores non-prereview messages", () => {
    const send = jest.fn();
    makeStage();
    handleProxyBridgeDirectives(document.body, send);

    beacon({ type: "nav", url: "/x" });
    expect(send).not.toHaveBeenCalled();
  });

  it("teardown removes the window listener (no dispatch after cleanup)", () => {
    const send = jest.fn();
    makeStage();
    handleProxyBridgeDirectives(document.body, send);
    teardownProxyBridgeForRoot(document.body);

    beacon({ __prereview: true, type: "nav", url: "/after", docW: 1, docH: 1 });
    expect(send).not.toHaveBeenCalled();
  });

  it("relays a focus request to the iframe only when the focus token changes", () => {
    const { stage, iframe } = makeStage();
    stage.setAttribute("data-focus-token", "1");
    stage.setAttribute("data-focus-url", "/pricing");
    stage.setAttribute("data-focus-y", "0.42");
    const post = jest
      .spyOn(iframe.contentWindow as Window, "postMessage")
      .mockImplementation(() => {});

    handleProxyBridgeDirectives(document.body, jest.fn());
    expect(post).toHaveBeenCalledWith(
      { __prereviewFocus: true, url: "/pricing", y: 0.42 },
      expect.any(String)
    );

    // Re-render with the SAME token → no repeat (avoids re-scrolling on every
    // unrelated server render).
    post.mockClear();
    handleProxyBridgeDirectives(document.body, jest.fn());
    expect(post).not.toHaveBeenCalled();

    // A new token (the user tapped Locate again) → relays again.
    stage.setAttribute("data-focus-token", "2");
    handleProxyBridgeDirectives(document.body, jest.fn());
    expect(post).toHaveBeenCalledTimes(1);
  });

  it("does not relay a focus request when no focus token is set", () => {
    const { iframe } = makeStage();
    const post = jest
      .spyOn(iframe.contentWindow as Window, "postMessage")
      .mockImplementation(() => {});
    handleProxyBridgeDirectives(document.body, jest.fn());
    expect(post).not.toHaveBeenCalled();
  });

  it("arms exactly one bridge and ignores a second (shared-metrics guard)", () => {
    const warn = jest.spyOn(console, "warn").mockImplementation(() => {});
    makeStage();
    makeStage(); // a second stage — should be ignored
    const send = jest.fn();
    handleProxyBridgeDirectives(document.body, send);

    // Only the first bridge is armed; the second triggers the warning.
    expect(
      warn.mock.calls.some((c) => String(c[0]).includes("exactly one bridge"))
    ).toBe(true);
    // A nav beacon still drives setProxyURL once (the single armed bridge).
    beacon({ __prereview: true, type: "nav", url: "/p", docW: 1, docH: 1 });
    expect(send).toHaveBeenCalledTimes(1);
  });

  it("fails closed when the iframe origin can't be resolved (no src)", () => {
    // An iframe with no src → origin can't be resolved → reject all inbound
    // messages (regardless of sender origin) and never broadcast focus.
    const stage = document.createElement("div");
    stage.setAttribute("lvt-fx:proxy-bridge", "setProxyURL");
    const iframe = document.createElement("iframe"); // deliberately no src
    const pin = document.createElement("div");
    pin.setAttribute("data-pin-layer", "");
    stage.appendChild(iframe);
    stage.appendChild(pin);
    document.body.appendChild(stage);

    const send = jest.fn();
    handleProxyBridgeDirectives(document.body, send);
    window.dispatchEvent(
      new MessageEvent("message", {
        data: { __prereview: true, type: "nav", url: "/x", docW: 1, docH: 1 },
        origin: "http://anything.example",
      })
    );
    expect(send).not.toHaveBeenCalled();

    const post = jest
      .spyOn(iframe.contentWindow as Window, "postMessage")
      .mockImplementation(() => {});
    stage.setAttribute("data-focus-token", "1");
    stage.setAttribute("data-focus-url", "/x");
    stage.setAttribute("data-focus-y", "0.5");
    handleProxyBridgeDirectives(document.body, send);
    expect(post).not.toHaveBeenCalled();
  });

  it("re-applies the pin-layer size/transform on re-render (morphdom-wipe recovery)", () => {
    const { pin } = makeStage();
    handleProxyBridgeDirectives(document.body, jest.fn());
    beacon({ __prereview: true, type: "scroll", scrollX: 0, scrollY: 100, docW: 800, docH: 4000 });
    expect(pin.style.transform).toBe("translate(0px, -100px)");

    // A server render re-renders the pin layer; morphdom diffs it against the
    // template (no inline style) and wipes the imperative styles.
    pin.removeAttribute("style");
    expect(pin.style.transform).toBe("");

    // The directive scan runs after morphdom and must restore them from the
    // cached metrics — otherwise a saved pin vanishes until the next scroll.
    handleProxyBridgeDirectives(document.body, jest.fn());
    expect(pin.style.width).toBe("800px");
    expect(pin.style.height).toBe("4000px");
    expect(pin.style.transform).toBe("translate(0px, -100px)");
  });
});

describe("regionRectFromBox (viewport box → document fractions)", () => {
  const ORIGIN = "http://127.0.0.1:7778";

  function box(x: number, y: number, w: number, h: number) {
    // regionRectFromBox only reads x/y/w/h; the rect fields are unused here.
    return { left: 0, top: 0, right: 0, bottom: 0, x, y, w, h };
  }

  // Arm the bridge with given page metrics so the module-level PageMetrics is
  // populated the same way production does (via a beacon message).
  function armMetrics(m: { scrollX?: number; scrollY?: number; docW: number; docH: number }) {
    const stage = document.createElement("div");
    stage.setAttribute("lvt-fx:proxy-bridge", "setProxyURL");
    const iframe = document.createElement("iframe");
    iframe.src = ORIGIN + "/";
    stage.appendChild(iframe);
    document.body.appendChild(stage);
    handleProxyBridgeDirectives(document.body, jest.fn());
    window.dispatchEvent(
      new MessageEvent("message", {
        origin: ORIGIN,
        data: { __prereview: true, type: "scroll", scrollX: 0, scrollY: 0, ...m },
      })
    );
  }

  function overlay(width: number, height: number): HTMLElement {
    const el = document.createElement("div");
    el.getBoundingClientRect = jest.fn(
      () => ({ width, height, left: 0, top: 0, right: width, bottom: height, x: 0, y: 0, toJSON() {} }) as DOMRect
    );
    return el;
  }

  beforeEach(() => {
    document.body.innerHTML = "";
    jest.spyOn(console, "warn").mockImplementation(() => {});
  });
  afterEach(() => {
    teardownProxyBridgeForRoot(document.body);
    document.body.innerHTML = "";
    jest.restoreAllMocks();
  });

  it("returns null when no metrics have arrived (too-early drag)", () => {
    // No bridge armed → pageBridgeMetrics is null after teardown in afterEach.
    expect(regionRectFromBox(overlay(1000, 500), box(0.1, 0.1, 0.2, 0.2))).toBeNull();
  });

  it("converts a viewport box to document fractions using scroll + doc size", () => {
    // Overlay = iframe viewport 1000x500; document 1000x5000 scrolled 1000px.
    // A box at y=0.2 of the viewport → docTop = 1000 + 0.2*500 = 1100 → 1100/5000 = 0.22.
    armMetrics({ scrollY: 1000, docW: 1000, docH: 5000 });
    const r = regionRectFromBox(overlay(1000, 500), box(0.1, 0.2, 0.3, 0.4));
    expect(r).not.toBeNull();
    expect(r!.x).toBeCloseTo((0 + 0.1 * 1000) / 1000, 5); // 0.1
    expect(r!.y).toBeCloseTo((1000 + 0.2 * 500) / 5000, 5); // 0.22
    expect(r!.w).toBeCloseTo((0.3 * 1000) / 1000, 5); // 0.3
    expect(r!.h).toBeCloseTo((0.4 * 500) / 5000, 5); // 0.04
  });

  it("clamps document fractions into [0,1]", () => {
    armMetrics({ scrollY: 4900, docW: 1000, docH: 5000 });
    // docTop = 4900 + 0.9*500 = 5350 → 5350/5000 = 1.07 → clamps to 1.
    const r = regionRectFromBox(overlay(1000, 500), box(0, 0.9, 0.5, 0.5));
    expect(r!.y).toBe(1);
    expect(r!.h).toBeLessThanOrEqual(1);
  });
});
