import test from "node:test";
import assert from "node:assert/strict";

import {
  guardAsync,
  guardCall,
  guardRender,
  installGlobalErrorReporting,
  renderBoundaryErrorHTML,
  withErrorBoundary,
} from "./error-boundary.mjs";
import { createSettingsPanelRegistry } from "./settings-panel-registry.mjs";

// console.error is the boundary's diagnostic channel, so tests silence it and
// assert on it rather than letting it litter the run.
function captureConsoleError(run) {
  const previous = console.error;
  const entries = [];
  console.error = (...args) => entries.push(args);
  try {
    return { result: run(), entries };
  } finally {
    console.error = previous;
  }
}

async function captureConsoleErrorAsync(run) {
  const previous = console.error;
  const entries = [];
  console.error = (...args) => entries.push(args);
  try {
    return { result: await run(), entries };
  } finally {
    console.error = previous;
  }
}

test("guardRender returns markup unchanged when nothing throws", () => {
  const { result, entries } = captureConsoleError(() => guardRender("panel", (name) => `<p>${name}</p>`, "ok"));
  assert.equal(result, "<p>ok</p>");
  assert.equal(entries.length, 0);
});

test("guardRender substitutes a card instead of letting the throw escape", () => {
  const { result, entries } = captureConsoleError(() => guardRender("panel.broken", () => {
    throw new Error("render exploded");
  }));
  assert.match(result, /error-boundary-card/);
  assert.match(result, /panel\.broken: render exploded/);
  assert.equal(entries.length, 1);
  assert.match(String(entries[0][0]), /panel\.broken failed/);
});

test("the boundary card escapes the failure detail rather than injecting it", () => {
  const { result } = captureConsoleError(() => guardRender("panel", () => {
    throw new Error("<img src=x onerror=boom>");
  }));
  assert.doesNotMatch(result, /<img/);
  assert.match(result, /&lt;img/);
});

test("a thrown non-Error still produces a readable card", () => {
  const { result } = captureConsoleError(() => guardRender("panel", () => { throw "just a string"; }));
  assert.match(result, /just a string/);
  const blank = captureConsoleError(() => guardRender("panel", () => { throw null; }));
  assert.match(blank.result, /unknown error/);
});

test("guardCall reports a failing side effect without rethrowing", () => {
  const { result, entries } = captureConsoleError(() => guardCall("panel.bind", () => {
    throw new Error("bind exploded");
  }));
  assert.equal(result, false);
  assert.equal(entries.length, 1);
});

test("guardCall passes arguments through and reports success", () => {
  const seen = [];
  const { result } = captureConsoleError(() => guardCall("panel.bind", (...args) => seen.push(args), 1, 2));
  assert.equal(result, true);
  assert.deepEqual(seen, [[1, 2]]);
});

test("guardCall treats a missing function as nothing to do", () => {
  const { result, entries } = captureConsoleError(() => guardCall("panel.bind", undefined));
  assert.equal(result, true);
  assert.equal(entries.length, 0);
});

test("guardAsync resolves to the fallback instead of rejecting", async () => {
  const { result, entries } = await captureConsoleErrorAsync(() => guardAsync("lazy.panel", async () => {
    throw new Error("import failed");
  }, { fallback: "fallback" }));
  assert.equal(result, "fallback");
  assert.equal(entries.length, 1);
});

test("guardAsync passes a successful value through", async () => {
  const { result } = await captureConsoleErrorAsync(() => guardAsync("lazy.panel", async () => "loaded"));
  assert.equal(result, "loaded");
});

test("withErrorBoundary preserves layout and only adds bind when the panel had one", () => {
  const wrapped = withErrorBoundary({ render: () => "x", layout: "about" }, "settings.about");
  assert.equal(wrapped.layout, "about");
  assert.equal(wrapped.bind, undefined);
  const withBind = withErrorBoundary({ render: () => "x", bind: () => {} }, "settings.other");
  assert.equal(typeof withBind.bind, "function");
});

