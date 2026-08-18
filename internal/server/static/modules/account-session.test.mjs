import test from "node:test";
import assert from "node:assert/strict";

import { accountIsAdmin, accountIsCollaborator, accountIsGuest, accountIsOperator, createAccountSessionController, defaultSettingsPanelKey, visibleSettingsItems } from "./account-session.mjs";

test("guest settings are limited to profile", () => {
  const guest = { role: "guest", handle: "viewer" };
  assert.equal(accountIsGuest(guest), true);
  assert.equal(accountIsAdmin(guest), false);
  assert.equal(defaultSettingsPanelKey(guest), "profile");
  assert.deepEqual(visibleSettingsItems(guest, true).map((item) => item.key), ["profile"]);
});

test("administrators can open user management", () => {
  const admin = { role: "admin", handle: "host" };
  assert.equal(accountIsAdmin(admin), true);
  assert.equal(defaultSettingsPanelKey(admin), "providers");
  assert.equal(defaultSettingsPanelKey(admin, false), "users");
  assert.equal(visibleSettingsItems(admin, true).some((item) => item.key === "users"), true);
});

test("operators keep full settings except user management", () => {
  const user = { role: "user", handle: "teammate" };
  assert.equal(accountIsOperator(user), true);
  const keys = visibleSettingsItems(user, true).map((item) => item.key);
  assert.equal(keys.includes("providers"), true);
  assert.equal(keys.includes("users"), false);
  assert.equal(visibleSettingsItems(null, false).some((item) => item.key === "users"), true);
});

test("collaborators keep profile and appearance only", () => {
  const user = { role: "collaborator", handle: "worker" };
  assert.equal(accountIsCollaborator(user), true);
  assert.equal(defaultSettingsPanelKey(user), "profile");
  assert.deepEqual(visibleSettingsItems(user, true).map((item) => item.key), ["profile", "appearance"]);
});

function classList(initial = []) {
  const values = new Set(initial);
  return {
    contains: (name) => values.has(name),
    add: (name) => values.add(name),
    remove: (name) => values.delete(name),
    toggle(name, force) {
      const enable = force === undefined ? !values.has(name) : Boolean(force);
      if (enable) values.add(name);
      else values.delete(name);
      return enable;
    },
  };
}

function loginOverlayHarness() {
  const focused = [];
  const attrs = new Map([["aria-hidden", "true"]]);
  const handle = { id: "accountSessionHandle", value: "", tagName: "INPUT", focus() { document.activeElement = handle; focused.push("handle"); } };
  const password = { id: "accountSessionPassword", value: "", tagName: "INPUT", focus() { document.activeElement = password; focused.push("password"); } };
  const key = { id: "accountSessionAccessKey", value: "", tagName: "INPUT", focus() { document.activeElement = key; focused.push("key"); } };
  const overlay = {
    id: "accountSessionOverlay",
    classList: classList(["hidden"]),
    dataset: { mode: "password" },
    getAttribute: (name) => attrs.get(name),
    setAttribute: (name, value) => attrs.set(name, value),
    contains: (node) => node === handle || node === password || node === key,
  };
  const elements = {
    accountSessionOverlay: overlay,
    accountSessionHandle: handle,
    accountSessionPassword: password,
    accountSessionAccessKey: key,
    accountSessionPasswordFields: { classList: classList() },
    accountSessionKeyFields: { classList: classList(["hidden"]) },
    accountSessionUsePasswordBtn: { classList: classList(["active"]) },
    accountSessionUseKeyBtn: { classList: classList() },
    accountSessionTitle: { textContent: "" },
    accountSessionSubtitle: { textContent: "" },
    accountSessionSubmitBtn: { textContent: "" },
    accountSessionKeyToggle: { classList: classList() },
  };
  const previousDocument = globalThis.document;
  globalThis.document = {
    body: { classList: classList() },
    activeElement: null,
    getElementById: (id) => elements[id] || null,
  };
  return {
    focused,
    handle,
    password,
    overlay,
    restore: () => { globalThis.document = previousDocument; },
  };
}

test("opening the login overlay focuses the empty handle once", () => {
  const harness = loginOverlayHarness();
  try {
    const controller = createAccountSessionController({ state: {} });
    controller.showOverlay();
    assert.deepEqual(harness.focused, ["handle"]);
    harness.password.focus();
    controller.showOverlay();
    assert.deepEqual(harness.focused, ["handle", "password"]);
    assert.equal(globalThis.document.activeElement, harness.password);
  } finally {
    harness.restore();
  }
});

test("a filled handle opens the overlay on the password field", () => {
  const harness = loginOverlayHarness();
  try {
    harness.handle.value = "ray";
    const controller = createAccountSessionController({ state: {} });
    controller.showOverlay();
    assert.deepEqual(harness.focused, ["password"]);
  } finally {
    harness.restore();
  }
});
