import test from "node:test";
import assert from "node:assert/strict";

import { createGitWorkflowController, rollbackPreviewConfirmation, runCheckpointState } from "./git-workflow.mjs";
import { resetPlatformDialogs, setPlatformDialogs } from "./platform.mjs";
import { t as cr } from "./messages-chat-rendering-extra.mjs";

function classList(initial = []) {
  const values = new Set(initial);
  return {
    toggle(name, force) {
      if (force === undefined) force = !values.has(name);
      if (force) values.add(name);
      else values.delete(name);
      return force;
    },
    contains: (name) => values.has(name),
  };
}

test("Git header button distinguishes dirty state from an open modal", () => {
  const badge = { textContent: "", classList: classList(["hidden"]) };
  const button = {
    disabled: true,
    title: "",
    classList: classList(),
    attributes: new Map(),
    setAttribute(name, value) { this.attributes.set(name, value); },
    querySelector(selector) { return selector === "[data-git-tool-badge]" ? badge : null; },
  };
  const previousDocument = globalThis.document;
  globalThis.document = { getElementById: (id) => id === "gitWorkflowBtn" ? button : null };
  try {
    const state = {
      agent: { id: "agent-1" },
      gitOpen: false,
      gitError: "",
      gitStatus: { clean: false, files: [{ path: "a" }, { path: "b" }] },
    };
    const controller = createGitWorkflowController({ state });
    controller.renderGitButtonState();

    assert.equal(button.disabled, false);
    assert.equal(button.classList.contains("has-changes"), true);
    assert.equal(button.classList.contains("active"), false);
    assert.equal(badge.textContent, "2");
    assert.equal(badge.classList.contains("hidden"), false);
    assert.equal(button.attributes.get("aria-expanded"), "false");

    state.gitOpen = true;
    controller.renderGitButtonState();
    assert.equal(button.classList.contains("active"), true);
    assert.equal(button.classList.contains("has-changes"), false);
    assert.equal(button.attributes.get("aria-expanded"), "true");
  } finally {
    if (previousDocument === undefined) delete globalThis.document;
    else globalThis.document = previousDocument;
  }
});

// The git modal is rendered by assigning body.innerHTML directly (see
// renderGitModal in git-workflow.mjs), so a fake document only needs to hand
// back one capturing element for #gitModalBody; every other id used by
// bindGitModalActions() is looked up through the `$()?.addEventListener`
// pattern, which tolerates a missing element.
function renderModalHTML(controller) {
  const body = { innerHTML: "" };
  const previousDocument = globalThis.document;
  globalThis.document = {
    getElementById: (id) => (id === "gitModalBody" ? body : null),
    querySelectorAll: () => [],
  };
  try {
    controller.renderGitModal();
    return body.innerHTML;
  } finally {
    if (previousDocument === undefined) delete globalThis.document;
    else globalThis.document = previousDocument;
  }
}

// rollbackMostRecentRun() is async and calls renderGitModal() at several
// points after its internal awaits (busy-state toggles, the soft-failure
// branch, the finally block), so the fake document has to stay installed for
// the whole promise chain -- not just until the first `await` inside it,
// which is all a synchronous try/finally around an un-awaited call would
// cover.
async function withNullDocument(fn) {
  const previousDocument = globalThis.document;
  globalThis.document = { getElementById: () => null, querySelectorAll: () => [] };
  try {
    return await fn();
  } finally {
    if (previousDocument === undefined) delete globalThis.document;
    else globalThis.document = previousDocument;
  }
}

function recorder() {
  const calls = [];
  const fn = (...args) => calls.push(args);
  fn.calls = calls;
  return fn;
}

test("runCheckpointState explains every reason a run's checkpoint cannot be rolled back", () => {
  assert.deepEqual(runCheckpointState(null), { available: false, tone: "muted", reason: cr("run.checkpointDirtyWorkspace") });
  assert.equal(runCheckpointState({ checkpointState: "rolled_back" }).reason, cr("run.checkpointRolledBack"));
  assert.equal(runCheckpointState({ checkpointState: "rolling_back" }).reason, cr("run.checkpointRollingBack"));
  assert.equal(runCheckpointState({ checkpointState: "invalid" }).reason, cr("run.checkpointInvalid"));
  assert.equal(runCheckpointState({ checkpointState: "invalid", checkpointError: "custom" }).reason, "custom");
  assert.equal(runCheckpointState({ checkpointState: "ready" }).reason, cr("run.checkpointDirtyWorkspace"));
  assert.equal(
    runCheckpointState({ checkpointState: "ready", baseHead: "aaa111", endHead: "bbb222" }).reason,
    cr("run.checkpointHasCommit"),
  );
  assert.equal(runCheckpointState({ checkpointState: "none", baseHead: "aaa111" }).reason, cr("run.checkpointNoSnapshot"));
  assert.equal(
    runCheckpointState({ checkpointState: "ready", baseHead: "aaa111" }).reason,
    cr("run.checkpointNoSnapshot"),
    "ready without a recorded snapshot timestamp/repo root cannot be restored",
  );

  const available = runCheckpointState({
    checkpointState: "ready",
    baseHead: "abcdef1234567890",
    gitSnapshotAt: "2026-01-01T00:00:00Z",
    checkpointRepoRoot: "/repo",
  });
  assert.equal(available.available, true);
  assert.equal(available.tone, "ok");
  assert.equal(available.reason, cr("run.checkpointRestoreHint", { hash: "abcdef12" }));
});

