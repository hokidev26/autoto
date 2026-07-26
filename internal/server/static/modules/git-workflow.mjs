import { $, escapeAttr, escapeHtml } from "./dom.mjs";
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

export function createGitWorkflowController({
  state,
  showError,
  showToast,
  apiRequest = api,
} = {}) {
  const request = apiRequest;

  function resetGitWorkflowState() {
    state.gitStatus = null;
    state.gitDiff = null;
    state.gitLog = null;
    state.gitError = "";
    state.gitSelectedPath = "";
    state.gitCommitMessage = "";
    state.gitCommitSelected = {};
    state.gitCommitBusy = false;
    state.gitOpen = false;
    state.gitSeq++;
    renderGitButtonState();
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
      badge.textContent = count > 99 ? "99+" : String(count);
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
    const status = state.gitStatus;
    const files = Array.isArray(status?.files) ? status.files : [];
    const diff = state.gitDiff;
    const log = state.gitLog;
    const selectedPath = state.gitSelectedPath || "";
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
      ${renderRunCheckpointSection()}
      ${renderGitCommitPanel(files, selectedCommitPaths)}
      <div class="git-layout">
        <aside class="git-file-list">
          <div class="git-panel-title">${escapeHtml(t("changedFiles"))}</div>
          ${renderGitFileList(files, selectedPath)}
        </aside>
        <section class="git-diff-panel">
          <div class="git-panel-title">${escapeHtml(t("diff"))} ${selectedPath ? `<span>${escapeHtml(selectedPath)}</span>` : ""}</div>
          ${diff?.truncated ? `<div class="settings-inline-alert">${escapeHtml(t("diffTruncated"))}</div>` : ""}
          ${renderUnifiedDiff(diff?.patch || "")}
        </section>
        <aside class="git-log-panel">
          <div class="git-panel-title">${escapeHtml(t("recentCommits"))}</div>
          ${renderGitLog(log?.commits || [])}
        </aside>
      </div>
    `;
    bindGitModalActions();
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

  function renderGitCommitPanel(files, selectedPaths) {
    const selectedCount = selectedPaths.length;
    const disabled = state.gitCommitBusy || selectedCount === 0 || !String(state.gitCommitMessage || "").trim();
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
          <button id="selectAllGitFilesBtn" class="ghost-btn mini git-mini-btn" type="button" ${files.length && !state.gitCommitBusy ? "" : "disabled"}>${escapeHtml(t("selectAll"))}</button>
          <button id="clearGitFilesBtn" class="ghost-btn mini git-mini-btn" type="button" ${selectedCount && !state.gitCommitBusy ? "" : "disabled"}>${escapeHtml(t("clearSelection"))}</button>
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

  function renderUnifiedDiff(patch) {
    if (!patch) return `<pre class="git-diff-view empty">${escapeHtml(t("noDiff"))}</pre>`;
    const lines = String(patch).split("\n");
    return `<pre class="git-diff-view">${lines.map((line) => `<div class="diff-line ${diffLineClass(line)}">${escapeHtml(line || " ")}</div>`).join("")}</pre>`;
  }

  function diffLineClass(line) {
    if (line.startsWith("@@")) return "hunk";
    if (line.startsWith("diff --git") || line.startsWith("index ") || line.startsWith("---") || line.startsWith("+++")) return "meta";
    if (line.startsWith("+")) return "add";
    if (line.startsWith("-")) return "del";
    return "context";
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
    $("refreshGitBtn")?.addEventListener("click", () => refreshGitWorkflow({ notify: true }).catch(showError));
    $("gitScopeSelect")?.addEventListener("change", (event) => {
      state.gitScope = event.target.value || "all";
      loadGitDiff().catch(showError);
    });
    $("gitCommitForm")?.addEventListener("submit", (event) => commitGitSelection(event).catch(showError));
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
    loadGitDiff,
    loadGitLog,
    loadGitStatus,
    openGitModal,
    refreshGitWorkflow,
    renderGitButtonState,
    renderGitModal,
    rollbackMostRecentRun,
    resetGitWorkflowState,
  };
}
