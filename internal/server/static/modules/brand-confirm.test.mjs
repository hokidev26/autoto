import test from "node:test";
import assert from "node:assert/strict";

import { createBrandConfirm } from "./brand-confirm.mjs";

function fakeElement() {
  const classes = new Set(["hidden"]);
  const attributes = new Map();
  const listeners = new Map();
  return {
    textContent: "",
    focused: false,
    classList: {
      add: (name) => classes.add(name),
      remove: (name) => classes.delete(name),
      contains: (name) => classes.has(name),
    },
    setAttribute: (name, value) => attributes.set(name, value),
    getAttribute: (name) => attributes.get(name),
    addEventListener: (type, handler) => listeners.set(type, handler),
    fire: (type, event = {}) => listeners.get(type)?.(event),
    focus() { this.focused = true; },
  };
}

function withDialogEnvironment(run) {
  const previousDocument = globalThis.document;
  const backdrop = fakeElement();
  const message = fakeElement();
  const ok = fakeElement();
  const cancel = fakeElement();
  const documentListeners = new Map();
  const byID = {
    brandConfirmBackdrop: backdrop,
    brandConfirmMessage: message,
    brandConfirmOk: ok,
    brandConfirmCancel: cancel,
  };
  globalThis.document = {
    getElementById: (id) => byID[id] || null,
    addEventListener: (type, handler) => documentListeners.set(type, handler),
    activeElement: null,
  };
  const restore = () => {
    globalThis.document = previousDocument;
  };
  try {
    const result = run({ backdrop, message, ok, cancel, documentListeners });
    if (result && typeof result.then === "function") return result.finally(restore);
    restore();
    return result;
  } catch (error) {
    restore();
    throw error;
  }
}

test("confirm shows the dialog, fills the message, and resolves true on OK", async () => {
  await withDialogEnvironment(async ({ backdrop, message, ok, cancel }) => {
    const dialog = createBrandConfirm();
    const pending = dialog.confirm("really?");
    assert.equal(backdrop.classList.contains("hidden"), false);
    assert.equal(message.textContent, "really?");
    assert.equal(cancel.focused, true, "cancel is the safe default focus");
    ok.fire("click");
    assert.equal(await pending, true);
    assert.equal(backdrop.classList.contains("hidden"), true);
  });
});

test("cancel button and backdrop click both resolve false", async () => {
  await withDialogEnvironment(async ({ backdrop, cancel }) => {
    const dialog = createBrandConfirm();
    const first = dialog.confirm("one");
    cancel.fire("click");
    assert.equal(await first, false);

    const second = dialog.confirm("two");
    backdrop.fire("click", { target: backdrop });
    assert.equal(await second, false);
  });
});

test("a click inside the card does not dismiss the dialog", async () => {
  await withDialogEnvironment(async ({ backdrop, ok }) => {
    const dialog = createBrandConfirm();
    const pending = dialog.confirm("stay");
    backdrop.fire("click", { target: {} });
    assert.equal(backdrop.classList.contains("hidden"), false);
    ok.fire("click");
    assert.equal(await pending, true);
  });
});

test("Escape closes the dialog as a refusal", async () => {
  await withDialogEnvironment(async ({ documentListeners }) => {
    const dialog = createBrandConfirm();
    const pending = dialog.confirm("escape me");
    let prevented = false;
    documentListeners.get("keydown")({
      key: "Escape",
      preventDefault: () => { prevented = true; },
      stopPropagation: () => {},
    });
    assert.equal(await pending, false);
    assert.equal(prevented, true);
  });
});

test("missing markup falls back to the platform dialog", async () => {
  const previousDocument = globalThis.document;
  globalThis.document = { getElementById: () => null, addEventListener: () => {} };
  try {
    const calls = [];
    const dialog = createBrandConfirm({ fallback: async (message) => { calls.push(message); return true; } });
    assert.equal(await dialog.confirm("no dom"), true);
    assert.deepEqual(calls, ["no dom"]);

    const refusing = createBrandConfirm();
    assert.equal(await refusing.confirm("no dom either"), false);
  } finally {
    globalThis.document = previousDocument;
  }
});

test("a second confirm while one is open refuses the first", async () => {
  await withDialogEnvironment(async ({ message, ok }) => {
    const dialog = createBrandConfirm();
    const first = dialog.confirm("first");
    const second = dialog.confirm("second");
    assert.equal(await first, false);
    assert.equal(message.textContent, "second");
    ok.fire("click");
    assert.equal(await second, true);
  });
});
