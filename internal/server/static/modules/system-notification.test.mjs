import assert from "node:assert/strict";
import test from "node:test";

import { createSystemNotifications, systemNotificationPermission } from "./system-notification.mjs";

function fakeScope({ permission = "granted", visibility = "hidden", requestResult, throwOnConstruct = false } = {}) {
  const created = [];
  let focused = 0;
  class FakeNotification {
    static permission = permission;
    static requestPermission() {
      return Promise.resolve(requestResult ?? permission);
    }
    constructor(title, options) {
      if (throwOnConstruct) throw new Error("platform refused");
      this.title = title;
      this.options = options;
      this.closed = false;
      created.push(this);
    }
    close() { this.closed = true; }
  }
  return {
    scope: {
      Notification: FakeNotification,
      document: { visibilityState: visibility },
      focus() { focused += 1; },
    },
    created,
    focusCount: () => focused,
  };
}

test("permission is reported, including when unsupported", () => {
  const { scope } = fakeScope({ permission: "denied" });
  assert.equal(systemNotificationPermission(scope), "denied");
  assert.equal(systemNotificationPermission({}), "unsupported");
});

test("a hidden page shows the notification", () => {
  const { scope, created } = fakeScope({ visibility: "hidden" });
  const notifications = createSystemNotifications({ scope });
  const shown = notifications.show("Run finished", { body: "My conversation" });
  assert.ok(shown, "notification was created");
  assert.equal(created.length, 1);
  assert.equal(created[0].title, "Run finished");
  assert.equal(created[0].options.body, "My conversation");
});

test("a visible page relies on the toast instead", () => {
  const { scope, created } = fakeScope({ visibility: "visible" });
  const notifications = createSystemNotifications({ scope });
  assert.equal(notifications.show("Run finished"), null);
  assert.equal(created.length, 0);
  // force exists for an explicit test action from settings.
  assert.ok(notifications.show("Run finished", { force: true }));
  assert.equal(created.length, 1);
});

test("without granted permission nothing is shown", () => {
  const { scope, created } = fakeScope({ permission: "default" });
  const notifications = createSystemNotifications({ scope });
  assert.equal(notifications.show("Run finished"), null);
  assert.equal(created.length, 0);
});

test("a disabled preference suppresses the notification", () => {
  const { scope, created } = fakeScope();
  const notifications = createSystemNotifications({ scope, isEnabled: () => false });
  assert.equal(notifications.show("Run finished"), null);
  assert.equal(created.length, 0);
});

test("request resolves the granted permission", async () => {
  const { scope } = fakeScope({ permission: "default", requestResult: "granted" });
  const notifications = createSystemNotifications({ scope });
  assert.equal(await notifications.request(), "granted");
});

test("request short-circuits when already decided", async () => {
  const { scope } = fakeScope({ permission: "denied" });
  const notifications = createSystemNotifications({ scope });
  assert.equal(await notifications.request(), "denied");
});

test("an unsupported browser is inert rather than fatal", async () => {
  const notifications = createSystemNotifications({ scope: {} });
  assert.equal(notifications.supported(), false);
  assert.equal(await notifications.request(), "unsupported");
  assert.equal(notifications.show("Run finished"), null);
});

test("clicking a notification focuses the window and reports the agent", () => {
  const { scope, focusCount } = fakeScope();
  const activated = [];
  const notifications = createSystemNotifications({
    scope,
    onActivate: (data) => activated.push(data),
  });
  const shown = notifications.show("Run finished", { data: { agentId: "a1" } });
  shown.onclick();
  assert.equal(focusCount(), 1);
  assert.deepEqual(activated, [{ agentId: "a1" }]);
  assert.equal(shown.closed, true);
});

test("a platform that throws on construction is reported and then stays inert", () => {
  const errors = [];
  const { scope } = fakeScope({ throwOnConstruct: true });
  const notifications = createSystemNotifications({ scope, onError: (error) => errors.push(error) });
  assert.equal(notifications.show("Run finished"), null);
  assert.equal(notifications.show("Run finished"), null);
  assert.equal(errors.length, 1, "construction is not retried after failing");
});

test("an empty title is never shown", () => {
  const { scope, created } = fakeScope();
  const notifications = createSystemNotifications({ scope });
  assert.equal(notifications.show("   "), null);
  assert.equal(created.length, 0);
});

test("the body is bounded so a long error cannot bloat the notification", () => {
  const { scope, created } = fakeScope();
  const notifications = createSystemNotifications({ scope });
  notifications.show("Run failed", { body: "x".repeat(1000) });
  assert.equal(created[0].options.body.length, 300);
});
