/**
 * lvt-fx:viewport-report directive — unit coverage for the deterministic
 * reporting logic: it reports the topmost + bottommost visible tracked key,
 * suppresses unchanged repeats, re-arms idempotently, and tears down cleanly.
 * jsdom has no layout, so each element's getBoundingClientRect is mocked to
 * position it relative to the container's viewport.
 */
import {
  handleViewportReportDirectives,
  teardownViewportReportForRoot,
} from "../src/directives";

function rect(top: number, bottom: number): DOMRect {
  return {
    top,
    bottom,
    left: 0,
    right: 0,
    width: 0,
    height: bottom - top,
    x: 0,
    y: top,
    toJSON() {},
  } as DOMRect;
}

// The container viewport is 0..100; each line carries its own [top,bottom].
function makeContainer(lines: { key: string; top: number; bottom: number }[]) {
  document.body.replaceChildren();
  const c = document.createElement("main");
  c.setAttribute("lvt-fx:viewport-report", "reportViewport");
  c.setAttribute("data-lvt-viewport-items", ".line");
  c.getBoundingClientRect = () => rect(0, 100);
  for (const l of lines) {
    const el = document.createElement("div");
    el.className = "line";
    el.setAttribute("data-key", l.key);
    el.getBoundingClientRect = () => rect(l.top, l.bottom);
    c.appendChild(el);
  }
  document.body.appendChild(c);
  return c;
}

const SAMPLE = [
  { key: "L1-1", top: -40, bottom: -20 }, // fully above the viewport
  { key: "L2-2", top: -10, bottom: 10 }, // overlaps the top edge
  { key: "L3-3", top: 30, bottom: 50 }, // fully inside
  { key: "L4-4", top: 90, bottom: 110 }, // overlaps the bottom edge
  { key: "L5-5", top: 120, bottom: 140 }, // fully below the viewport
];

beforeEach(() => jest.useFakeTimers());
afterEach(() => {
  teardownViewportReportForRoot(document.body);
  jest.clearAllTimers();
  jest.useRealTimers();
});

describe("lvt-fx:viewport-report", () => {
  it("reports the topmost and bottommost visible line keys", () => {
    const sends: any[] = [];
    makeContainer(SAMPLE);
    handleViewportReportDirectives(document.body, (m) => sends.push(m));
    jest.advanceTimersByTime(300); // past the 250ms debounce (initial schedule)
    expect(sends).toEqual([
      { action: "reportViewport", data: { topKey: "L2-2", bottomKey: "L4-4" } },
    ]);
  });

  it("suppresses a duplicate report when the visible extremes are unchanged", () => {
    const sends: any[] = [];
    const c = makeContainer(SAMPLE);
    handleViewportReportDirectives(document.body, (m) => sends.push(m));
    jest.advanceTimersByTime(300); // initial report
    c.dispatchEvent(new Event("scroll")); // same geometry (mocks unchanged)
    jest.advanceTimersByTime(300);
    expect(sends).toHaveLength(1);
  });

  it("reports again when the visible range changes (a real scroll)", () => {
    const sends: any[] = [];
    const c = makeContainer(SAMPLE);
    handleViewportReportDirectives(document.body, (m) => sends.push(m));
    jest.advanceTimersByTime(300);
    // Simulate scrolling down: L4/L5 now straddle the viewport, L2 leaves.
    const relayout: Record<string, [number, number]> = {
      "L1-1": [-140, -120],
      "L2-2": [-110, -90],
      "L3-3": [-70, -50],
      "L4-4": [-10, 10],
      "L5-5": [20, 40],
    };
    c.querySelectorAll<HTMLElement>(".line").forEach((el) => {
      const [t, b] = relayout[el.getAttribute("data-key")!];
      el.getBoundingClientRect = () => rect(t, b);
    });
    c.dispatchEvent(new Event("scroll"));
    jest.advanceTimersByTime(300);
    expect(sends).toHaveLength(2);
    expect(sends[1]).toEqual({
      action: "reportViewport",
      data: { topKey: "L4-4", bottomKey: "L5-5" },
    });
  });

  it("re-arm is idempotent — no duplicate listeners", () => {
    const sends: any[] = [];
    const c = makeContainer(SAMPLE);
    handleViewportReportDirectives(document.body, (m) => sends.push(m));
    handleViewportReportDirectives(document.body, (m) => sends.push(m)); // re-scan
    jest.advanceTimersByTime(300);
    expect(sends).toHaveLength(1); // only the single initial report
    // A scroll that changes the range fires exactly once, not twice.
    c.querySelectorAll<HTMLElement>(".line").forEach((el) => {
      el.getBoundingClientRect = () => rect(200, 220); // all below → nothing visible
    });
    // put one back in view so there IS a change
    (c.querySelector('[data-key="L3-3"]') as HTMLElement).getBoundingClientRect =
      () => rect(40, 60);
    c.dispatchEvent(new Event("scroll"));
    jest.advanceTimersByTime(300);
    expect(sends).toHaveLength(2);
  });

  it("teardown stops reporting", () => {
    const sends: any[] = [];
    const c = makeContainer(SAMPLE);
    handleViewportReportDirectives(document.body, (m) => sends.push(m));
    jest.advanceTimersByTime(300);
    teardownViewportReportForRoot(document.body);
    c.querySelectorAll<HTMLElement>(".line").forEach((el) => {
      el.getBoundingClientRect = () => rect(40, 60);
    });
    c.dispatchEvent(new Event("scroll"));
    jest.advanceTimersByTime(300);
    expect(sends).toHaveLength(1); // no new reports after teardown
  });
});
