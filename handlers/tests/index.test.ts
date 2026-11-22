import { handlers, register } from "../src/index";

afterEach(() => {
  delete window.lvtAttributes;
});

describe("prereview's handler registration", () => {
  it("keeps the order the client ran them in", () => {
    expect(handlers.map((h) => h.name)).toEqual([
      "lvt-fx:auto-click",
      "lvt-fx:area-select",
      "lvt-fx:resize",
      "lvt-fx:region-select",
      "lvt-fx:text-select",
      "lvt-fx:viewport-report",
      "lvt-fx:proxy-bridge",
      "lvt-fx:preview-bridge",
      "lvt-fx:url-hash",
      "lvt-spy",
    ]);
  });

  it("grants the server channel to exactly the handlers that dispatch actions", () => {
    expect(handlers.filter((h) => h.needsServerChannel).map((h) => h.name).sort()).toEqual([
      "lvt-fx:area-select",
      "lvt-fx:proxy-bridge",
      "lvt-fx:region-select",
      "lvt-fx:text-select",
      "lvt-fx:url-hash",
      "lvt-fx:viewport-report",
    ]);
  });

  it("runs only url-hash on connect, so a #path:L4 link dispatches on page load", () => {
    expect(handlers.filter((h) => h.runOnConnect).map((h) => h.name)).toEqual(["lvt-fx:url-hash"]);
  });

  it("declares root-less cleanup only for auto-click's module-global timers", () => {
    expect(handlers.filter((h) => h.dispose).map((h) => h.name)).toEqual(["lvt-fx:auto-click"]);
  });

  it("queues every handler when it loads before the client", () => {
    register();
    expect(window.lvtAttributes).toEqual(handlers);
  });

  it("pushes into the client's installed queue when it loads after the client", () => {
    const push = jest.fn((...h: unknown[]) => h.length);
    window.lvtAttributes = { push };
    register();
    expect(push).toHaveBeenCalledWith(...handlers);
  });
});
