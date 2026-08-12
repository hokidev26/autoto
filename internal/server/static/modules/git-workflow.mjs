import { $, escapeAttr, escapeHtml, setTextIfChanged } from "./dom.mjs";
import { formatNumber, formatTimestamp } from "./formatters.mjs";
import { confirm as platformConfirm } from "./platform.mjs";
import { api } from "./runtime.mjs";
import { gitExtraT as t } from "./messages-git-extra.mjs";
// The run-checkpoint/rollback copy already lives in the run.* namespace of
// this pack (it used to back the chat-side run review card); reuse it here
// instead of forking a parallel set of git-extra keys.
import { t as cr } from "./messages-chat-rendering-extra.mjs?v=git-modal-rollback-1";

/**
 * Whether the most recently loaded run recorded a Git checkpoint that can
 * still be rolled back, and if not, why. Moved here from chat-rendering.mjs
 * along with the rollback flow itself: once the project run review card was
 * removed from the chat, this state was only ever needed to drive the git
 * modal's own checkpoint section.
 */
export function runCheckpointState(run) {
  const checkpointState = String(run?.checkpointState || "").trim();
  if (checkpointState === "rolled_back") {
    return { available: false, tone: "muted", reason: cr("run.checkpointRolledBack") };
  }
  if (checkpointState === "rolling_back") {
    return { available: false, tone: "warn", reason: cr("run.checkpointRollingBack") };
  }
  if (checkpointState === "invalid") {
    return { available: false, tone: "warn", reason: run?.checkpointError || cr("run.checkpointInvalid") };
  }
  if (checkpointState === "capturing") {
    return { available: false, tone: "warn", reason: cr("run.checkpointCapturing") };
  }
  if (checkpointState === "tracking") {
    return { available: false, tone: "muted", reason: cr("run.checkpointTracking") };
  }
  if (!run?.baseHead) {
    return { available: false, tone: "muted", reason: cr("run.checkpointDirtyWorkspace") };
  }
  if (run.endHead && run.endHead !== run.baseHead) {
    return { available: false, tone: "warn", reason: cr("run.checkpointHasCommit") };
  }
  if (checkpointState === "none") {
    return { available: false, tone: "muted", reason: cr("run.checkpointNoSnapshot") };
  }
  if (checkpointState !== "ready") {
    return { available: false, tone: "warn", reason: cr("run.checkpointUnknown") };
  }
  if (!run.gitSnapshotAt || !run.checkpointRepoRoot) {
    return { available: false, tone: "muted", reason: cr("run.checkpointNoSnapshot") };
  }
  return { available: true, tone: "ok", reason: cr("run.checkpointRestoreHint", { hash: shortGitHash(run.baseHead) }) };
}

function shortGitHash(hash) {
  const text = String(hash || "").trim();
  return text ? text.slice(0, 8) : "";
}

/** Confirmation prompt text for rolling back to the pre-run checkpoint. */
export function rollbackPreviewConfirmation(preview) {
  const restorePaths = Array.isArray(preview?.restorePaths) ? preview.restorePaths : [];
  const deletePaths = Array.isArray(preview?.deletePaths) ? preview.deletePaths : [];
  const lines = [
    cr("run.rollbackConfirm"),
    "",
    cr("run.rollbackSummary", { restoreCount: Number(preview?.restoreCount || 0), deleteCount: Number(preview?.deleteCount || 0) }),
  ];
  if (restorePaths.length) lines.push("", cr("run.restorePaths"), ...restorePaths.map((path) => `- ${path}`));
  if (deletePaths.length) lines.push("", cr("run.deletePaths"), ...deletePaths.map((path) => `- ${path}`));
  if (preview?.truncated) lines.push("", cr("run.rollbackTruncated"));
  lines.push("", cr("run.rollbackSafety"));
  return lines.join("\n");
}

export const GIT_DIFF_CACHE_MAX_ENTRIES = 4;
export const GIT_DIFF_DIRECT_RENDER_LINE_LIMIT = 600;
export const GIT_DIFF_VIRTUAL_OVERSCAN = 24;
export const GIT_DIFF_VIRTUAL_LINE_HEIGHT = 19;
export const GIT_DIFF_VIRTUAL_DEFAULT_VIEWPORT_HEIGHT = GIT_DIFF_VIRTUAL_LINE_HEIGHT * 96;

function normalizePatchIdentity(identity) {
  if (identity && typeof identity === "object") return JSON.stringify(identity);
  return String(identity || "");
}

function unsignedHash(text) {
  let hash = 2166136261;
  for (let index = 0; index < text.length; index++) {
    hash ^= text.charCodeAt(index);
    hash = Math.imul(hash, 16777619);
  }
  return (hash >>> 0).toString(36);
}

export function gitDiffPatchCacheKey(patch, identity = "") {
  const text = String(patch || "");
  const identityText = normalizePatchIdentity(identity);
  return `${text.length}:${unsignedHash(text)}:${unsignedHash(identityText)}`;
}

export function diffLineClass(line) {
  const text = String(line || "");
  if (text.startsWith("@@")) return "hunk";
  if (text.startsWith("diff --git") || text.startsWith("index ") || text.startsWith("---") || text.startsWith("+++")) return "meta";
  if (text.startsWith("+")) return "add";
  if (text.startsWith("-")) return "del";
  return "context";
}

export function splitAndClassifyUnifiedDiff(patch) {
  const text = String(patch || "");
  if (!text) return [];
  return text.split("\n").map((line) => ({ text: line || " ", className: diffLineClass(line) }));
}

