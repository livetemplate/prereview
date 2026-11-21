import { setupSpy, teardownSpy } from "../src/spy";

// Mock getBoundingClientRect so tests can position headings deterministically.
// jsdom returns all-zero rects by default, which would make every heading
// "above the trigger line" and every test ambiguous.
function setRect(el: Element, top: number): void {
  (el as any).getBoundingClientRect = () => ({
    top,
    bottom: top + 30,
    left: 0,
    right: 100,
    width: 100,
    height: 30,
    x: 0,
    y: top,
    toJSON: () => ({}),
  });
}

describe("setupSpy", () => {
  let rafCallbacks: FrameRequestCallback[];

  beforeEach(() => {
    document.body.replaceChildren();
    rafCallbacks = [];
    jest.spyOn(window, "requestAnimationFrame").mockImplementation((cb) => {
      rafCallbacks.push(cb);
      return rafCallbacks.length;
    });
    // jsdom's innerHeight is 768 by default; pin it for deterministic
    // trigger-line math (25% of 768 = 192).
    Object.defineProperty(window, "innerHeight", { value: 768, configurable: true });
  });

  afterEach(() => {
    teardownSpy();
    jest.restoreAllMocks();
  });

  function flushRAF(): void {
    const cbs = rafCallbacks.splice(0);
    cbs.forEach((cb) => cb(performance.now()));
  }

  function buildFixture(): { article: HTMLElement; h1: HTMLElement; h2: HTMLElement; h3: HTMLElement; linkA: HTMLAnchorElement; linkB: HTMLAnchorElement; linkC: HTMLAnchorElement } {
    document.body.innerHTML = `
      <article lvt-spy="h1, h2, h3">
        <h1 id="intro">Intro</h1>
        <p>Body</p>
        <h2 id="usage">Usage</h2>
        <p>Body</p>
        <h3 id="config">Config</h3>
        <p>Body</p>
      </article>
      <nav>
        <a id="lA" href="#intro" lvt-spy-link>Intro</a>
        <a id="lB" href="#usage" lvt-spy-link>Usage</a>
        <a id="lC" href="#config" lvt-spy-link>Config</a>
      </nav>
    `;
    return {
      article: document.querySelector("article")!,
      h1: document.getElementById("intro")!,
      h2: document.getElementById("usage")!,
      h3: document.getElementById("config")!,
      linkA: document.getElementById("lA") as HTMLAnchorElement,
      linkB: document.getElementById("lB") as HTMLAnchorElement,
      linkC: document.getElementById("lC") as HTMLAnchorElement,
    };
  }

  it("highlights the topmost passed heading on initial paint", () => {
    const f = buildFixture();
    // h1 is above the trigger line; h2 and h3 are below.
    setRect(f.h1, 50);
    setRect(f.h2, 400);
    setRect(f.h3, 800);

    setupSpy(document.body);

    expect(f.linkA.classList.contains("lvt-active")).toBe(true);
    expect(f.linkB.classList.contains("lvt-active")).toBe(false);
    expect(f.linkC.classList.contains("lvt-active")).toBe(false);
  });

  it("advances active link as the user scrolls past each heading", () => {
    const f = buildFixture();
    setRect(f.h1, 50);
    setRect(f.h2, 400);
    setRect(f.h3, 800);
    setupSpy(document.body);

    // Simulate scroll: h2 has now passed the trigger line.
    setRect(f.h1, -200);
    setRect(f.h2, 100);
    setRect(f.h3, 500);
    window.dispatchEvent(new Event("scroll"));
    flushRAF();

    expect(f.linkA.classList.contains("lvt-active")).toBe(false);
    expect(f.linkB.classList.contains("lvt-active")).toBe(true);
    expect(f.linkC.classList.contains("lvt-active")).toBe(false);

    // Scroll further: h3 has passed too.
    setRect(f.h1, -700);
    setRect(f.h2, -400);
    setRect(f.h3, 100);
    window.dispatchEvent(new Event("scroll"));
    flushRAF();

    expect(f.linkA.classList.contains("lvt-active")).toBe(false);
    expect(f.linkB.classList.contains("lvt-active")).toBe(false);
    expect(f.linkC.classList.contains("lvt-active")).toBe(true);
  });

  it("highlights no link when scrolled above the first heading", () => {
    const f = buildFixture();
    // All headings below trigger line.
    setRect(f.h1, 500);
    setRect(f.h2, 800);
    setRect(f.h3, 1100);

    setupSpy(document.body);

    expect(f.linkA.classList.contains("lvt-active")).toBe(false);
    expect(f.linkB.classList.contains("lvt-active")).toBe(false);
    expect(f.linkC.classList.contains("lvt-active")).toBe(false);
  });

  it("optimistically activates a link on click", () => {
    const f = buildFixture();
    // Layout puts h1 as active.
    setRect(f.h1, 50);
    setRect(f.h2, 400);
    setRect(f.h3, 800);
    setupSpy(document.body);
    expect(f.linkA.classList.contains("lvt-active")).toBe(true);

    // User clicks the last link — h3 may never become topmost-visible if
    // the document ends shortly after it, so optimism is what keeps the UI
    // in sync with the user's intent.
    f.linkC.dispatchEvent(new MouseEvent("click", { bubbles: true }));

    expect(f.linkA.classList.contains("lvt-active")).toBe(false);
    expect(f.linkC.classList.contains("lvt-active")).toBe(true);
  });

  it("element-mode: spy directly on the element (empty attribute)", () => {
    document.body.innerHTML = `
      <h2 id="just-one" lvt-spy></h2>
      <a id="L" href="#just-one" lvt-spy-link>Just one</a>
    `;
    const h = document.getElementById("just-one")!;
    const link = document.getElementById("L")!;
    setRect(h, 50);

    setupSpy(document.body);

    expect(link.classList.contains("lvt-active")).toBe(true);
  });

  it("teardownSpy clears active class and removes scroll listener", () => {
    const f = buildFixture();
    setRect(f.h1, 50);
    setRect(f.h2, 400);
    setRect(f.h3, 800);
    setupSpy(document.body);
    expect(f.linkA.classList.contains("lvt-active")).toBe(true);

    teardownSpy();
    expect(f.linkA.classList.contains("lvt-active")).toBe(false);

    // After teardown, a scroll event should not re-pick. Move h2 above the
    // trigger and confirm linkB does NOT get activated.
    setRect(f.h2, 50);
    window.dispatchEvent(new Event("scroll"));
    flushRAF();
    expect(f.linkB.classList.contains("lvt-active")).toBe(false);
  });

  it("teardownSpy(wrapper) clears active class on links OUTSIDE the wrapper", () => {
    // Realistic layout: nav at the top of the document, content wrapper
    // below containing the spy targets. applyActive() queries links
    // globally, so the nav link picks up lvt-active even though it sits
    // outside the wrapper. A wrapper-scoped teardown must still scrub
    // those classes — otherwise disconnecting leaves the nav with a
    // stale highlight forever.
    document.body.innerHTML = `
      <nav id="topnav">
        <a id="navlink" href="#deep" lvt-spy-link>Deep</a>
      </nav>
      <div id="content">
        <article lvt-spy="h2">
          <h2 id="deep">Deep</h2>
        </article>
      </div>
    `;
    const navlink = document.getElementById("navlink")!;
    const target = document.getElementById("deep")!;
    const wrapper = document.getElementById("content")!;
    setRect(target, 50);

    setupSpy(document.body);
    expect(navlink.classList.contains("lvt-active")).toBe(true);

    // Wrapper-scoped teardown: the content wrapper is what disconnects.
    teardownSpy(wrapper);
    expect(navlink.classList.contains("lvt-active")).toBe(false);
  });

  it("swallows invalid lvt-spy selectors instead of aborting the scan", () => {
    // Two spy containers — the first has a typo that would throw
    // SyntaxError from querySelectorAll, the second is well-formed. The
    // bad container must not prevent the good one from initializing.
    document.body.innerHTML = `
      <article id="broken" lvt-spy="h1, h2,">
        <h1 id="x">X</h1>
      </article>
      <article id="ok" lvt-spy="h2">
        <h2 id="y">Y</h2>
      </article>
      <a id="L" href="#y" lvt-spy-link>Y</a>
    `;
    const link = document.getElementById("L")!;
    setRect(document.getElementById("y")!, 50);

    const warn = jest.spyOn(console, "warn").mockImplementation(() => {});
    setupSpy(document.body);

    expect(warn).toHaveBeenCalled();
    expect(link.classList.contains("lvt-active")).toBe(true);
    warn.mockRestore();
  });

  it("teardownSpy(wrapper) removes the click handler when no bindings remain", () => {
    // The framework always calls teardownSpy(this.wrapperElement). If
    // the click handler is gated on (!wrapper) it survives every
    // teardown and clicks on stray [lvt-spy-link] elements continue to
    // re-apply lvt-active — with no scroll-driven reconciliation to
    // undo it. Gate on the binding count instead so the handler comes
    // off whenever the last binding is gone.
    const f = buildFixture();
    setRect(f.h1, 50);
    setRect(f.h2, 400);
    setRect(f.h3, 800);
    setupSpy(document.body);
    expect(f.linkA.classList.contains("lvt-active")).toBe(true);

    teardownSpy(f.article);
    expect(f.linkA.classList.contains("lvt-active")).toBe(false);

    // A click after teardown must NOT re-apply lvt-active. Before the
    // gate fix, the optimistic click handler survived teardown and
    // would put lvt-active back on the clicked link forever.
    f.linkC.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    expect(f.linkC.classList.contains("lvt-active")).toBe(false);
  });

  it("readMarginPx rejects unsupported units and falls back to 25vh", () => {
    document.body.innerHTML = `
      <article id="container" lvt-spy="h2" style="--lvt-spy-margin: 2rem;">
        <h2 id="h1" style="position: relative; top: 100px;">H1</h2>
      </article>
      <a id="L" href="#h1" lvt-spy-link>H1</a>
    `;
    const h = document.getElementById("h1")!;
    const link = document.getElementById("L")!;
    // 25vh of 768 = 192. Bare px from 2rem (i.e. 2) would treat the
    // trigger line at 2px, so a heading at top=100 would be BELOW the
    // line and NOT active. Correct behaviour: fall back to 25vh = 192,
    // heading at top=100 is above → active.
    setRect(h, 100);
    const warn = jest.spyOn(console, "warn").mockImplementation(() => {});

    setupSpy(document.body);

    expect(warn).toHaveBeenCalled();
    expect(link.classList.contains("lvt-active")).toBe(true);
    warn.mockRestore();
  });

  it("readMarginPx accepts explicit px unit", () => {
    document.body.innerHTML = `
      <article id="container" lvt-spy="h2" style="--lvt-spy-margin: 300px;">
        <h2 id="h2only">Above</h2>
      </article>
      <a id="L" href="#h2only" lvt-spy-link>Above</a>
    `;
    setRect(document.getElementById("h2only")!, 250);
    setupSpy(document.body);
    // 250 < 300 → active.
    expect(document.getElementById("L")!.classList.contains("lvt-active")).toBe(true);
  });

  it("warns once when a target has no id", () => {
    document.body.innerHTML = `
      <article lvt-spy="h2">
        <h2 id="ok">OK</h2>
        <h2>No id</h2>
      </article>
      <a href="#ok" lvt-spy-link>OK</a>
    `;
    setRect(document.getElementById("ok")!, 50);
    const warn = jest.spyOn(console, "warn").mockImplementation(() => {});

    setupSpy(document.body);

    expect(warn).toHaveBeenCalled();
    const calls = warn.mock.calls.map((args) => String(args[0]));
    expect(calls.some((s) => s.includes("without an id"))).toBe(true);
    warn.mockRestore();
  });

  it("does not re-warn about the same id-less target on re-attach", () => {
    // A morphdom render that changes the target set triggers
    // detach+attach. Without per-element dedup, the missing-id warning
    // would re-fire every render and spam the console for any page
    // that legitimately has an id-less heading the author hasn't
    // gotten around to fixing.
    document.body.innerHTML = `
      <article lvt-spy="h2">
        <h2 id="a">A</h2>
        <h2>No id</h2>
      </article>
      <a href="#a" lvt-spy-link>A</a>
    `;
    setRect(document.getElementById("a")!, 50);
    const warn = jest.spyOn(console, "warn").mockImplementation(() => {});

    setupSpy(document.body);
    const firstWarnCount = warn.mock.calls.filter((args) =>
      String(args[0]).includes("without an id"),
    ).length;
    expect(firstWarnCount).toBe(1);

    // Simulate a morphdom-style structural update: append a brand-new
    // heading WITH an id. processContainer detects the target set
    // changed → detaches the old binding → attaches a new one.
    const newH = document.createElement("h2");
    newH.id = "b";
    document.querySelector("article")!.appendChild(newH);
    setRect(newH, 80);

    setupSpy(document.body);
    const secondWarnCount = warn.mock.calls.filter((args) =>
      String(args[0]).includes("without an id"),
    ).length;
    // The id-less target is the SAME element — must not warn again.
    expect(secondWarnCount).toBe(1);
    warn.mockRestore();
  });

  it("picks the latest passed target even when targets are visually out of order", () => {
    // A real document-order h2 list, but the second heading is visually
    // shifted ABOVE the first via getBoundingClientRect (simulating
    // sticky / transform / flex-order layouts). The early-break
    // optimisation would have bailed on the first heading and missed
    // that the second one is actually above the trigger line too.
    document.body.innerHTML = `
      <article lvt-spy="h2">
        <h2 id="alpha">Alpha</h2>
        <h2 id="beta">Beta</h2>
      </article>
      <a id="lA" href="#alpha" lvt-spy-link>Alpha</a>
      <a id="lB" href="#beta" lvt-spy-link>Beta</a>
    `;
    // alpha is visually BELOW the trigger; beta is visually ABOVE it
    // (think: beta is a sticky header that latched to the top).
    setRect(document.getElementById("alpha")!, 400);
    setRect(document.getElementById("beta")!, 50);
    setupSpy(document.body);
    // Beta is the last document-order target whose top is above the
    // trigger, so it wins. With the old early-break this assertion
    // would have failed (the iteration would have bailed on alpha).
    expect(document.getElementById("lB")!.classList.contains("lvt-active")).toBe(true);
    expect(document.getElementById("lA")!.classList.contains("lvt-active")).toBe(false);
  });

  it("teardownSpy(wrapper) preserves active class on links of surviving bindings", () => {
    // Two independent spy containers, each with its own link. Tearing
    // down one must NOT blank the other's active highlight — only
    // links pointing to a torn-down target should be cleared. Without
    // the surviving-id check, multi-instance setups would flicker
    // every time a peer disconnected.
    document.body.innerHTML = `
      <article id="A" lvt-spy="h2">
        <h2 id="alpha">Alpha</h2>
      </article>
      <article id="B" lvt-spy="h2">
        <h2 id="beta">Beta</h2>
      </article>
      <a id="lA" href="#alpha" lvt-spy-link>A</a>
      <a id="lB" href="#beta" lvt-spy-link>B</a>
    `;
    setRect(document.getElementById("alpha")!, 50);
    setRect(document.getElementById("beta")!, 50);
    setupSpy(document.body);

    expect(document.getElementById("lA")!.classList.contains("lvt-active")).toBe(true);
    expect(document.getElementById("lB")!.classList.contains("lvt-active")).toBe(true);

    // Tear down container A only.
    teardownSpy(document.getElementById("A")!);

    // lA's target was in A — it should lose lvt-active.
    expect(document.getElementById("lA")!.classList.contains("lvt-active")).toBe(false);
    // lB points to beta which lives in still-alive B — must keep its highlight.
    expect(document.getElementById("lB")!.classList.contains("lvt-active")).toBe(true);
  });

  it("readMarginPx accepts unitless decimal values", () => {
    // `String(parseFloat("200.0"))` is "200", not "200.0", so the
    // identity check missed this — the regex grammar fixes it.
    document.body.innerHTML = `
      <article id="container" lvt-spy="h2" style="--lvt-spy-margin: 200.0;">
        <h2 id="h2only">Above</h2>
      </article>
      <a id="L" href="#h2only" lvt-spy-link>Above</a>
    `;
    setRect(document.getElementById("h2only")!, 150);
    const warn = jest.spyOn(console, "warn").mockImplementation(() => {});
    setupSpy(document.body);
    // 150 < 200 → active. If the regex misfired, the value would fall
    // back to 25vh = 192, also above 150 — so to actually exercise the
    // regex we also assert no fallback warning fired.
    expect(document.getElementById("L")!.classList.contains("lvt-active")).toBe(true);
    const unitWarns = warn.mock.calls.filter((args) =>
      String(args[0]).includes("unsupported --lvt-spy-margin unit"),
    );
    expect(unitWarns).toHaveLength(0);
    warn.mockRestore();
  });

  it("clears lvt-active on links whose target is removed in a morphdom update", () => {
    // The existing 'morphdom re-attaches' case only covers ADDING a
    // heading. Removing one used to leave the orphaned link with a
    // stale lvt-active forever, because processContainer's detach()
    // didn't clear classes. detach() now clears as part of its contract.
    document.body.innerHTML = `
      <article lvt-spy="h2">
        <h2 id="keep">Keep</h2>
        <h2 id="drop">Drop</h2>
      </article>
      <nav>
        <a id="lKeep" href="#keep" lvt-spy-link>Keep</a>
        <a id="lDrop" href="#drop" lvt-spy-link>Drop</a>
      </nav>
    `;
    const keep = document.getElementById("keep")!;
    const drop = document.getElementById("drop")!;
    setRect(keep, 50);
    setRect(drop, 80);
    setupSpy(document.body);
    // Both above the trigger; drop is last in document order.
    expect(document.getElementById("lDrop")!.classList.contains("lvt-active")).toBe(true);

    // Morphdom-style removal: the second heading goes away.
    drop.remove();
    // setupSpy gets called again on each render — processContainer
    // detects the new target set and re-attaches.
    setupSpy(document.body);

    // The orphaned link must lose lvt-active.
    expect(document.getElementById("lDrop")!.classList.contains("lvt-active")).toBe(false);
    // Keep should now be the active one.
    expect(document.getElementById("lKeep")!.classList.contains("lvt-active")).toBe(true);
  });

  it("re-attaches when target set changes (morphdom-style update)", () => {
    const f = buildFixture();
    setRect(f.h1, 50);
    setRect(f.h2, 400);
    setRect(f.h3, 800);
    setupSpy(document.body);
    expect(f.linkA.classList.contains("lvt-active")).toBe(true);

    // Simulate morphdom adding a new heading at the top.
    const h0 = document.createElement("h1");
    h0.id = "preamble";
    setRect(h0, 10);
    f.article.insertBefore(h0, f.article.firstChild);
    setRect(f.h1, 300); // pushed down by new heading

    // Add a corresponding link.
    const linkZ = document.createElement("a");
    linkZ.setAttribute("href", "#preamble");
    linkZ.setAttribute("lvt-spy-link", "");
    document.querySelector("nav")!.insertBefore(linkZ, f.linkA);

    setupSpy(document.body);
    expect(linkZ.classList.contains("lvt-active")).toBe(true);
    expect(f.linkA.classList.contains("lvt-active")).toBe(false);
  });
});
