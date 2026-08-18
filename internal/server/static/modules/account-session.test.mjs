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

function guestShellHarness() {
  const banner = { id: "guestObserveBanner", classList: classList(["hidden"]), textContent: "" };
  const elements = { guestObserveBanner: banner };
  const previousDocument = globalThis.document;
  globalThis.document = {
    body: { classList: classList() },
    getElementById: (id) => elements[id] || null,
  };
  return {
    banner,
    body: globalThis.document.body,
    restore: () => { globalThis.document = previousDocument; },
  };
}

test("collaborators keep the observe banner off the chat and on the profile card", () => {
  const harness = guestShellHarness();
  try {
    const collaborator = { role: "collaborator", handle: "worker" };
    const controller = createAccountSessionController({ state: { account: collaborator } });
    controller.applyGuestShell();
    assert.equal(harness.body.classList.contains("collaborator-limited"), true);
    assert.equal(harness.body.classList.contains("guest-observe"), false);
    assert.equal(harness.banner.classList.contains("hidden"), true);
    assert.equal(harness.banner.textContent, "");
    const html = controller.profileSessionHTML();
    assert.match(html, /profile-session-notice/);
    assert.match(html, /你当前是协作者/);
    assert.match(html, /已登录 worker/);
  } finally {
    harness.restore();
  }
});

test("guests still use the chat observe banner", () => {
  const harness = guestShellHarness();
  try {
    const controller = createAccountSessionController({ state: { account: { role: "guest", handle: "viewer" } } });
    controller.applyGuestShell();
    assert.equal(harness.body.classList.contains("guest-observe"), true);
    assert.equal(harness.banner.classList.contains("hidden"), false);
    assert.match(harness.banner.textContent, /你当前是访客/);
    assert.doesNotMatch(controller.profileSessionHTML(), /profile-session-notice/);
  } finally {
    harness.restore();
  }
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

function attachEvents(target) {
  const byType = new Map();
  target.addEventListener = (type, fn) => {
    const list = byType.get(type) || [];
    list.push(fn);
    byType.set(type, list);
  };
  target.dispatch = (type, event = {}) => {
    for (const fn of byType.get(type) || []) fn({ type, ...event });
  };
  return target;
}

function loginOverlayHarness() {
  const focused = [];
  const attrs = new Map([["aria-hidden", "true"]]);
  const makeInput = (id, label) => attachEvents({
    id,
    value: "",
    tagName: "INPUT",
    focus() {
      document.activeElement = this;
      focused.push(label);
      this.dispatch("focusin");
    },
  });
  const handle = makeInput("accountSessionHandle", "handle");
  const password = makeInput("accountSessionPassword", "password");
  const key = makeInput("accountSessionAccessKey", "key");
  const overlay = attachEvents({
    id: "accountSessionOverlay",
    classList: classList(["hidden"]),
    dataset: { mode: "password" },
    getAttribute: (name) => attrs.get(name),
    setAttribute: (name, value) => attrs.set(name, value),
    contains: (node) => node === handle || node === password || node === key,
  });
  const elements = {
    accountSessionOverlay: overlay,
    accountSessionHandle: handle,
    accountSessionPassword: password,
    accountSessionAccessKey: key,
    accountSessionPasswordFields: { classList: classList() },
    accountSessionKeyFields: { classList: classList(["hidden"]) },
    accountSessionUsePasswordBtn: attachEvents({ classList: classList(["active"]) }),
    accountSessionUseKeyBtn: attachEvents({ classList: classList() }),
    accountSessionForm: attachEvents({ id: "accountSessionForm" }),
    accountSessionTitle: { textContent: "" },
    accountSessionSubtitle: { textContent: "", hidden: false },
    accountSessionSubmitBtn: { textContent: "" },
    accountSessionKeyToggle: { classList: classList() },
  };
  const previousDocument = globalThis.document;
  const documentTarget = attachEvents({
    body: { classList: classList() },
    activeElement: null,
    getElementById: (id) => elements[id] || null,
  });
  globalThis.document = documentTarget;
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
    assert.equal(controller.overlayIsOpen(), true);
    const title = globalThis.document.getElementById("accountSessionTitle");
    title.textContent = "KEEP";
    harness.password.focus();
    controller.showOverlay();
    assert.equal(title.textContent, "KEEP");
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

test("the login overlay hides the empty subtitle and only shows it for setup", () => {
  const harness = loginOverlayHarness();
  try {
    const controller = createAccountSessionController({ state: {} });
    controller.showOverlay();
    const subtitle = globalThis.document.getElementById("accountSessionSubtitle");
    assert.equal(subtitle.hidden, true);
    controller.showOverlay({ createAdministrator: true });
    assert.equal(subtitle.hidden, false);
    assert.ok(String(subtitle.textContent || "").trim());
  } finally {
    harness.restore();
  }
});

test("password typing wins over a programmatic jump to the handle", () => {
  const harness = loginOverlayHarness();
  try {
    const controller = createAccountSessionController({ state: {} });
    controller.bind();
    harness.handle.value = "feng";
    controller.showOverlay();
    harness.password.value = "partial";
    harness.password.focus();
    harness.password.dispatch("keydown");
    harness.handle.focus();
    assert.equal(globalThis.document.activeElement, harness.password);
    assert.equal(harness.focused.at(-1), "password");
  } finally {
    harness.restore();
  }
});

test("clicking the handle still lets the person edit the account name", () => {
  const harness = loginOverlayHarness();
  try {
    const controller = createAccountSessionController({ state: {} });
    controller.bind();
    harness.handle.value = "feng";
    controller.showOverlay();
    harness.password.value = "partial";
    harness.password.focus();
    harness.password.dispatch("keydown");
    harness.handle.dispatch("pointerdown");
    harness.handle.focus();
    assert.equal(globalThis.document.activeElement, harness.handle);
  } finally {
    harness.restore();
  }
});
