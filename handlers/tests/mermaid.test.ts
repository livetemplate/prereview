import type { ElementContext } from "@livetemplate/client";

type Loader = typeof import("../src/mermaid");

// Each test gets a fresh module, so the cached load promise starts empty.
function freshModule(): Loader {
  let mod!: Loader;
  jest.isolateModules(() => {
    mod = jest.requireActual<Loader>("../src/mermaid");
  });
  return mod;
}

function diagram(def: string, key = "mmd-1"): Element {
  const box = document.createElement("div");
  box.className = "md-mermaid";
  box.setAttribute("data-key", key);
  box.setAttribute("lvt-x:mermaid", "svg");
  const pre = document.createElement("pre");
  pre.className = "mermaid";
  pre.textContent = def;
  box.appendChild(pre);
  document.body.appendChild(box);
  return box;
}

function ctx(el: Element, signal = new AbortController().signal): ElementContext {
  return { value: "svg", wrapperRoot: document.body, signal };
}

// Settles each script tag the loader appends: the first `failures` loads
// error, the rest succeed with a fake mermaid.
function fakeScriptLoads(failures: number, render: (def: string) => Promise<{ svg: string }>) {
  const loads: string[] = [];
  jest.spyOn(document.head, "appendChild").mockImplementation((node) => {
    const s = node as HTMLScriptElement;
    loads.push(s.src);
    queueMicrotask(() => {
      if (loads.length <= failures) {
        s.onerror?.(new Event("error"));
        return;
      }
      window.mermaid = { initialize: jest.fn(), render: jest.fn((_id, def) => render(def)) };
      s.onload?.(new Event("load"));
    });
    return node;
  });
  return loads;
}

const flush = () => new Promise((r) => setTimeout(r, 0));

let warn: jest.SpyInstance;
beforeEach(() => {
  document.body.innerHTML = "";
  delete window.mermaid;
  warn = jest.spyOn(console, "warn").mockImplementation(() => {});
});
afterEach(() => jest.restoreAllMocks());

describe("mermaid handler", () => {
  it("replaces the source with the rendered SVG", async () => {
    const { mermaidHandler } = freshModule();
    fakeScriptLoads(0, async (def) => ({ svg: `<svg data-def="${def}"></svg>` }));
    const box = diagram("graph TD");
    mermaidHandler.onElementAdded!(box, ctx(box));
    await flush();
    expect(box.querySelector("pre.mermaid")).toBeNull();
    expect(box.querySelector(".md-mermaid-svg svg")?.getAttribute("data-def")).toBe("graph TD");
    expect(warn).not.toHaveBeenCalled();
  });

  it("keeps the source visible and warns once on a bad definition", async () => {
    const { mermaidHandler } = freshModule();
    fakeScriptLoads(0, () => Promise.reject(new Error("parse error")));
    const box = diagram("notadiagram", "mmd-bad");
    mermaidHandler.onElementAdded!(box, ctx(box));
    await flush();
    expect(box.classList.contains("md-mermaid-failed")).toBe(true);
    expect(box.querySelector("pre.mermaid")?.textContent).toBe("notadiagram");
    expect(box.querySelectorAll(".md-mermaid-error")).toHaveLength(1);
    expect(warn).toHaveBeenCalledTimes(1);
    expect(warn.mock.calls[0][0]).toContain("mmd-bad");
  });

  it("loads the library once for many diagrams", async () => {
    const { mermaidHandler } = freshModule();
    const loads = fakeScriptLoads(0, async () => ({ svg: "<svg></svg>" }));
    const a = diagram("graph TD", "mmd-same");
    const b = diagram("graph TD", "mmd-same");
    mermaidHandler.onElementAdded!(a, ctx(a));
    mermaidHandler.onElementAdded!(b, ctx(b));
    await flush();
    expect(loads).toHaveLength(1);
    expect(document.querySelectorAll(".md-mermaid-svg svg")).toHaveLength(2);
  });

  it("tries the load again for the next diagram after a failed load", async () => {
    const { mermaidHandler } = freshModule();
    const loads = fakeScriptLoads(1, async () => ({ svg: "<svg></svg>" }));
    const first = diagram("graph TD", "mmd-a");
    mermaidHandler.onElementAdded!(first, ctx(first));
    await flush();
    expect(first.classList.contains("md-mermaid-failed")).toBe(true);

    const second = diagram("graph LR", "mmd-b");
    mermaidHandler.onElementAdded!(second, ctx(second));
    await flush();
    expect(loads).toHaveLength(2);
    expect(second.querySelector(".md-mermaid-svg svg")).not.toBeNull();
  });

  it("leaves a diagram alone once it disarms", async () => {
    const { mermaidHandler } = freshModule();
    fakeScriptLoads(0, async () => ({ svg: "<svg></svg>" }));
    const box = diagram("graph TD");
    const ac = new AbortController();
    mermaidHandler.onElementAdded!(box, ctx(box, ac.signal));
    ac.abort();
    await flush();
    expect(box.querySelector("pre.mermaid")).not.toBeNull();
    expect(box.querySelector(".md-mermaid-svg")).toBeNull();
  });
});
