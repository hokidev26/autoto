import assert from "node:assert/strict";
import test from "node:test";

import { createSettingsNavigationHelpers } from "./settings-navigation-helpers.mjs";

test("settings panel refresh restores the page scroll offset", () => {
  const scroller = { scrollTop: 280 };
  const body = {
    closest: (selector) => (selector === ".settings-page-scroll" ? scroller : null),
  };
  const modal = { classList: { contains: () => false } };
  const previous = globalThis.document;
  globalThis.document = {
    getElementById: (id) => {
      if (id === "settingsContentBody") return body;
      if (id === "settingsModal") return modal;
      return null;
    },
  };
  let selected = "";
  try {
    const helpers = createSettingsNavigationHelpers({
      state: { activeSettingsPanel: "providers", settingsMobileViewport: false },
      selectSettingsPanel: (key) => {
        selected = key;
        scroller.scrollTop = 0;
      },
      isMobileSettingsViewport: () => false,
      renderMobileSettingsIndex: () => {},
      renderSettingsNav: () => {},
    });
    helpers.refreshActiveSettingsPanel();
    assert.equal(selected, "providers");
    assert.equal(scroller.scrollTop, 280);
  } finally {
    globalThis.document = previous;
  }
});
