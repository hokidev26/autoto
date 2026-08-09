import assert from "node:assert/strict";
import test from "node:test";

import { defaultSkillsPrefs, seedSkillCommandKeys, seedSkillCommandTexts } from "./preferences-data.mjs";
import { t } from "./i18n.mjs";

// The two shipped slash commands are seed data copied into localStorage, so they
// used to carry literal zh-CN text. That froze at the locale of whoever first
// opened the app: a zh-TW profile read Simplified descriptions in the slash
// palette forever, and switching language did nothing because the literal was
// already stored. These tests pin the parts that made the fix possible.
test("the seed ships keys rather than literal text", () => {
  for (const command of defaultSkillsPrefs.commands) {
    assert.equal(command.description, "", `${command.name} must not ship literal text`);
    assert.equal(command.prompt, "", `${command.name} must not ship a literal prompt`);
    assert.ok(command.descriptionKey, `${command.name} needs a description key`);
    assert.ok(command.promptKey, `${command.name} needs a prompt key`);
  }
});

test("every seed key resolves in all three catalogs", () => {
  for (const { descriptionKey, promptKey } of Object.values(seedSkillCommandKeys)) {
    for (const locale of ["zh-TW", "zh-CN", "en"]) {
      for (const key of [descriptionKey, promptKey]) {
        const value = t(key, {}, locale);
        // t() returns the key itself when an entry is missing, which would put a
        // dotted key in front of the user instead of a description.
        assert.notEqual(value, key, `${key} is missing from ${locale}`);
        assert.ok(String(value).trim().length > 0, `${key} is empty in ${locale}`);
      }
    }
  }
});

test("the descriptions differ per script, so a locale switch is visible", () => {
  const key = seedSkillCommandKeys["review-diff"].descriptionKey;
  const hant = t(key, {}, "zh-TW");
  const hans = t(key, {}, "zh-CN");
  const en = t(key, {}, "en");
  assert.notEqual(hant, hans, "zh-TW and zh-CN must not share one string");
  assert.notEqual(hant, en);
  // The zh-TW entry must actually be Traditional. 審 is the giveaway: the
  // Simplified form is 审, and the original bug was the Simplified text leaking
  // into a Traditional UI.
  assert.match(hant, /審查/);
  assert.match(hans, /审查/);
});

test("the migration list still covers every literal the seed once shipped", () => {
  // A string dropped from this list stops being recognised as an untouched seed,
  // so it freezes in place for everyone still storing it. These four are what
  // shipped before the keys existed and must never be removed.
  for (const literal of [
    "审查当前工作区改动并给出风险提示。",
    "为当前改动补充必要测试。",
  ]) {
    assert.ok(seedSkillCommandTexts.includes(literal), `${literal} must stay recognisable as a seed`);
  }
  assert.ok(seedSkillCommandTexts.length >= 4);
});
