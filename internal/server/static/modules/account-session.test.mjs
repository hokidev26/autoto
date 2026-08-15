import test from "node:test";
import assert from "node:assert/strict";

import { accountIsAdmin, accountIsGuest, defaultSettingsPanelKey, visibleSettingsItems } from "./account-session.mjs";

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

test("collaborators keep full settings except user management", () => {
  const user = { role: "user", handle: "teammate" };
  const keys = visibleSettingsItems(user, true).map((item) => item.key);
  assert.equal(keys.includes("providers"), true);
  assert.equal(keys.includes("users"), false);
  assert.equal(visibleSettingsItems(null, false).some((item) => item.key === "users"), true);
});
