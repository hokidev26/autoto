import test from "node:test";
import assert from "node:assert/strict";

import { setUILocale } from "./i18n.mjs";
import { createUserAdminSettingsController } from "./user-admin-settings.mjs";

test("user admin page renders guest memberships and hides them for collaborators", () => {
  setUILocale("en");
  const state = {
    account: { id: "admin-1", role: "admin", handle: "host" },
    authStatus: { hasUsers: true },
    projects: [{ id: "p1", name: "Shared" }],
    userAccounts: [
      {
        id: "guest-1",
        handle: "viewer",
        role: "guest",
        passwordSet: false,
        keyCount: 1,
        keys: [{ id: "k1", label: "phone" }],
        projectIds: ["p1"],
      },
      {
        id: "user-1",
        handle: "teammate",
        role: "user",
        passwordSet: true,
        keyCount: 0,
        keys: [],
        projectIds: ["p1"],
      },
    ],
  };
  const html = createUserAdminSettingsController({ state }).render();
  assert.match(html, /id="createGuestForm"/);
  assert.match(html, /data-user-id="guest-1"/);
  assert.match(html, /data-guest-project-id="p1"/);
  assert.match(html, /data-revoke-key="k1"/);
  assert.match(html, /data-issue-key/);
  assert.match(html, /data-save-memberships/);
  assert.match(html, /data-user-id="user-1"/);
  assert.match(html, /data-delete-user/);
  const teammateCard = html.slice(html.indexOf('data-user-id="user-1"'));
  assert.doesNotMatch(teammateCard, /data-issue-key/);
  assert.doesNotMatch(teammateCard, /data-save-memberships/);
});

test("bootstrap page offers administrator creation when no local users exist", () => {
  setUILocale("en");
  const html = createUserAdminSettingsController({
    state: { account: null, authStatus: { hasUsers: false }, projects: [], userAccounts: [] },
  }).render();
  assert.match(html, /id="createAdministratorBtn"/);
  assert.doesNotMatch(html, /id="createGuestForm"/);
});

test("non-administrators do not fetch the account directory", async () => {
  let called = false;
  const state = { account: { id: "u1", role: "user", handle: "teammate" }, userAccounts: ["stale"] };
  const controller = createUserAdminSettingsController({
    state,
    request: async () => {
      called = true;
      return [];
    },
  });
  await controller.load();
  assert.equal(called, false);
  assert.deepEqual(state.userAccounts, []);
});
