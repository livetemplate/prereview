// Registers prereview's attribute handlers with the livetemplate client that
// page.tmpl loads beside this bundle.
//
// The import is type-only, and the build marks @livetemplate/client external.
// A runtime import would bundle a second copy of the client, and these
// handlers would register into that copy's registry, which the page never
// runs.
//
// window.lvtAttributes is the client's pre-load queue, so this works whichever
// script runs first: before the client, the client drains the queue when it
// starts; after it, push() registers at once and the registry runs the handler
// against the live page.
import type { LowLevelHandler } from "@livetemplate/client";
import {
  handleAreaSelectDirectives,
  handleAutoClickDirectives,
  handlePreviewBridgeDirectives,
  handleProxyBridgeDirectives,
  handleRegionSelectDirectives,
  handleTextSelectDirectives,
  handleURLHashDirective,
  handleViewportReportDirectives,
  teardownAreaSelectForRoot,
  teardownAutoClickTimers,
  teardownPreviewBridgeForRoot,
  teardownProxyBridgeForRoot,
  teardownRegionSelectForRoot,
  teardownTextSelectForRoot,
  teardownURLHashForRoot,
  teardownViewportReportForRoot,
} from "./directives";
import { handleResizeDirectives, teardownResizeForRoot } from "./resize";
import { setupSpy, teardownSpy } from "./spy";

// The order the client ran them in before they moved here.
export const handlers: LowLevelHandler[] = [
  {
    name: "lvt-fx:auto-click",
    selectors: ["[lvt-fx\\:auto-click]"],
    setup: ({ scanRoot }) => handleAutoClickDirectives(scanRoot),
    // dispose(), not teardown(root): the timers are module-global, so there is
    // no root to scope their cleanup to.
    dispose: () => teardownAutoClickTimers(),
  },
  {
    name: "lvt-fx:area-select",
    selectors: ["[lvt-fx\\:area-select]"],
    needsServerChannel: true,
    setup: ({ scanRoot, send }) => handleAreaSelectDirectives(scanRoot, send!),
    teardown: (root) => teardownAreaSelectForRoot(root),
  },
  {
    name: "lvt-fx:resize",
    selectors: ["[lvt-fx\\:resize]"],
    setup: ({ scanRoot }) => handleResizeDirectives(scanRoot),
    teardown: (root) => teardownResizeForRoot(root),
  },
  {
    name: "lvt-fx:region-select",
    selectors: ["[lvt-fx\\:region-select]"],
    needsServerChannel: true,
    setup: ({ scanRoot, send }) => handleRegionSelectDirectives(scanRoot, send!),
    teardown: (root) => teardownRegionSelectForRoot(root),
  },
  {
    name: "lvt-fx:text-select",
    selectors: ["[lvt-fx\\:text-select]"],
    needsServerChannel: true,
    setup: ({ scanRoot, send }) => handleTextSelectDirectives(scanRoot, send!),
    teardown: (root) => teardownTextSelectForRoot(root),
  },
  {
    name: "lvt-fx:viewport-report",
    selectors: ["[lvt-fx\\:viewport-report]"],
    needsServerChannel: true,
    setup: ({ scanRoot, send }) => handleViewportReportDirectives(scanRoot, send!),
    teardown: (root) => teardownViewportReportForRoot(root),
  },
  {
    name: "lvt-fx:proxy-bridge",
    selectors: ["[lvt-fx\\:proxy-bridge]"],
    needsServerChannel: true,
    setup: ({ scanRoot, send }) => handleProxyBridgeDirectives(scanRoot, send!),
    teardown: (root) => teardownProxyBridgeForRoot(root),
  },
  {
    name: "lvt-fx:preview-bridge",
    selectors: ["iframe[lvt-fx\\:preview-bridge]"],
    setup: ({ scanRoot }) => handlePreviewBridgeDirectives(scanRoot),
    teardown: (root) => teardownPreviewBridgeForRoot(root),
  },
  {
    name: "lvt-fx:url-hash",
    selectors: ["[lvt-fx\\:url-hash]"],
    needsServerChannel: true,
    // A `#path:L4` URL must dispatch on page load, and a page load hydrates
    // without a render that changes anything.
    runOnConnect: true,
    setup: ({ scanRoot, send }) => handleURLHashDirective(scanRoot, send!),
    teardown: (root) => teardownURLHashForRoot(root),
  },
  {
    name: "lvt-spy",
    claims: ["lvt-spy", "lvt-spy-link"],
    selectors: ["[lvt-spy]"],
    setup: ({ scanRoot }) => setupSpy(scanRoot),
    teardown: (root) => teardownSpy(root),
  },
];

export function register(): void {
  (window.lvtAttributes ||= []).push(...handlers);
}