export function createGitDiffLineCache({ maxEntries = GIT_DIFF_CACHE_MAX_ENTRIES } = {}) {
  const entries = new Map();
  const limit = Math.max(1, Number(maxEntries) || GIT_DIFF_CACHE_MAX_ENTRIES);
  return {
    get(patch, identity = "") {
      const text = String(patch || "");
      const identityText = normalizePatchIdentity(identity);
      const key = gitDiffPatchCacheKey(text, identityText);
      const existing = entries.get(key);
      if (existing?.patch === text && existing.identity === identityText) {
        entries.delete(key);
        entries.set(key, existing);
        return { key, value: existing.value, hit: true };
      }
      const lines = splitAndClassifyUnifiedDiff(text);
      const value = Object.freeze({ key, lineCount: lines.length, lines });
      entries.set(key, { patch: text, identity: identityText, value });
      while (entries.size > limit) entries.delete(entries.keys().next().value);
      return { key, value, hit: false };
    },
    peek(key) {
      return entries.get(key)?.value || null;
    },
    size() {
      return entries.size;
    },
  };
}

export function shouldVirtualizeGitDiff(lineCount, threshold = GIT_DIFF_DIRECT_RENDER_LINE_LIMIT) {
  return Number(lineCount || 0) > Number(threshold || GIT_DIFF_DIRECT_RENDER_LINE_LIMIT);
}

function clampNumber(value, min, max) {
  return Math.max(min, Math.min(max, value));
}

export function computeGitDiffWindow({
  lineCount,
  scrollTop = 0,
  viewportHeight = GIT_DIFF_VIRTUAL_DEFAULT_VIEWPORT_HEIGHT,
  lineHeight = GIT_DIFF_VIRTUAL_LINE_HEIGHT,
  overscan = GIT_DIFF_VIRTUAL_OVERSCAN,
} = {}) {
  const total = Math.max(0, Number(lineCount) || 0);
  if (!total) return { start: 0, end: 0, topSpacer: 0, bottomSpacer: 0 };
  const rowHeight = Math.max(1, Number(lineHeight) || GIT_DIFF_VIRTUAL_LINE_HEIGHT);
  const visibleHeight = Math.max(rowHeight, Number(viewportHeight) || GIT_DIFF_VIRTUAL_DEFAULT_VIEWPORT_HEIGHT);
  const extra = Math.max(0, Number(overscan) || 0);
  const firstVisible = clampNumber(Math.floor(Math.max(0, Number(scrollTop) || 0) / rowHeight), 0, total - 1);
  const visibleCount = Math.max(1, Math.ceil(visibleHeight / rowHeight));
  const start = clampNumber(firstVisible - extra, 0, total);
  const end = clampNumber(firstVisible + visibleCount + extra, start, total);
  return {
    start,
    end,
    topSpacer: start * rowHeight,
    bottomSpacer: Math.max(0, (total - end) * rowHeight),
  };
}

export function renderUnifiedDiffLines(lines) {
  return lines.map((line) => `<div class="diff-line ${line.className}">${escapeHtml(line.text)}</div>`).join("");
}

export function renderUnifiedDiffWindow(lines, window) {
  const topSpacer = window.topSpacer ? `<div class="diff-virtual-spacer" aria-hidden="true" style="height:${window.topSpacer}px"></div>` : "";
  const bottomSpacer = window.bottomSpacer ? `<div class="diff-virtual-spacer" aria-hidden="true" style="height:${window.bottomSpacer}px"></div>` : "";
  return `${topSpacer}${renderUnifiedDiffLines(lines.slice(window.start, window.end))}${bottomSpacer}`;
}

export function renderUnifiedDiff(patch, {
  cache = createGitDiffLineCache(),
  identity = "",
  scrollTop = 0,
  viewportHeight = GIT_DIFF_VIRTUAL_DEFAULT_VIEWPORT_HEIGHT,
  threshold = GIT_DIFF_DIRECT_RENDER_LINE_LIMIT,
} = {}) {
  if (!patch) return `<pre class="git-diff-view empty">${escapeHtml(t("noDiff"))}</pre>`;
  const { key, value } = cache.get(patch, identity);
  const virtual = shouldVirtualizeGitDiff(value.lineCount, threshold);
  if (!virtual) {
    return `<pre id="gitDiffView" class="git-diff-view" tabindex="0" data-git-diff-key="${escapeAttr(key)}">${renderUnifiedDiffLines(value.lines)}</pre>`;
  }
  const window = computeGitDiffWindow({ lineCount: value.lineCount, scrollTop, viewportHeight });
  return `<pre id="gitDiffView" class="git-diff-view" tabindex="0" data-git-diff-key="${escapeAttr(key)}" data-git-diff-virtual="true" data-git-diff-line-count="${value.lineCount}" data-git-diff-window-start="${window.start}" data-git-diff-window-end="${window.end}">${renderUnifiedDiffWindow(value.lines, window)}</pre>`;
}