function literalRegExp(text) {
  return new RegExp(String(text).replace(/[.*+?^${}()|[\]\\]/g, "\\$&"));
}

test("rollbackPreviewConfirmation composes restore/delete paths and always includes the safety line", () => {
  const minimal = rollbackPreviewConfirmation({ restoreCount: 0, deleteCount: 0 });
  assert.match(minimal, literalRegExp(cr("run.rollbackConfirm")));
  assert.match(minimal, literalRegExp(cr("run.rollbackSafety")));
  assert.doesNotMatch(minimal, /restorePaths|deletePaths|恢复路径|恢復路徑|Restore paths/);

  const full = rollbackPreviewConfirmation({
    restoreCount: 2,
    deleteCount: 1,
    restorePaths: ["src/a.js"],
    deletePaths: ["tmp/b.txt"],
    truncated: true,
  });
  assert.match(full, /src\/a\.js/);
  assert.match(full, /tmp\/b\.txt/);
  assert.match(full, literalRegExp(cr("run.rollbackTruncated")));
});

test("the git modal explains the run-checkpoint section as unavailable rather than hiding it when no run has loaded", () => {
  const state = { agent: { id: "agent-1" }, activeRunSummary: null, activeRunSummaryRunId: "" };
  const controller = createGitWorkflowController({ state });
  const html = renderModalHTML(controller);
  assert.match(html, /data-run-checkpoint-section/);
  assert.match(html, literalRegExp(cr("run.checkpointNoActiveRun")));
  assert.doesNotMatch(html, /runCheckpointRollbackBtn/);
});

test("the git modal shows the checkpoint reason and disables rollback when the run has no restorable checkpoint", () => {
  const state = {
    agent: { id: "agent-1" },
    activeRunSummary: { run: { id: "run-1", checkpointState: "none" } },
    activeRunSummaryRunId: "run-1",
  };
  const controller = createGitWorkflowController({ state });
  const html = renderModalHTML(controller);
  assert.match(html, /data-run-checkpoint-section/);
  assert.match(html, literalRegExp(cr("run.checkpointDirtyWorkspace")));
  assert.match(html, /id="runCheckpointRollbackBtn"[^>]*disabled/);
});

test("the git modal enables rollback and shows the restore hint when the run's checkpoint is available", () => {
  const state = {
    agent: { id: "agent-1" },
    activeRunSummary: {
      run: { id: "run-1", checkpointState: "ready", baseHead: "abcdef1234567890", gitSnapshotAt: "2026-01-01T00:00:00Z", checkpointRepoRoot: "/repo" },
    },
    activeRunSummaryRunId: "run-1",
    runRollbackBusy: false,
  };
  const controller = createGitWorkflowController({ state });
  const html = renderModalHTML(controller);
  assert.match(html, /abcdef12/);
  assert.doesNotMatch(html, /id="runCheckpointRollbackBtn"[^>]*disabled/);

  state.runRollbackBusy = true;
  const busyHtml = renderModalHTML(controller);
  assert.match(busyHtml, /id="runCheckpointRollbackBtn"[^>]*disabled/);
  assert.match(busyHtml, literalRegExp(cr("run.rollingBack")));
});

test("rollback warns and never calls the API when the current run has no checkpoint to restore", async () => {
  const showToast = recorder();
  const requestCalls = [];
  const state = { agent: { id: "agent-1" }, activeRunSummary: { run: { id: "run-1", checkpointState: "none" } }, activeRunSummaryRunId: "run-1" };
  const controller = createGitWorkflowController({
    state,
    showError: recorder(),
    showToast,
    apiRequest: async (url, options) => { requestCalls.push({ url, options }); return {}; },
  });
  await withNullDocument(() => controller.rollbackMostRecentRun());
  assert.equal(requestCalls.length, 0);
  assert.equal(showToast.calls.length, 1);
  assert.equal(showToast.calls[0][0], cr("run.checkpointDirtyWorkspace"));
  assert.equal(showToast.calls[0][1], "warn");
});

