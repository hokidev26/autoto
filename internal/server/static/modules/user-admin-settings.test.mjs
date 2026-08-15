import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";

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
        id: "admin-1",
        handle: "host",
        role: "admin",
        passwordSet: true,
        keyCount: 0,
        keys: [],
        projectIds: [],
      },
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
  assert.match(html, /class="settings-live-page user-admin-page"/);
  assert.match(html, /settings-stat-grid/);
  assert.match(html, />1<\/strong><span>Administrators<\/span>/);
  assert.match(html, />1<\/strong><span>Collaborators<\/span>/);
  assert.match(html, />1<\/strong><span>Guests<\/span>/);
  assert.match(html, /id="createGuestForm"/);
  assert.match(html, /id="guestHandleInput"/);
  assert.match(html, /id="guestPasswordInput"/);
  assert.match(html, /id="guestKeyLabelInput"/);
  assert.match(html, /id="guestIssueKeyInput"/);
  assert.match(html, /data-project-picker-toggle/);
  assert.match(html, /composer-select-chevron/);
  assert.match(html, /data-guest-project-name="Shared"/);
  assert.match(html, /1 selected/);
  assert.match(html, /None selected/);
  assert.doesNotMatch(html, /user-admin-project-chip/);
  assert.match(html, /data-user-id="guest-1"/);
  assert.match(html, /data-guest-project-id="p1"/);
  assert.match(html, /data-revoke-key="k1"/);
  assert.match(html, /data-issue-key/);
  assert.match(html, /data-save-memberships/);
  assert.match(html, /data-user-id="user-1"/);
  assert.match(html, /data-delete-user/);
  const selfCard = html.slice(html.indexOf('data-user-id="admin-1"'), html.indexOf('data-user-id="guest-1"'));
  assert.match(selfCard, /user-admin-avatar/);
  assert.match(selfCard, />You</);
  assert.doesNotMatch(selfCard, /data-delete-user/);
  assert.doesNotMatch(selfCard, /data-issue-key/);
  const teammateCard = html.slice(html.indexOf('data-user-id="user-1"'));
  assert.doesNotMatch(teammateCard, /data-issue-key/);
  assert.doesNotMatch(teammateCard, /data-save-memberships/);
  assert.doesNotMatch(teammateCard, /data-guest-project-id/);
});

test("bootstrap page offers administrator creation when no local users exist", () => {
  setUILocale("en");
  const html = createUserAdminSettingsController({
    state: { account: null, authStatus: { hasUsers: false }, projects: [], userAccounts: [] },
  }).render();
  assert.match(html, /class="settings-live-page user-admin-page"/);
  assert.match(html, /id="createAdministratorBtn"/);
  assert.doesNotMatch(html, /id="createGuestForm"/);
});

test("user admin styles stack the hero card like other settings pages", () => {
  const css = readFileSync(new URL("../styles/extras.css", import.meta.url), "utf8");
  const source = readFileSync(new URL("./user-admin-settings.mjs", import.meta.url), "utf8");
  assert.match(css, /#settingsContentBody \.user-admin-page \.settings-hero-card \{[\s\S]*?flex-direction:\s*column/);
  assert.match(css, /\.user-admin-project-menu\.composer-select-popover \{[\s\S]*?z-index:\s*130/);
  assert.match(source, /composer-select-popover user-admin-project-menu/);
  assert.match(source, /composer-select-option/);
  assert.match(source, /spaceBelow >= spaceAbove/);
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
