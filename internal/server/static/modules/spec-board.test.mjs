import test from "node:test";
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";

import { setUILocale } from "./i18n.mjs";

import {
  createSpecBoardController,
  normalizeSpecBoard,
  renderSpecBoard,
  specBoardLimits,
} from "./spec-board.mjs";

const staticRoot = new URL("../", import.meta.url);

function classList(initial = []) {
  const values = new Set(initial);
  return {
    add: (...names) => names.forEach((name) => values.add(name)),
    remove: (...names) => names.forEach((name) => values.delete(name)),
    toggle: (name, force) => {
      if (force === undefined) force = !values.has(name);
      if (force) values.add(name);
      else values.delete(name);
      return force;
    },
    contains: (name) => values.has(name),
  };
}

test("spec board normalization bounds tasks and normalizes unsafe statuses", () => {
  const board = normalizeSpecBoard({
    agentId: "agent-1",
    tasks: Array.from({ length: specBoardLimits.tasks + 3 }, (_, index) => ({
      id: `task-${index}`,
      text: `task ${index}`,
      status: index === 0 ? "doing" : "unexpected",
      revision: 0,
    })),
  });

  assert.equal(board.tasks.length, specBoardLimits.tasks);
  assert.equal(board.tasks[0].status, "doing");
  assert.equal(board.tasks[1].status, "todo");
  assert.equal(board.tasks[0].revision, 1);
});

test("spec board rendering escapes task, agent, and confirmation content", () => {
  const malicious = '\"><img src=x onerror="boom">';
  const html = renderSpecBoard({
    loaded: true,
    rootAgent: { id: "agent-1", title: malicious },
    selectedAgentId: "agent-1",
    board: {
      agentId: "agent-1",
      tasks: [{ id: "task-1", text: malicious, status: "todo", revision: 1 }],
      goalConfirmations: [{ id: "confirm-1", taskId: "task-1", queueState: malicious, status: "accepted" }],
    },
  });

  assert.doesNotMatch(html, /<img src=x/);
  assert.match(html, /&lt;img src=x/);
  assert.match(html, /data-spec-task="task-1"/);
});

test("spec board controls follow the active UI locale", () => {
  setUILocale("en");
  try {
    const html = renderSpecBoard({
      loaded: true,
      rootAgent: { id: "agent-1", title: "Root" },
      selectedAgentId: "agent-1",
      board: { tasks: [{ id: "task-1", text: "Task", status: "todo", revision: 1 }] },
    });
    assert.match(html, />Agent<select/);
    assert.match(html, />Save<\/button>/);
    assert.match(html, />Delete<\/button>/);
    assert.match(html, />To do<\/option>/);
  } finally {
    setUILocale("zh-CN");
  }
});

test("spec board controller loads root and child navigation and accepts goal confirmations", async () => {
  const calls = [];
  const request = async (path) => {
    calls.push(path);
    if (path.endsWith("/children")) return [{ id: "child-1", title: "Child" }];
    return {
      agentId: "root-1",
      revision: 2,
      tasks: [{ id: "task-1", agentId: "root-1", text: "Ship", status: "doing", revision: 1 }],
      goalConfirmations: [],
    };
  };
  const controller = createSpecBoardController({
    request,
    document: { getElementById: () => null },
  });

  controller.setAgent({ id: "root-1", title: "Root" });
  assert.equal(await controller.load(), true);
  assert.deepEqual(calls, ["/api/agents/root-1/spec", "/api/agents/root-1/children"]);
  assert.equal(controller.getState().children[0].id, "child-1");

  assert.equal(await controller.handleGoalConfirmation({
    kind: "goal.confirmation",
    confirmation: { id: "confirm-1", agentId: "root-1", taskId: "task-2", queueState: "queued", status: "accepted" },
  }, "root-1"), true);
  assert.equal(controller.getState().latestConfirmation.id, "confirm-1");
});

