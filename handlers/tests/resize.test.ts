/**
 * lvt-fx:resize directive — unit coverage for the deterministic, non-pointer
 * logic: restore-from-localStorage with clamping, idempotent re-arm, sweep on
 * attribute removal, and the missing-var warning. The live pointer drag is
 * covered end-to-end by the prereview chromedp test (jsdom has no PointerEvent).
 */
import { handleResizeDirectives, teardownResizeForRoot } from "../src/resize";

function makeHost(attrs: Record<string, string>): HTMLElement {
  document.body.replaceChildren();
  document.documentElement.style.removeProperty("--w");
  const wrapper = document.createElement("div");
  wrapper.setAttribute("data-lvt-id", "test");
  const host = document.createElement("aside");
  for (const [k, v] of Object.entries(attrs)) host.setAttribute(k, v);
  const handle = document.createElement("div");
  handle.className = "resize-handle";
  host.appendChild(handle);
  wrapper.appendChild(host);
  document.body.appendChild(wrapper);
  return host;
}

const baseAttrs = {
  "lvt-fx:resize": "--w",
  "data-resize-handle": ".resize-handle",
  "data-resize-min": "200",
  "data-resize-max": "560",
  "data-resize-store": "test.w",
};

afterEach(() => {
  localStorage.clear();
  teardownResizeForRoot(document.body);
});

describe("lvt-fx:resize restore + clamp", () => {
  it("restores a persisted width within range onto :root", () => {
    localStorage.setItem("test.w", "350");
    makeHost(baseAttrs);
    handleResizeDirectives(document.body);
    expect(document.documentElement.style.getPropertyValue("--w")).toBe("350px");
  });

  it("clamps a persisted width above max", () => {
    localStorage.setItem("test.w", "5000");
    makeHost(baseAttrs);
    handleResizeDirectives(document.body);
    expect(document.documentElement.style.getPropertyValue("--w")).toBe("560px");
  });

  it("clamps a persisted width below min", () => {
    localStorage.setItem("test.w", "50");
    makeHost(baseAttrs);
    handleResizeDirectives(document.body);
    expect(document.documentElement.style.getPropertyValue("--w")).toBe("200px");
  });

  it("teardownResizeForRoot clears the :root custom property", () => {
    localStorage.setItem("test.w", "350");
    makeHost(baseAttrs);
    handleResizeDirectives(document.body);
    expect(document.documentElement.style.getPropertyValue("--w")).toBe("350px");
    teardownResizeForRoot(document.body);
    expect(document.documentElement.style.getPropertyValue("--w")).toBe("");
  });

  it("warns and falls back to the host when the handle selector misses", () => {
    const warn = jest.spyOn(console, "warn").mockImplementation(() => {});
    makeHost({ ...baseAttrs, "data-resize-handle": ".does-not-exist" });
    handleResizeDirectives(document.body);
    expect(warn).toHaveBeenCalledWith(
      expect.stringContaining("matched no element")
    );
    warn.mockRestore();
  });

  it("does nothing when there is no persisted width", () => {
    makeHost(baseAttrs);
    handleResizeDirectives(document.body);
    expect(document.documentElement.style.getPropertyValue("--w")).toBe("");
  });

  it("ignores a non-numeric persisted value", () => {
    localStorage.setItem("test.w", "wide");
    makeHost(baseAttrs);
    handleResizeDirectives(document.body);
    expect(document.documentElement.style.getPropertyValue("--w")).toBe("");
  });
});

describe("lvt-fx:resize arm/sweep lifecycle", () => {
  it("is idempotent — re-running does not throw or duplicate work", () => {
    localStorage.setItem("test.w", "300");
    makeHost(baseAttrs);
    handleResizeDirectives(document.body);
    expect(() => handleResizeDirectives(document.body)).not.toThrow();
    expect(document.documentElement.style.getPropertyValue("--w")).toBe("300px");
  });

  it("warns and skips when the var name is empty", () => {
    const warn = jest.spyOn(console, "warn").mockImplementation(() => {});
    const host = makeHost({ ...baseAttrs, "lvt-fx:resize": "" });
    handleResizeDirectives(document.body);
    expect(warn).toHaveBeenCalled();
    expect(host.hasAttribute("data-resizing")).toBe(false);
    warn.mockRestore();
  });

  it("warns and skips when the value is not a custom property (footgun guard)", () => {
    const warn = jest.spyOn(console, "warn").mockImplementation(() => {});
    localStorage.setItem("test.w", "350");
    makeHost({ ...baseAttrs, "lvt-fx:resize": "width" });
    handleResizeDirectives(document.body);
    expect(warn).toHaveBeenCalled();
    // Must NOT have written a real `width` property onto :root.
    expect(document.documentElement.style.getPropertyValue("width")).toBe("");
    warn.mockRestore();
  });

  it("warns when two connected elements share the same resize var", () => {
    const warn = jest.spyOn(console, "warn").mockImplementation(() => {});
    document.body.replaceChildren();
    const wrap = document.createElement("div");
    wrap.setAttribute("data-lvt-id", "t");
    for (let i = 0; i < 2; i++) {
      const host = document.createElement("aside");
      for (const [k, v] of Object.entries(baseAttrs)) host.setAttribute(k, v);
      const h = document.createElement("div");
      h.className = "resize-handle";
      host.appendChild(h);
      wrap.appendChild(host);
    }
    document.body.appendChild(wrap);
    handleResizeDirectives(document.body);
    expect(warn).toHaveBeenCalledWith(
      expect.stringContaining("already controlled")
    );
    warn.mockRestore();
  });

  it("sweeps the entry when the attribute is removed", () => {
    const host = makeHost(baseAttrs);
    handleResizeDirectives(document.body);
    // Removing the directive attribute and re-running should clean up without
    // error (no listeners left dangling on the handle).
    host.removeAttribute("lvt-fx:resize");
    expect(() => handleResizeDirectives(document.body)).not.toThrow();
  });
});
