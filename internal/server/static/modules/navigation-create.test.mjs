import test from "node:test";
import assert from "node:assert/strict";

import { navigationCreateLabelKey, navigationCreateTarget } from "./navigation-create.mjs";

test("schedules mode keeps its dedicated schedule creation target", () => {
  assert.equal(navigationCreateTarget({ activeWorkbench: "schedules" }), "schedule");
});

test("every non-schedule surface creates or opens a project", () => {
  for (const activeWorkbench of ["conversation", "workbench", "", undefined, null]) {
    for (const navigationMode of ["projects", "all", "conversations", "", undefined]) {
      assert.equal(navigationCreateTarget({ activeWorkbench, navigationMode }), "project", `${activeWorkbench}/${navigationMode}`);
    }
  }
});

test("create labels expose only the project and schedule boundaries", () => {
  assert.equal(navigationCreateLabelKey("schedule"), "shell.newSchedule");
  assert.equal(navigationCreateLabelKey("project"), "shell.chooseFolder");
  assert.equal(navigationCreateLabelKey("conversation"), "shell.chooseFolder");
  assert.equal(navigationCreateLabelKey("unknown"), "shell.chooseFolder");
});