test("Spec board subscriptions receive snapshots and agent switches invalidate stale loads", async () => {
  let resolveFirstLoad;
  const firstLoad = new Promise((resolve) => { resolveFirstLoad = resolve; });
  const controller = createSpecBoardController({
    document: { getElementById: () => null },
    request: (path) => {
      if (path.includes("agent-a")) return firstLoad;
      return Promise.resolve({ agentId: "agent-b", tasks: [{ id: "fresh", text: "Fresh", status: "todo", revision: 1 }] });
    },
  });
  const selectedAgents = [];
  const unsubscribe = controller.subscribe((state) => selectedAgents.push(state.selectedAgentId));

  controller.setAgent({ id: "agent-a", title: "A" });
  const staleLoad = controller.load({ includeChildren: false });
  controller.setAgent({ id: "agent-b", title: "B" });
  await controller.load({ includeChildren: false });
  resolveFirstLoad({ agentId: "agent-a", tasks: [{ id: "stale", text: "Stale", status: "done", revision: 1 }] });
  assert.equal(await staleLoad, false);
  assert.equal(controller.getState().board.tasks[0].id, "fresh");
  assert.ok(selectedAgents.includes("agent-a"));
  assert.ok(selectedAgents.includes("agent-b"));

  unsubscribe();
  const count = selectedAgents.length;
  controller.setAgent({ id: "agent-c", title: "C" });
  assert.equal(selectedAgents.length, count);

  const directSubscriber = () => selectedAgents.push("direct");
  controller.subscribe(directSubscriber, { immediate: false });
  assert.equal(controller.unsubscribe(directSubscriber), true);
  controller.setAgent({ id: "agent-d", title: "D" });
  assert.equal(selectedAgents.includes("direct"), false);
});

test("Spec header button reflects open, doing, and blocked task state", async () => {
  const badge = { textContent: "", classList: classList(["hidden"]) };
  const button = {
    disabled: true,
    title: "",
    classList: classList(),
    attributes: new Map(),
    setAttribute(name, value) { this.attributes.set(name, value); },
    querySelector(selector) { return selector === "[data-spec-tool-badge]" ? badge : null; },
    addEventListener() {},
  };
  const modal = { classList: classList(["hidden"]), addEventListener() {} };
  const document = {
    getElementById(id) {
      if (id === "specBoardBtn") return button;
      if (id === "specBoardModal") return modal;
      return null;
    },
  };
  const controller = createSpecBoardController({
    document,
    request: async () => ({
      agentId: "root-1",
      revision: 1,
      tasks: [
        { id: "doing", text: "Doing", status: "doing", revision: 1 },
        { id: "blocked", text: "Blocked", status: "blocked", revision: 1 },
      ],
    }),
  });

  controller.bind();
  controller.setAgent({ id: "root-1", title: "Root" });
  await controller.load({ includeChildren: false });
  assert.equal(button.disabled, false);
  assert.equal(button.classList.contains("has-blocked"), true);
  assert.equal(badge.textContent, "2");
  assert.equal(badge.classList.contains("hidden"), false);

  controller.open();
  assert.equal(button.classList.contains("active"), true);
  assert.equal(button.attributes.get("aria-expanded"), "true");
  controller.close();
  assert.equal(button.classList.contains("active"), false);
  assert.equal(button.attributes.get("aria-expanded"), "false");
});

test("shell mounts the Spec board and forwards accepted messages to goal confirmation", async () => {
  const [html, appMain, composer] = await Promise.all([
    readFile(new URL("index.html", staticRoot), "utf8"),
    readFile(new URL("modules/app-main.mjs", staticRoot), "utf8"),
    readFile(new URL("modules/chat-composer.mjs", staticRoot), "utf8"),
  ]);

  for (const id of ["specBoardBtn", "specBoardModal", "specBoardBody", "closeSpecBoardBtn", "goalConfirmationStack"]) {
    assert.match(html, new RegExp(`id="${id}"`));
  }
  // Matched across newlines: the call gained dock callbacks and wrapped onto several
  // lines, and the point here is that the shell mounts the board with the shared
  // request and reporting helpers, not how that call is formatted.
  assert.match(appMain, /createSpecBoardController\(\{[\s\S]*?request: api,[\s\S]*?showError,[\s\S]*?showToast,?[\s\S]*?\}\)/);
  assert.match(appMain, /onMessageAccepted: async \(result, agentId\) => \{[\s\S]*?return specBoard\.handleGoalConfirmation\(result, agentId\);\s*\},/);
  assert.match(composer, /await onMessageAccepted\?\.\(accepted, agentId\)/);
});