test("rollback performs the GET preview -> confirm -> POST sequence and updates git status on success", async () => {
  setPlatformDialogs({ confirm: async () => true });
  try {
    const showToast = recorder();
    const requestCalls = [];
    const postStatus = { branch: "main", clean: true, files: [] };
    const state = {
      agent: { id: "agent-1" },
      activeRunSummary: {
        run: { id: "run-1", checkpointState: "ready", baseHead: "abcdef1234567890", gitSnapshotAt: "2026-01-01T00:00:00Z", checkpointRepoRoot: "/repo" },
      },
      activeRunSummaryRunId: "run-1",
      gitDiff: { patch: "stale diff" },
    };
    const controller = createGitWorkflowController({
      state,
      showError: recorder(),
      showToast,
      apiRequest: async (url, options) => {
        requestCalls.push({ url, options });
        if (options?.method === "POST") return { status: postStatus, warning: "" };
        return { available: true, restoreCount: 1, deleteCount: 0, restorePaths: ["a.txt"], deletePaths: [] };
      },
    });

    await withNullDocument(() => controller.rollbackMostRecentRun());

    assert.equal(requestCalls.length, 2);
    assert.equal(requestCalls[0].url, "/api/agents/agent-1/runs/run-1/rollback");
    assert.equal(requestCalls[0].options, undefined);
    assert.equal(requestCalls[1].options.method, "POST");
    assert.deepEqual(JSON.parse(requestCalls[1].options.body), { confirm: true });
    assert.equal(state.gitStatus, postStatus);
    assert.equal(state.gitDiff, null);
    assert.equal(state.runRollbackBusy, false);
    assert.equal(state.activeRunSummary.run.checkpointState, "rolled_back");
    assert.ok(showToast.calls.some(([message, tone]) => tone === "success" && message === cr("run.rollbackComplete")));
  } finally {
    resetPlatformDialogs();
  }
});

test("rollback declines to call POST when the user cancels the confirmation prompt", async () => {
  setPlatformDialogs({ confirm: async () => false });
  try {
    const requestCalls = [];
    const state = {
      agent: { id: "agent-1" },
      activeRunSummary: {
        run: { id: "run-1", checkpointState: "ready", baseHead: "abcdef1234567890", gitSnapshotAt: "2026-01-01T00:00:00Z", checkpointRepoRoot: "/repo" },
      },
      activeRunSummaryRunId: "run-1",
      runRollbackBusy: false,
    };
    const controller = createGitWorkflowController({
      state,
      showError: recorder(),
      showToast: recorder(),
      apiRequest: async (url, options) => {
        requestCalls.push({ url, options });
        return { available: true, restoreCount: 0, deleteCount: 0 };
      },
    });

    await withNullDocument(() => controller.rollbackMostRecentRun());

    assert.equal(requestCalls.length, 1, "only the preview GET should run before a cancelled confirmation");
    assert.equal(state.runRollbackBusy, false);
  } finally {
    resetPlatformDialogs();
  }
});

test("rollback surfaces a soft preview failure via gitError and a warning toast without going busy", async () => {
  const showToast = recorder();
  const requestCalls = [];
  const state = {
    agent: { id: "agent-1" },
    activeRunSummary: {
      run: { id: "run-1", checkpointState: "ready", baseHead: "abcdef1234567890", gitSnapshotAt: "2026-01-01T00:00:00Z", checkpointRepoRoot: "/repo" },
    },
    activeRunSummaryRunId: "run-1",
    runRollbackBusy: false,
  };
  const controller = createGitWorkflowController({
    state,
    showError: recorder(),
    showToast,
    apiRequest: async (url, options) => {
      requestCalls.push({ url, options });
      return { available: false, reason: "worktree changed since the run completed" };
    },
  });

  await withNullDocument(() => controller.rollbackMostRecentRun());

  // The preview GET still runs (that's how we learn rollback isn't
  // available), it just never escalates to the confirm/POST steps.
  assert.equal(requestCalls.length, 1);
  assert.equal(requestCalls[0].options, undefined);
  assert.equal(state.gitError, "worktree changed since the run completed");
  assert.equal(state.runRollbackBusy, false);
  assert.ok(showToast.calls.some(([message, tone]) => tone === "warn" && message === "worktree changed since the run completed"));
});

test("rollback recovers from a failed POST request: the busy flag clears and the error surfaces, leaving the modal usable", async () => {
  setPlatformDialogs({ confirm: async () => true });
  try {
    const state = {
      agent: { id: "agent-1" },
      activeRunSummary: {
        run: { id: "run-1", checkpointState: "ready", baseHead: "abcdef1234567890", gitSnapshotAt: "2026-01-01T00:00:00Z", checkpointRepoRoot: "/repo" },
      },
      activeRunSummaryRunId: "run-1",
    };
    const controller = createGitWorkflowController({
      state,
      showError: recorder(),
      showToast: recorder(),
      apiRequest: async (url, options) => {
        if (options?.method === "POST") throw new Error("network unreachable");
        return { available: true, restoreCount: 0, deleteCount: 0 };
      },
    });

    await withNullDocument(async () => {
      await assert.rejects(controller.rollbackMostRecentRun(), /network unreachable/);
    });

    assert.equal(state.runRollbackBusy, false, "the busy flag must clear even after a failed POST");
    assert.equal(state.gitError, "network unreachable");
    // The modal must still be renderable afterward (a failed rollback must
    // not leave the controller in a broken state).
    assert.doesNotThrow(() => renderModalHTML(controller));
  } finally {
    resetPlatformDialogs();
  }
});
