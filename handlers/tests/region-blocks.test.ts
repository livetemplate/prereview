/**
 * lineRangeFromBlocks — the HTML preview's region hit-test over block rects the
 * in-iframe bridge posts. Content blocks union; `wrap` blocks (pure page
 * wrappers) only answer when the box hits no content block.
 */
import { lineRangeFromBlocks, PreviewBlock } from "../src/directives";

const block = (from: number, to: number, top: number, bottom: number, wrap = false): PreviewBlock => ({
  from,
  to,
  left: 0,
  right: 400,
  top,
  bottom,
  ...(wrap ? { wrap: true } : {}),
});

// A page: an outer wrapper (lines 1–40) around a main wrapper (5–30) holding two
// cards with a 40px gap between them.
const blocks: PreviewBlock[] = [
  block(1, 40, 0, 1000, true),
  block(5, 30, 100, 600, true),
  block(8, 9, 120, 200),
  block(20, 22, 240, 320),
];
const box = (top: number, bottom: number) => ({ left: 50, right: 150, top, bottom });

describe("lineRangeFromBlocks", () => {
  it("unions only the content blocks a box touches, never the wrappers around them", () => {
    expect(lineRangeFromBlocks(blocks, box(130, 150), 0, 0)).toEqual({ from: 8, to: 9 });
    expect(lineRangeFromBlocks(blocks, box(150, 260), 0, 0)).toEqual({ from: 8, to: 22 });
  });

  it("falls back to the smallest wrapper when the box hits no content block", () => {
    // the gap between the cards: inside both wrappers, the inner one is smaller
    expect(lineRangeFromBlocks(blocks, box(205, 235), 0, 0)).toEqual({ from: 5, to: 30 });
    // below main, inside only the outer wrapper
    expect(lineRangeFromBlocks(blocks, box(700, 720), 0, 0)).toEqual({ from: 1, to: 40 });
  });

  it("ignores a wrapper the box touches when it also touches content", () => {
    // the box spans the inner wrapper's padding above the first card and the card
    expect(lineRangeFromBlocks(blocks, box(105, 140), 0, 0)).toEqual({ from: 8, to: 9 });
  });

  it("shifts blocks by the iframe offset on the wrapper path too", () => {
    // blocks are iframe-viewport coords; the iframe sits 30px down the page
    expect(lineRangeFromBlocks(blocks, box(235, 265), 0, 30)).toEqual({ from: 5, to: 30 });
    expect(lineRangeFromBlocks(blocks, box(165, 180), 0, 30)).toEqual({ from: 8, to: 9 });
  });

  it("breaks an equal-area wrapper tie by the narrower line span", () => {
    const tie: PreviewBlock[] = [block(1, 50, 0, 100, true), block(2, 10, 0, 100, true)];
    expect(lineRangeFromBlocks(tie, box(10, 20), 0, 0)).toEqual({ from: 2, to: 10 });
  });

  it("returns null when the box touches nothing", () => {
    expect(lineRangeFromBlocks(blocks, { left: 500, right: 600, top: 10, bottom: 20 }, 0, 0)).toBeNull();
  });

  it("treats blocks without the flag as content (older bridges)", () => {
    const old = blocks.map(({ wrap: _w, ...b }) => b);
    expect(lineRangeFromBlocks(old, box(130, 150), 0, 0)).toEqual({ from: 1, to: 40 });
  });
});