// The point of wiring boundaries into the registry: a broken panel must not be
// able to take down the navigation that opened it.
test("a registered panel that throws yields a card rather than breaking the caller", () => {
  const registry = createSettingsPanelRegistry();
  registry.register("broken", {
    render: () => { throw new Error("panel is broken"); },
    bind: () => { throw new Error("bind is broken"); },
  });
  const panel = registry.resolve("broken");
  const { result, entries } = captureConsoleError(() => panel.render({ key: "broken" }));
  assert.match(result, /error-boundary-card/);
  assert.match(result, /settings\.broken: panel is broken/);
  // bind failing separately must also stay contained.
  const bindCapture = captureConsoleError(() => panel.bind({ key: "broken" }));
  assert.equal(bindCapture.result, false);
  assert.ok(entries.length >= 1);
});

test("a healthy registered panel is unaffected by the boundary", () => {
  const registry = createSettingsPanelRegistry();
  let bound = 0;
  registry.register("fine", { render: (item) => `<p>${item.key}</p>`, bind: () => { bound += 1; } });
  const panel = registry.resolve("fine");
  assert.equal(panel.render({ key: "fine" }), "<p>fine</p>");
  panel.bind({ key: "fine" });
  assert.equal(bound, 1);
});

test("registry validation still rejects malformed definitions", () => {
  const registry = createSettingsPanelRegistry();
  assert.throws(() => registry.register("", { render: () => "" }), TypeError);
  assert.throws(() => registry.register("x", {}), TypeError);
  assert.throws(() => registry.register("x", { render: () => "", bind: "nope" }), TypeError);
  registry.register("dup", { render: () => "" });
  assert.throws(() => registry.register("dup", { render: () => "" }), /already registered/);
});

function fakeGlobal() {
  const listeners = new Map();
  const documentListeners = new Map();
  let reloaded = 0;
  return {
    reloadCount: () => reloaded,
    addEventListener: (type, handler) => listeners.set(type, handler),
    removeEventListener: (type) => listeners.delete(type),
    emit: (type, event) => listeners.get(type)?.(event),
    emitClick: (event) => documentListeners.get("click")?.(event),
    hasListener: (type) => listeners.has(type),
    location: { reload: () => { reloaded += 1; } },
    document: {
      addEventListener: (type, handler) => documentListeners.set(type, handler),
      removeEventListener: (type) => documentListeners.delete(type),
    },
  };
}

test("global reporting records window errors and unhandled rejections", () => {
  const target = fakeGlobal();
  const seen = [];
  const { entries } = captureConsoleError(() => {
    installGlobalErrorReporting({ target, onReport: (error) => seen.push(error) });
    target.emit("error", { error: new Error("listener blew up") });
    target.emit("unhandledrejection", { reason: new Error("nobody awaited") });
  });
  assert.equal(seen.length, 2);
  assert.equal(entries.length, 2);
});

test("the boundary card's reload button is handled by one delegated listener", () => {
  const target = fakeGlobal();
  captureConsoleError(() => {
    installGlobalErrorReporting({ target });
    target.emitClick({ target: { closest: (selector) => (selector.includes("reload") ? {} : null) } });
  });
  assert.equal(target.reloadCount(), 1);
});

test("clicks elsewhere do not reload", () => {
  const target = fakeGlobal();
  captureConsoleError(() => {
    installGlobalErrorReporting({ target });
    target.emitClick({ target: { closest: () => null } });
  });
  assert.equal(target.reloadCount(), 0);
});

test("installing twice does not stack duplicate listeners", () => {
  const target = fakeGlobal();
  const seen = [];
  captureConsoleError(() => {
    installGlobalErrorReporting({ target, onReport: (error) => seen.push(error) });
    installGlobalErrorReporting({ target, onReport: (error) => seen.push(error) });
    target.emit("error", { error: new Error("once") });
  });
  assert.equal(seen.length, 1);
});

test("the returned disposer removes what it installed", () => {
  const target = fakeGlobal();
  let dispose = () => {};
  captureConsoleError(() => { dispose = installGlobalErrorReporting({ target }); });
  assert.equal(target.hasListener("error"), true);
  dispose();
  assert.equal(target.hasListener("error"), false);
});

test("a target without addEventListener is tolerated", () => {
  const { result } = captureConsoleError(() => installGlobalErrorReporting({ target: {} }));
  assert.equal(typeof result, "function");
});

test("renderBoundaryErrorHTML names the surface that failed", () => {
  const html = renderBoundaryErrorHTML("settings.models", new Error("nope"));
  assert.match(html, /data-error-boundary="settings\.models"/);
  assert.match(html, /role="alert"/);
});