export function createGitWorkflowController({
  state,
  showError,
  showToast,
  apiRequest = api,
} = {}) {
  const request = apiRequest;
  const gitDiffLineCache = createGitDiffLineCache();
  let gitDiffScrollTop = 0;
  let gitDiffScrollKey = "";

  function resetGitWorkflowState() {
    state.gitStatus = null;
    state.gitDiff = null;
    state.gitLog = null;
    state.gitError = "";
    state.gitSelectedPath = "";
    state.gitCommitMessage = "";
    state.gitCommitSelected = {};
    state.gitCommitBusy = false;
    state.gitDiscardBusy = false;
    state.gitOpen = false;
    state.mergeCheck = null;
    state.mergeCheckBusy = false;
    state.mergeCheckError = "";
    state.mergeBusy = false;
    state.worklineUnmergeBusy = false;
    state.gitSeq++;
    renderGitButtonState();
  }

  // Integration is a decision, so nothing here runs on open. The review panel
  // starts empty and only reports what a trial merge actually found once the
  // user asks for it; the merge button stays disabled until then.
  async function loadWorklineMergeCheck() {
    const workline = state.workline;
    const sourceId = String(workline?.id || "");
    if (!sourceId || state.mergeCheckBusy) return null;
    if (workline?.isRoot) {
      state.mergeCheck = null;
      state.mergeCheckError = t("mergeRootOnly");
      renderGitModal();
      return null;
    }
    state.mergeCheckBusy = true;
    state.mergeCheckError = "";
    renderGitModal();
    try {
      const result = await request(`/api/worklines/${encodeURIComponent(sourceId)}/merge-check`);
      if (state.workline?.id !== sourceId) return null;
      state.mergeCheck = result;
      return result;
    } catch (error) {
      if (state.workline?.id !== sourceId) return null;
      state.mergeCheck = null;
      state.mergeCheckError = error?.message || String(error);
      return null;
    } finally {
      if (state.workline?.id === sourceId) {
        state.mergeCheckBusy = false;
        renderGitModal();
      }
    }
  }

  // Only reachable from a check that already said canMerge, and still confirmed:
  // the merge writes to the target branch, which is not the worktree the user is
  // looking at.
  async function mergeCurrentWorkline() {
    const check = state.mergeCheck;
    const sourceId = String(state.workline?.id || "");
    if (!sourceId || !check?.canMerge || state.mergeBusy) return null;
    const confirmed = await platformConfirm(t("mergeConfirm", {
      source: check.sourceBranch || sourceId,
      target: check.targetBranch || check.targetWorklineId || "",
      count: formatNumber(check.changedCount || 0),
    }));
    if (!confirmed) return null;
    state.mergeBusy = true;
    renderGitModal();
    try {
      const result = await request(`/api/worklines/${encodeURIComponent(sourceId)}/merge`, {
        method: "POST",
        body: JSON.stringify({ targetWorklineId: check.targetWorklineId || "" }),
      });
      // The check is now stale whatever the outcome, so it is cleared rather
      // than left showing a verdict that no longer describes the repository.
      state.mergeCheck = null;
      if (result?.workline && state.workline?.id === sourceId) state.workline = result.workline;
      showToast(t("mergeDone", { commit: shortGitHash(result?.mergeCommit || "") }), "success", { force: true });
      await offerWorklineCleanup(sourceId, check.sourceBranch || "");
      await refreshGitWorkflow({ silent: true });
      return result;
    } catch (error) {
      state.mergeCheck = null;
      state.mergeCheckError = error?.message || String(error);
      showToast(state.mergeCheckError, "error", { force: true });
      return null;
    } finally {
      state.mergeBusy = false;
      renderGitModal();
    }
  }

  // Undo of a completed merge. The backend picks the strategy: a hard reset to
  // the pre-merge head when the merge commit is still the target's tip, or a
  // revert commit when the target has moved on. Only offered while the source
  // branch still exists, so the workline can genuinely return to active work.
  async function unmergeCurrentWorkline() {
    const workline = state.workline;
    const sourceId = String(workline?.id || "");
    if (!sourceId || workline?.status !== "merged" || state.worklineUnmergeBusy) return null;
    const confirmed = await platformConfirm(t("unmergeConfirm", {
      branch: workline.branch || workline.title || sourceId,
      commit: shortGitHash(workline.mergeCommitSha || ""),
    }));
    if (!confirmed) return null;
    state.worklineUnmergeBusy = true;
    renderGitModal();
    try {
      const result = await request(`/api/worklines/${encodeURIComponent(sourceId)}/unmerge`, {
        method: "POST",
        body: JSON.stringify({ confirm: true }),
      });
      if (result?.workline && state.workline?.id === sourceId) state.workline = result.workline;
      state.mergeCheck = null;
      if (result?.strategy === "reset") showToast(t("unmergeDoneReset"), "success", { force: true });
      else showToast(t("unmergeDoneRevert", { commit: shortGitHash(result?.newTargetHead || "") }), "success", { force: true });
      await refreshGitWorkflow({ silent: true });
      return result;
    } catch (error) {
      showToast(t("unmergeFailed", { message: error?.message || String(error) }), "error", { force: true });
      return null;
    } finally {
      state.worklineUnmergeBusy = false;
      renderGitModal();
    }
  }

  // Merging only flips the database status; the source branch and its worktree
  // directory would otherwise accumulate forever. Cleanup stays a separate,
  // explicitly confirmed step because it retires the branch for good.
  async function offerWorklineCleanup(worklineId, branch) {
    if (!worklineId) return;
    const confirmed = await platformConfirm(t("cleanupConfirm", { branch: branch || worklineId }));
    if (!confirmed) return;
    try {
      const result = await request(`/api/worklines/${encodeURIComponent(worklineId)}/cleanup`, {
        method: "POST",
        body: JSON.stringify({ confirm: true }),
      });
      if (result?.workline && state.workline?.id === worklineId) state.workline = result.workline;
      const warnings = Array.isArray(result?.warnings) ? result.warnings.filter(Boolean) : [];
      if (warnings.length) showToast(t("cleanupWarnings", { message: warnings.join("; ") }), "warn", { force: true });
      else showToast(t("cleanupDone"), "success", { force: true });
    } catch (error) {
      showToast(t("cleanupFailed", { message: error?.message || String(error) }), "error", { force: true });
    }
  }

  function renderGitButtonState() {
    const button = $("gitWorkflowBtn");
    if (!button) return;
    const enabled = Boolean(state.agent?.id);
    const dirty = Boolean(state.gitStatus && state.gitStatus.clean === false);
    const count = Array.isArray(state.gitStatus?.files) ? state.gitStatus.files.length : 0;
    const hasError = Boolean(state.gitError);
    button.disabled = !enabled;
    button.classList.toggle("active", Boolean(state.gitOpen));
    button.classList.toggle("has-changes", dirty && !state.gitOpen);
    button.classList.toggle("tool-error", hasError);
    button.setAttribute("aria-expanded", state.gitOpen ? "true" : "false");
    button.title = !enabled ? t("selectAgent") : hasError ? t("statusUnavailable", { message: state.gitError }) : dirty ? t("changesCount", { count }) : t("clean");
    const badge = button.querySelector("[data-git-tool-badge]");
    if (badge) {
      setTextIfChanged(badge, count > 99 ? "99+" : String(count));
      badge.classList.toggle("hidden", !dirty || count === 0);
    }
  }

  async function loadGitStatus(options = {}) {
    const agentId = state.agent?.id;
    renderGitButtonState();
    if (!agentId) return null;
    const seq = ++state.gitSeq;
    try {
      const status = await api(`/api/agents/${agentId}/git/status`);
      if (seq !== state.gitSeq || state.agent?.id !== agentId) return null;
      state.gitStatus = status;
      state.gitError = "";
      const files = Array.isArray(status.files) ? status.files : [];
      state.gitCommitSelected = pruneGitCommitSelection(files);
      if (!files.some((file) => file.path === state.gitSelectedPath)) {
        state.gitSelectedPath = files[0]?.path || "";
      }
      renderGitButtonState();
      if (state.gitOpen) renderGitModal();
      return status;
    } catch (err) {
      if (seq !== state.gitSeq || state.agent?.id !== agentId) return null;
      state.gitStatus = null;
      state.gitError = err.message || String(err);
      renderGitButtonState();
      if (!options.silent) showError(err);
      if (state.gitOpen) renderGitModal();
      return null;
    }
  }

  async function loadGitDiff(options = {}) {
    const agentId = state.agent?.id;
    if (!agentId) return null;
    const scope = options.scope || state.gitScope || "all";
    const path = options.path !== undefined ? options.path : state.gitSelectedPath || "";
    const params = new URLSearchParams({ scope });
    if (path) params.set("path", path);
    const diff = await api(`/api/agents/${agentId}/git/diff?${params.toString()}`);
    if (state.agent?.id !== agentId) return null;
    state.gitDiff = diff;
    state.gitError = "";
    if (state.gitOpen) renderGitModal();
    return diff;
  }

  async function loadGitLog() {
    const agentId = state.agent?.id;
    if (!agentId) return null;
    const log = await api(`/api/agents/${agentId}/git/log?limit=30`);
    if (state.agent?.id !== agentId) return null;
    state.gitLog = log;
    state.gitError = "";
    if (state.gitOpen) renderGitModal();
    return log;
  }

  async function refreshGitWorkflow(options = {}) {
    const status = await loadGitStatus({ silent: options.silent });
    if (!status) return;
    const files = Array.isArray(status.files) ? status.files : [];
    if (!state.gitSelectedPath && files.length) state.gitSelectedPath = files[0].path || "";
    await Promise.allSettled([loadGitDiff(), loadGitLog()]);
    if (options.notify) showToast(t("refreshed"), "success");
  }

  /**
   * Roll back the workspace to the Git checkpoint recorded before the most
   * recent run (state.activeRunSummary?.run / state.activeRunSummaryRunId,
   * populated by chat-rendering.mjs's loadRunSummary). Mirrors the
   * GET-preview -> confirm -> POST flow the chat run-review card used to run,
   * now driven from the git modal since that card no longer exists.
   */
  async function rollbackMostRecentRun() {
    const agentId = state.agent?.id;
    const run = state.activeRunSummary?.run;
    const runId = state.activeRunSummaryRunId || run?.id || "";
    const checkpoint = runCheckpointState(run);
    if (!agentId || !runId || !checkpoint.available) {
      showToast(checkpoint.reason || cr("run.noCheckpoint"), "warn", { force: true });
      return;
    }
    const preview = await request(`/api/agents/${agentId}/runs/${encodeURIComponent(runId)}/rollback`);
    if (state.agent?.id !== agentId) return;
    if (!preview?.available) {
      const reason = preview?.reason || cr("run.noCheckpoint");
      state.gitError = reason;
      renderGitModal();
      showToast(reason, "warn", { force: true });
      return;
    }
    const confirmed = await platformConfirm(rollbackPreviewConfirmation(preview));
    if (!confirmed) return;
    state.runRollbackBusy = true;
    state.gitError = "";
    renderGitModal();
    try {
      const result = await request(`/api/agents/${agentId}/runs/${encodeURIComponent(runId)}/rollback`, {
        method: "POST",
        body: JSON.stringify({ confirm: true }),
      });
      if (state.agent?.id !== agentId) return;
      if (result?.status) {
        state.gitStatus = result.status;
        state.gitDiff = null;
      }
      // There is no chat-side run card left to refetch for a fresh
      // checkpointState; a successful rollback deterministically consumes
      // the checkpoint, so flip it locally instead of re-requesting the run.
      if (state.activeRunSummary?.run) {
        state.activeRunSummary = { ...state.activeRunSummary, run: { ...state.activeRunSummary.run, checkpointState: "rolled_back" } };
      }
      const rollbackWarning = String(result?.warning || "").trim();
      showToast(rollbackWarning ? cr("run.rollbackRefreshFailed") : cr("run.rollbackComplete"), rollbackWarning ? "warn" : "success", { force: true });
      await refreshGitWorkflow({ silent: true });
    } catch (err) {
      if (state.agent?.id !== agentId) return;
      state.gitError = err.message || String(err);
      throw err;
    } finally {
      if (state.agent?.id === agentId) {
        state.runRollbackBusy = false;
        renderGitModal();
      }
    }
  }

  function pruneGitCommitSelection(files) {
    const next = Object.create(null);
    const selected = state.gitCommitSelected || {};
    (files || []).forEach((file) => {
      const path = file.path || "";
      if (path && selected[path]) next[path] = true;
    });
    return next;
  }

  function selectedGitCommitPaths(files = state.gitStatus?.files || []) {
    const selected = state.gitCommitSelected || {};
    return (Array.isArray(files) ? files : [])
      .map((file) => file.path || "")
      .filter((path) => path && selected[path]);
  }

  function setGitCommitSelection(paths) {
    const next = Object.create(null);
    (paths || []).forEach((path) => {
      if (path) next[path] = true;
    });
    state.gitCommitSelected = next;
    renderGitModal();
  }

  function updateGitCommitControls() {
    const input = $("gitCommitMessageInput");
    if (input) state.gitCommitMessage = input.value;
    const selectedCount = selectedGitCommitPaths().length;
    const count = $("gitCommitSelectedCount");
    if (count) count.textContent = t("selectedCount", { count: formatNumber(selectedCount) });
    const button = $("gitCommitBtn");
    if (button) button.disabled = state.gitCommitBusy || !selectedCount || !String(state.gitCommitMessage || "").trim();
  }

  async function commitGitSelection(event) {
    event?.preventDefault();
    if (state.gitCommitBusy) return;
    const agentId = state.agent?.id;
    if (!agentId) return;
    const input = $("gitCommitMessageInput");
    if (input) state.gitCommitMessage = input.value;
    const message = String(state.gitCommitMessage || "").trim();
    const paths = selectedGitCommitPaths();
    if (!message) {
      showToast(t("commitMessageRequired"), "warn", { force: true });
      return;
    }
    if (!paths.length) {
      showToast(t("filesRequired"), "warn", { force: true });
      return;
    }
    state.gitCommitBusy = true;
    state.gitError = "";
    renderGitModal();
    try {
      const result = await api(`/api/agents/${agentId}/git/commit`, {
        method: "POST",
        body: JSON.stringify({ message, paths }),
      });
      if (state.agent?.id !== agentId) return;
      state.gitCommitMessage = "";
      state.gitCommitSelected = {};
      const shortHash = result?.commit?.shortHash ? ` ${result.commit.shortHash}` : "";
      showToast(t("commitCreated", { hash: shortHash }), "success", { force: true });
      await refreshGitWorkflow({ silent: true });
    } catch (err) {
      if (state.agent?.id !== agentId) return;
      state.gitError = err.message || String(err);
      showError(err);
    } finally {
      if (state.agent?.id === agentId) {
        state.gitCommitBusy = false;
        renderGitModal();
      }
    }
  }

  /**
   * The general-purpose escape hatch beside the run checkpoint: when no
   * checkpoint is available (dirty start, later commits, hand edits), the
   * checked selection can still be reverted to HEAD. Untracked files in the
   * selection are deleted, which is why this is a separate confirmed action
   * rather than a side effect of anything else.
   */
  async function discardGitSelection() {
    if (state.gitDiscardBusy) return;
    const agentId = state.agent?.id;
    if (!agentId) return;
    const paths = selectedGitCommitPaths();
    if (!paths.length) {
      showToast(t("discardSelectionRequired"), "warn", { force: true });
      return;
    }
    const shownPaths = paths.slice(0, 20).map((path) => `- ${path}`);
    if (paths.length > 20) shownPaths.push(`… +${paths.length - 20}`);
    const confirmed = await platformConfirm([
      t("discardConfirm", { count: formatNumber(paths.length) }),
      "",
      t("discardPathsHeader"),
      ...shownPaths,
    ].join("\n"));
    if (!confirmed) return;
    state.gitDiscardBusy = true;
    state.gitError = "";
    renderGitModal();
    try {
      const result = await request(`/api/agents/${agentId}/git/discard`, {
        method: "POST",
        body: JSON.stringify({ paths, confirm: true }),
      });
      if (state.agent?.id !== agentId) return;
      state.gitCommitSelected = {};
      if (result?.status) {
        state.gitStatus = result.status;
        state.gitDiff = null;
      }
      const warning = String(result?.warning || "").trim();
      const summary = t("discardDone", {
        restored: formatNumber((result?.restoredPaths || []).length),
        deleted: formatNumber((result?.deletedPaths || []).length),
      });
      showToast(warning ? `${summary} ${warning}` : summary, warning ? "warn" : "success", { force: true });
      await refreshGitWorkflow({ silent: true });
    } catch (err) {
      if (state.agent?.id !== agentId) return;
      state.gitError = err.message || String(err);
      showError(err);
    } finally {
      if (state.agent?.id === agentId) {
        state.gitDiscardBusy = false;
        renderGitModal();
      }
    }
  }

  function openGitModal() {
    if (!state.agent?.id) return;
    state.gitOpen = true;
    $("gitModal")?.classList.remove("hidden");
    renderGitButtonState();
    renderGitModal();
    refreshGitWorkflow({ silent: true }).catch((err) => {
      state.gitError = err.message || String(err);
      renderGitModal();
    });
  }

  function closeGitModal() {
    state.gitOpen = false;
    $("gitModal")?.classList.add("hidden");
    renderGitButtonState();
  }

  function renderGitModal() {
    const body = $("gitModalBody");
    if (!body) return;
    const previousDiffView = $("gitDiffView");
    if (previousDiffView?.dataset?.gitDiffKey) {
      gitDiffScrollKey = previousDiffView.dataset.gitDiffKey;
      gitDiffScrollTop = Number(previousDiffView.scrollTop || 0);
    }
    const status = state.gitStatus;
    const files = Array.isArray(status?.files) ? status.files : [];
    const diff = state.gitDiff;
    const log = state.gitLog;
    const selectedPath = state.gitSelectedPath || "";
    const patch = diff?.patch || "";
    const diffIdentity = gitDiffRenderIdentity(status, selectedPath);
    const diffKey = patch ? gitDiffPatchCacheKey(patch, diffIdentity) : "";
    const diffScrollTop = diffKey && diffKey === gitDiffScrollKey ? gitDiffScrollTop : 0;
    const selectedCommitPaths = selectedGitCommitPaths(files);
    if ($("gitModalPath")) {
      $("gitModalPath").textContent = status?.repoRoot || state.agent?.cwd || state.project?.gitPath || t("pathHint");
    }
    body.innerHTML = `
      <div class="git-toolbar">
        <div class="git-summary">
          <strong>${escapeHtml(status?.branch || status?.head || t("fallbackBranch"))}</strong>
          <span>${escapeHtml(status?.clean === false ? t("filesChanged", { count: formatNumber(files.length) }) : (status ? t("workspaceClean") : t("notLoaded")))}</span>
          ${status?.upstream ? `<span>${escapeHtml(status.upstream)}${status.ahead || status.behind ? ` ${escapeHtml(t("aheadBehind", { ahead: formatNumber(status.ahead || 0), behind: formatNumber(status.behind || 0) }))}` : ""}</span>` : ""}
        </div>
        <div class="git-actions">
          <select id="gitScopeSelect" class="select git-scope-select" aria-label="${escapeAttr(t("diffScope"))}">
            <option value="all" ${state.gitScope === "all" ? "selected" : ""}>${escapeHtml(t("all"))}</option>
            <option value="unstaged" ${state.gitScope === "unstaged" ? "selected" : ""}>${escapeHtml(t("unstaged"))}</option>
            <option value="staged" ${state.gitScope === "staged" ? "selected" : ""}>${escapeHtml(t("staged"))}</option>
          </select>
          <button id="refreshGitBtn" class="ghost-btn mini" type="button">${escapeHtml(t("refresh"))}</button>
        </div>
      </div>
      ${state.gitError ? `<div class="settings-inline-alert">${escapeHtml(state.gitError)}</div>` : ""}
      <div class="git-layout">
        <aside class="git-file-list">
          <div class="git-panel-title">${escapeHtml(t("changedFiles"))}</div>
          ${renderGitFileList(files, selectedPath)}
        </aside>
        <section class="git-diff-panel">
          <div class="git-panel-title">${escapeHtml(t("diff"))} ${selectedPath ? `<span>${escapeHtml(selectedPath)}</span>` : ""}</div>
          ${diff?.truncated ? `<div class="settings-inline-alert">${escapeHtml(t("diffTruncated"))}</div>` : ""}
          ${renderUnifiedDiff(patch, { cache: gitDiffLineCache, identity: diffIdentity, scrollTop: diffScrollTop })}
        </section>
        <!-- Checkpoint, merge, commit and history used to be four full-width cards
             stacked above the file list. On a repository with any real number of
             changes that pushed the files and the diff -- the reason the panel is
             open -- entirely below the fold, behind three panels of controls the
             user had not asked for yet. They are actions on the selection, so they
             belong beside it in a column that scrolls on its own. -->
        <aside class="git-side-column">
          ${renderRunCheckpointSection()}
          ${renderWorklineMergePanel()}
          ${renderGitCommitPanel(files, selectedCommitPaths)}
          <div class="git-log-panel">
            <div class="git-panel-title">${escapeHtml(t("recentCommits"))}</div>
            ${renderGitLog(log?.commits || [])}
          </div>
        </aside>
      </div>
    `;
    restoreGitDiffScroll(diffKey, diffScrollTop);
    bindGitModalActions();
  }

  function gitDiffRenderIdentity(status, selectedPath) {
    return {
      agentId: state.agent?.id || "",
      repoRoot: status?.repoRoot || state.agent?.cwd || state.project?.gitPath || "",
      scope: state.gitScope || "all",
      path: selectedPath || "",
    };
  }

  function restoreGitDiffScroll(diffKey, scrollTop) {
    const node = $("gitDiffView");
    if (!node || !diffKey || node.dataset?.gitDiffKey !== diffKey) return;
    node.scrollTop = scrollTop;
  }

  function bindGitDiffVirtualScroll() {
    const node = $("gitDiffView");
    if (!node?.dataset || node.dataset.gitDiffVirtual !== "true") return;
    let frame = 0;
    const renderWindow = () => {
      frame = 0;
      const key = node.dataset.gitDiffKey || "";
      const prepared = gitDiffLineCache.peek(key);
      if (!prepared) return;
      gitDiffScrollKey = key;
      gitDiffScrollTop = Number(node.scrollTop || 0);
      const window = computeGitDiffWindow({
        lineCount: prepared.lineCount,
        scrollTop: gitDiffScrollTop,
        viewportHeight: node.clientHeight || GIT_DIFF_VIRTUAL_DEFAULT_VIEWPORT_HEIGHT,
      });
      if (String(window.start) === node.dataset.gitDiffWindowStart && String(window.end) === node.dataset.gitDiffWindowEnd) return;
      node.dataset.gitDiffWindowStart = String(window.start);
      node.dataset.gitDiffWindowEnd = String(window.end);
      node.innerHTML = renderUnifiedDiffWindow(prepared.lines, window);
    };
    node.addEventListener("scroll", () => {
      if (frame) return;
      if (typeof globalThis.requestAnimationFrame === "function") {
        frame = globalThis.requestAnimationFrame(renderWindow);
        return;
      }
      renderWindow();
    }, { passive: true });
    renderWindow();
  }

  /**
   * The chat run-review card used to show the checkpoint state and the
   * rollback button; both moved here since the git modal is the only place
   * left that can act on them. Reads the run straight out of shared state
   * (state.activeRunSummary?.run) rather than any run-scoped prop, because
   * this modal isn't run-scoped and chat-rendering.mjs already populates
   * that state whenever a run summary loads.
   */
  function renderRunCheckpointSection() {
    const run = state.activeRunSummary?.run;
    const runId = state.activeRunSummaryRunId || run?.id || "";
    return `
      <section class="git-commit-panel git-run-checkpoint-panel" data-run-checkpoint-section>
        <div class="git-commit-head">
          <div><strong>${escapeHtml(cr("run.checkpointSectionTitle"))}</strong></div>
        </div>
        ${run ? renderRunCheckpointAvailable(run, runId) : renderRunCheckpointUnavailable()}
      </section>
    `;
  }

  function renderRunCheckpointAvailable(run, runId) {
    const checkpoint = runCheckpointState(run);
    const head = run.baseHead ? shortGitHash(run.baseHead) : cr("run.checkpointNotRecorded");
    const busy = Boolean(state.runRollbackBusy);
    const canRollback = checkpoint.available && Boolean(runId) && !busy;
    return `
      <div class="run-summary-checkpoint ${escapeAttr(checkpoint.tone)}">
        <span>${escapeHtml(cr("run.checkpoint"))}</span>
        <strong>${escapeHtml(head)}</strong>
        <em>${escapeHtml(checkpoint.reason)}</em>
      </div>
      <div class="git-commit-actions">
        <button id="runCheckpointRollbackBtn" class="ghost-btn mini danger" type="button" title="${escapeAttr(checkpoint.reason)}" ${canRollback ? "" : "disabled"}>${escapeHtml(busy ? cr("run.rollingBack") : cr("run.rollback"))}</button>
      </div>
    `;
  }

  function renderRunCheckpointUnavailable() {
    return `<div class="settings-empty-card compact">${escapeHtml(cr("run.checkpointNoActiveRun"))}</div>`;
  }

  // The integration panel: what would come over, whether it conflicts, how far
  // the two sides have diverged, and only then a button. A merge is never
  // performed as a side effect of opening or refreshing this modal.
  function renderWorklineMergePanel() {
    const workline = state.workline;
    if (!workline?.id) return "";
    // A merged workline gets the undo panel instead of the merge controls:
    // there is nothing further to integrate, but the merge itself can still be
    // taken back while the source branch survives.
    if (workline.status === "merged" && workline.mergeCommitSha) {
      const unmergeBusy = Boolean(state.worklineUnmergeBusy);
      const cleaned = !String(workline.worktreePath || "").trim();
      return `
        <section class="git-commit-panel git-merge-panel" data-workline-merge-panel>
          <div class="git-commit-head">
            <div>
              <strong>${escapeHtml(t("mergeSectionTitle"))}</strong>
              <span>${escapeHtml(t("unmergedNotice", { commit: shortGitHash(workline.mergeCommitSha || "") }))}</span>
            </div>
          </div>
          ${cleaned ? `<div class="settings-empty-card compact">${escapeHtml(t("unmergeCleanedHint"))}</div>` : ""}
          <div class="git-commit-actions">
            <button id="worklineUnmergeBtn" class="ghost-btn mini danger" type="button" ${unmergeBusy || cleaned ? "disabled" : ""}>${escapeHtml(unmergeBusy ? t("unmerging") : t("unmergeAction"))}</button>
          </div>
        </section>
      `;
    }
    const check = state.mergeCheck;
    const busy = Boolean(state.mergeCheckBusy);
    const merging = Boolean(state.mergeBusy);
    const isRoot = Boolean(workline.isRoot);
    const canMerge = Boolean(check?.canMerge) && !merging && !busy;
    return `
      <section class="git-commit-panel git-merge-panel" data-workline-merge-panel>
        <div class="git-commit-head">
          <div>
            <strong>${escapeHtml(t("mergeSectionTitle"))}</strong>
            <span>${escapeHtml(isRoot ? t("mergeRootOnly") : t("mergeSectionHint", { branch: workline.branch || workline.title || workline.id }))}</span>
          </div>
        </div>
        ${state.mergeCheckError ? `<div class="settings-inline-alert">${escapeHtml(state.mergeCheckError)}</div>` : ""}
        ${check ? renderWorklineMergeReport(check) : `<div class="settings-empty-card compact">${escapeHtml(busy ? t("mergeChecking") : t("mergeNotChecked"))}</div>`}
        <div class="git-commit-actions">
          <button id="worklineMergeCheckBtn" class="ghost-btn mini git-mini-btn" type="button" ${isRoot || busy || merging ? "disabled" : ""}>${escapeHtml(busy ? t("mergeChecking") : t("mergeCheckAction"))}</button>
          <button id="worklineMergeBtn" class="send-btn git-commit-submit" type="button" ${canMerge ? "" : "disabled"}>${escapeHtml(merging ? t("merging") : t("mergeAction"))}</button>
        </div>
      </section>
    `;
  }

  function renderWorklineMergeReport(check) {
    const conflicts = Array.isArray(check.conflicts) ? check.conflicts : [];
    const files = Array.isArray(check.changedFiles) ? check.changedFiles : [];
    const verdict = check.alreadyMerged
      ? { tone: "muted", text: t("mergeAlready") }
      : conflicts.length
        ? { tone: "warn", text: t("mergeConflicts", { count: formatNumber(conflicts.length) }) }
        : check.canMerge
          ? { tone: "ok", text: t("mergeClean") }
          : { tone: "warn", text: t("mergeBlocked") };
    // Dirty worktrees do not block the check but do block the merge itself, so
    // they are surfaced here rather than as a surprise error afterwards.
    const warnings = [
      check.sourceDirty ? t("mergeSourceDirty") : "",
      check.targetDirty ? t("mergeTargetDirty") : "",
      check.behind ? t("mergeBehindWarning", { count: formatNumber(check.behind) }) : "",
    ].filter(Boolean);
    return `
      <div class="run-summary-checkpoint ${escapeAttr(verdict.tone)}">
        <span>${escapeHtml(t("mergeVerdict"))}</span>
        <strong>${escapeHtml(verdict.text)}</strong>
        <em>${escapeHtml(t("mergeInto", { target: check.targetBranch || check.targetWorklineId || "" }))}</em>
      </div>
      <div class="git-merge-metrics">
        <span>${escapeHtml(t("mergeChangedFiles", { count: formatNumber(check.changedCount || 0) }))}</span>
        <span>${escapeHtml(t("aheadBehind", { ahead: formatNumber(check.ahead || 0), behind: formatNumber(check.behind || 0) }))}</span>
        <span>${escapeHtml(t("mergeConflictCount", { count: formatNumber(conflicts.length) }))}</span>
      </div>
      ${warnings.length ? `<div class="git-merge-warnings">${warnings.map((warning) => `<span>${escapeHtml(warning)}</span>`).join("")}</div>` : ""}
      ${conflicts.length ? `<div class="git-merge-file-list conflicts">${conflicts.map((path) => `<span>${escapeHtml(path)}</span>`).join("")}</div>` : ""}
      ${files.length ? `<div class="git-merge-file-list">${files.map((path) => `<span>${escapeHtml(path)}</span>`).join("")}${check.filesLimited ? `<em>${escapeHtml(t("mergeFilesLimited"))}</em>` : ""}</div>` : ""}
    `;
  }

  function renderGitCommitPanel(files, selectedPaths) {
    const selectedCount = selectedPaths.length;
    const busy = state.gitCommitBusy || Boolean(state.gitDiscardBusy);
    const disabled = busy || selectedCount === 0 || !String(state.gitCommitMessage || "").trim();
    return `
      <form id="gitCommitForm" class="git-commit-panel">
        <div class="git-commit-head">
          <div>
            <strong>${escapeHtml(t("commitSelected"))}</strong>
            <span>${escapeHtml(t("commitSafety"))}</span>
          </div>
          <span id="gitCommitSelectedCount" class="git-commit-count">${escapeHtml(t("selectedCount", { count: formatNumber(selectedCount) }))}</span>
        </div>
        <textarea id="gitCommitMessageInput" class="git-commit-message" rows="2" maxlength="10000" placeholder="${escapeAttr(t("commitPlaceholder"))}">${escapeHtml(state.gitCommitMessage || "")}</textarea>
        <div class="git-commit-actions">
          <button id="selectAllGitFilesBtn" class="ghost-btn mini git-mini-btn" type="button" ${files.length && !busy ? "" : "disabled"}>${escapeHtml(t("selectAll"))}</button>
          <button id="clearGitFilesBtn" class="ghost-btn mini git-mini-btn" type="button" ${selectedCount && !busy ? "" : "disabled"}>${escapeHtml(t("clearSelection"))}</button>
          <button id="gitDiscardBtn" class="ghost-btn mini git-mini-btn danger" type="button" ${selectedCount && !busy ? "" : "disabled"}>${escapeHtml(state.gitDiscardBusy ? t("discarding") : t("discardFiles"))}</button>
          <button id="gitCommitBtn" class="send-btn git-commit-submit" type="submit" ${disabled ? "disabled" : ""}>${escapeHtml(state.gitCommitBusy ? t("committing") : t("commitFiles"))}</button>
        </div>
      </form>
    `;
  }

  function renderGitFileList(files, selectedPath) {
    if (!files.length) return `<div class="settings-empty-card compact">${escapeHtml(t("noChanges"))}</div>`;
    const selected = state.gitCommitSelected || {};
    return files.map((file) => {
      const path = file.path || "";
      const checked = Boolean(selected[path]);
      return `
        <div class="git-file-row ${path === selectedPath ? "active" : ""}">
          <input class="git-file-checkbox" type="checkbox" data-git-select="${escapeAttr(path)}" aria-label="${escapeAttr(t("selectFile", { path }))}" ${checked ? "checked" : ""} ${state.gitCommitBusy ? "disabled" : ""} />
          <button class="git-file-open" type="button" data-git-file="${escapeAttr(path)}">
            <span class="git-file-status ${gitFileBadgeClass(file)}">${escapeHtml(gitStatusLabel(file))}</span>
            <span class="git-file-path">${escapeHtml(path)}</span>
          </button>
        </div>
      `;
    }).join("");
  }


  function renderGitLog(commits) {
    if (!Array.isArray(commits) || !commits.length) return `<div class="settings-empty-card compact">${escapeHtml(t("noHistory"))}</div>`;
    return `<div class="git-log-list">${commits.map((commit) => `
      <div class="git-log-row">
        <strong>${escapeHtml(commit.shortHash || "")}</strong>
        <span>${escapeHtml(commit.subject || "")}</span>
        <small>${escapeHtml(formatTimestamp(commit.date))}</small>
      </div>
    `).join("")}</div>`;
  }

  function bindGitModalActions() {
    $("runCheckpointRollbackBtn")?.addEventListener("click", () => rollbackMostRecentRun().catch(showError));
    $("worklineMergeCheckBtn")?.addEventListener("click", () => loadWorklineMergeCheck().catch(showError));
    $("worklineMergeBtn")?.addEventListener("click", () => mergeCurrentWorkline().catch(showError));
    $("worklineUnmergeBtn")?.addEventListener("click", () => unmergeCurrentWorkline().catch(showError));
    $("refreshGitBtn")?.addEventListener("click", () => refreshGitWorkflow({ notify: true }).catch(showError));
    $("gitScopeSelect")?.addEventListener("change", (event) => {
      state.gitScope = event.target.value || "all";
      loadGitDiff().catch(showError);
    });
    $("gitCommitForm")?.addEventListener("submit", (event) => commitGitSelection(event).catch(showError));
    $("gitDiscardBtn")?.addEventListener("click", () => discardGitSelection().catch(showError));
    $("gitCommitMessageInput")?.addEventListener("input", updateGitCommitControls);
    $("selectAllGitFilesBtn")?.addEventListener("click", () => setGitCommitSelection((state.gitStatus?.files || []).map((file) => file.path || "").filter(Boolean)));
    $("clearGitFilesBtn")?.addEventListener("click", () => setGitCommitSelection([]));
    document.querySelectorAll("[data-git-select]").forEach((node) => {
      node.addEventListener("change", () => {
        const path = node.dataset.gitSelect || "";
        const next = Object.assign(Object.create(null), state.gitCommitSelected || {});
        if (node.checked) next[path] = true;
        else delete next[path];
        state.gitCommitSelected = next;
        renderGitModal();
      });
    });
    document.querySelectorAll("[data-git-file]").forEach((node) => {
      node.addEventListener("click", () => {
        state.gitSelectedPath = node.dataset.gitFile || "";
        renderGitModal();
        loadGitDiff({ path: state.gitSelectedPath }).catch(showError);
      });
    });
    bindGitDiffVirtualScroll();
  }

  function gitStatusLabel(file) {
    if (file.untracked) return "??";
    return `${file.index || " "}${file.worktree || " "}`.trim() || "M";
  }

  function gitFileBadgeClass(file) {
    if (file.untracked) return "untracked";
    if (file.staged && file.unstaged) return "mixed";
    if (file.staged) return "staged";
    return "modified";
  }

  return {
    closeGitModal,
    discardGitSelection,
    loadGitDiff,
    loadGitLog,
    loadGitStatus,
    openGitModal,
    loadWorklineMergeCheck,
    mergeCurrentWorkline,
    refreshGitWorkflow,
    renderGitButtonState,
    renderGitModal,
    rollbackMostRecentRun,
    resetGitWorkflowState,
  };
}
