import test from "node:test";
import assert from "node:assert/strict";

import { conversationToolsDockTarget } from "./mobile-shell-helpers.mjs";

test("conversation tools dock into the phone top bar only while the conversation is showing", () => {
  const group = { id: "conversationToolGroup" };
  const home = { id: "header-actions" };
  const dock = { id: "mobile-topbar-actions" };

  assert.equal(
    conversationToolsDockTarget({ mobile: true, conversationVisible: true, group, home, dock }),
    dock,
  );
  assert.equal(
    conversationToolsDockTarget({ mobile: true, conversationVisible: false, group, home, dock }),
    home,
  );
  assert.equal(
    conversationToolsDockTarget({ mobile: false, conversationVisible: true, group, home, dock }),
    home,
  );
  assert.equal(conversationToolsDockTarget({ mobile: true, conversationVisible: true, group, home }), null);
  assert.equal(conversationToolsDockTarget({}), null);
});
