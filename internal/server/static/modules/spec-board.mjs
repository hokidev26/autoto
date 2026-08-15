import { objectValue } from "./value-coercion.mjs";
import { escapeAttr, escapeHtml, setTextIfChanged } from "./dom.mjs";
import { t } from "./i18n.mjs";
import { shellExtraT as sx } from "./messages-shell-extra.mjs";
import { confirm as platformConfirm } from "./platform.mjs";

function specMessage(key, params = {}) {
  return sx(`spec.${key}`, params);
}

export const specBoardLimits = Object.freeze({ tasks: 200, children: 100, confirmations: 20 });
export const specTaskStatuses = Object.freeze(["todo", "doing", "done", "blocked"]);

function text(value, limit = 8000) {
  return String(value ?? "").trim().slice(0, limit);
}

function integer(value, fallback = 0) {
  const number = Number(value);
  return Number.isSafeInteger(number) && number >= 0 ? number : fallback;
}

export function normalizeSpecTask(value = {}) {
  const source = objectValue(value);
  const status = text(source.status, 24);
  return {
    id: text(source.id, 160),
    agentId: text(source.agentId, 160),
    text: text(source.text, 16000),
    status: specTaskStatuses.includes(status) ? status : "todo",
    protected: Boolean(source.protected),
    position: integer(source.position),
    revision: Math.max(1, integer(source.revision, 1)),
    sourceType: text(source.sourceType, 40),
    updatedAt: text(source.updatedAt, 80),
  };
}

export function normalizeGoalConfirmation(value = {}) {
  const source = objectValue(value);
  return {
    id: text(source.id, 160),
    agentId: text(source.agentId, 160),
    taskId: text(source.taskId, 160),
    queueState: text(source.queueState, 40) || "queued",
    status: text(source.status, 40) || "accepted",
    createdAt: text(source.createdAt, 80),
  };
}

export function normalizeSpecBoard(value = {}, fallbackAgentId = "") {
  const source = objectValue(value);
  return {
    agentId: text(source.agentId || fallbackAgentId, 160),
    revision: integer(source.revision),
    updatedAt: text(source.updatedAt, 80),
    tasks: (Array.isArray(source.tasks) ? source.tasks : []).slice(0, specBoardLimits.tasks).map(normalizeSpecTask),
    goalConfirmations: (Array.isArray(source.goalConfirmations) ? source.goalConfirmations : []).slice(0, specBoardLimits.confirmations).map(normalizeGoalConfirmation),
  };
}

function normalizeAgent(value = {}) {
  const source = objectValue(value);
  return { id: text(source.id, 160), title: text(source.title || source.name || source.id, 160) };
}

function statusOptions(selected) {
  return specTaskStatuses.map((status) => `<option value="${status}" ${status === selected ? "selected" : ""}>${escapeHtml(specMessage(`statuses.${status}`))}</option>`).join("");
}

function specMoveButton(direction, disabled) {
  const label = specMessage(direction === "up" ? "moveUp" : "moveDown");
  const path = direction === "up" ? "m6 14 6-6 6 6" : "m6 10 6 6 6-6";
  return `<button class="ghost-btn mini spec-move-btn" type="button" data-spec-move="${escapeAttr(direction)}" aria-label="${escapeAttr(label)}" title="${escapeAttr(label)}"${disabled ? " disabled" : ""}><svg viewBox="0 0 24 24" aria-hidden="true"><path d="${path}"></path></svg></button>`;
}

function renderConfirmationCards(board, latestConfirmation) {
  const confirmations = [...(latestConfirmation ? [normalizeGoalConfirmation(latestConfirmation)] : []), ...board.goalConfirmations];
  const seen = new Set();
  return confirmations.filter((item) => item.id && !seen.has(item.id) && seen.add(item.id)).slice(0, specBoardLimits.confirmations).map((item) => {
    const task = board.tasks.find((candidate) => candidate.id === item.taskId);
    return `<article class="spec-confirmation-card" data-goal-confirmation="${escapeAttr(item.id)}">
      <strong>${escapeHtml(t("workspace.spec.goalWritten"))}</strong>
      <span>${escapeHtml(task?.text || t("workspace.spec.taskCreated", { id: item.taskId || t("common.loading") }))}</span>
      <small>${escapeHtml(`${item.queueState} · ${item.status}`)}</small>
    </article>`;
  }).join("");
}

export function renderSpecBoard(value = {}) {
  const source = objectValue(value);
  const board = normalizeSpecBoard(source.board, source.selectedAgentId);
  const rootAgent = normalizeAgent(source.rootAgent);
  const children = (Array.isArray(source.children) ? source.children : []).slice(0, specBoardLimits.children).map(normalizeAgent).filter((item) => item.id);
  const agents = [rootAgent, ...children.filter((item) => item.id !== rootAgent.id)].filter((item) => item.id);
  const selectedAgentId = text(source.selectedAgentId || rootAgent.id, 160);
  if (!rootAgent.id) return `<div class="spec-empty">${escapeHtml(t("workspace.spec.selectAgent"))}</div>`;
  if (source.loading && !source.loaded) return `<div class="spec-empty" aria-busy="true">${escapeHtml(t("workspace.spec.loading"))}</div>`;
  return `<div class="spec-board-shell">
    <div class="spec-board-toolbar">
      <label>${escapeHtml(specMessage("agent"))}<select id="specAgentSelect">${agents.map((agent) => `<option value="${escapeAttr(agent.id)}" ${agent.id === selectedAgentId ? "selected" : ""}>${escapeHtml(agent.title || agent.id)}</option>`).join("")}</select></label>
      <button id="refreshSpecBoardBtn" class="ghost-btn mini" type="button">${escapeHtml(t("workspace.spec.refresh"))}</button>
    </div>
    <p class="spec-board-hint">${escapeHtml(t("workspace.spec.hint"))}</p>
    ${source.error ? `<div class="spec-error" role="alert">${escapeHtml(source.error)}</div>` : ""}
    <form id="createSpecTaskForm" class="spec-create-form">
      <textarea id="newSpecTaskText" rows="3" maxlength="16000" placeholder="${escapeAttr(t("workspace.spec.newTask"))}" required></textarea>
      <select id="newSpecTaskStatus">${statusOptions("todo")}</select>
      <label class="spec-protected-toggle"><input id="newSpecTaskProtected" type="checkbox" /> ${escapeHtml(specMessage("protected"))}</label>
      <button class="send-btn" type="submit" ${source.saving ? "disabled" : ""}>${escapeHtml(t("workspace.spec.add"))}</button>
    </form>
    <div class="spec-task-list">
      ${board.tasks.length ? board.tasks.map((task, index) => `<article class="spec-task-card ${task.protected ? "protected" : ""}" data-spec-task="${escapeAttr(task.id)}">
        <div class="spec-task-head"><strong>${task.protected ? `🔒 ${escapeHtml(specMessage("protected"))}` : `#${index + 1}`}</strong><small>${escapeHtml(specMessage("revision", { revision: task.revision }))}</small></div>
        <textarea data-spec-task-text rows="3" maxlength="16000">${escapeHtml(task.text)}</textarea>
        <div class="spec-task-controls">
          <select data-spec-task-status>${statusOptions(task.status)}</select>
          <label class="spec-protected-toggle"><input data-spec-task-protected type="checkbox" ${task.protected ? "checked" : ""} /> ${escapeHtml(specMessage("protected"))}</label>
          ${specMoveButton("up", index === 0)}
          ${specMoveButton("down", index === board.tasks.length - 1)}
          <button class="ghost-btn mini" type="button" data-spec-save>${escapeHtml(specMessage("save"))}</button>
          <button class="ghost-btn mini danger" type="button" data-spec-delete>${escapeHtml(specMessage("delete"))}</button>
        </div>
      </article>`).join("") : `<div class="spec-empty">${escapeHtml(specMessage("noTasks"))}</div>`}
    </div>
    <section class="spec-confirmations"><h3>${escapeHtml(specMessage("goalConfirmation"))}</h3>${renderConfirmationCards(board, source.latestConfirmation) || `<div class="spec-empty compact">${escapeHtml(specMessage("noConfirmations"))}</div>`}</section>
  </div>`;
}

export function createSpecBoardController({
  request,
  onChange,
  showError,
  showToast,
  confirmAction,
  onDockOpen = () => {},
  onDockClose = () => {},
  document: documentImpl = globalThis.document,
} = {}) {
  if (typeof request !== "function") throw new TypeError("Spec board request must be a function");
  const confirm = confirmAction || platformConfirm;
  const state = {
    rootAgent: null,
    selectedAgentId: "",
    children: [],
    board: normalizeSpecBoard({}),
    latestConfirmation: null,
    loading: false,
    loaded: false,
    saving: false,
    error: "",
    open: false,
    seq: 0,
  };
  const subscribers = new Set();

  const snapshot = () => ({ ...state, children: state.children.map((item) => ({ ...item })), board: normalizeSpecBoard(state.board), rootAgent: state.rootAgent ? { ...state.rootAgent } : null });
  const modal = () => documentImpl?.getElementById?.("specBoardModal");
  const body = () => documentImpl?.getElementById?.("specBoardBody");

  function renderButtonState() {
    const button = documentImpl?.getElementById?.("specBoardBtn");
    if (!button) return;
    const enabled = Boolean(state.rootAgent?.id);
    const tasks = state.board.tasks || [];
    const doing = tasks.filter((task) => task.status === "doing").length;
    const blocked = tasks.filter((task) => task.status === "blocked").length;
    const attention = blocked + doing;
    button.disabled = !enabled;
    button.classList.toggle("active", enabled && state.open);
    button.classList.toggle("has-blocked", blocked > 0 && !state.open);
    button.setAttribute("aria-expanded", enabled && state.open ? "true" : "false");
    button.title = !enabled ? t("workspace.spec.selectAgent") : blocked ? t("workspace.spec.statusBlocked", { doing, blocked }) : doing ? t("workspace.spec.statusDoing", { doing }) : t("workspace.spec.open");
    const badge = button.querySelector("[data-spec-tool-badge]");
    if (badge) {
      setTextIfChanged(badge, attention > 99 ? "99+" : String(attention));
      badge.classList.toggle("hidden", attention === 0);
    }
  }

  function emit() {
    const value = snapshot();
    onChange?.(value);
    subscribers.forEach((subscriber) => {
      try {
        subscriber(value);
      } catch (error) {
        showError?.(error);
      }
    });
    render();
    renderConfirmationStack();
    renderButtonState();
  }

  function unsubscribe(subscriber) {
    return subscribers.delete(subscriber);
  }

  function subscribe(subscriber, { immediate = true } = {}) {
    if (typeof subscriber !== "function") throw new TypeError("Spec board subscriber must be a function");
    subscribers.add(subscriber);
    if (immediate) subscriber(snapshot());
    return () => unsubscribe(subscriber);
  }

  function render() {
    const target = body();
    if (!target || !state.open) return;
    target.innerHTML = renderSpecBoard(snapshot());
    bindBody();
  }

  function renderConfirmationStack() {
    const target = documentImpl?.getElementById?.("goalConfirmationStack");
    if (!target) return;
    const html = renderConfirmationCards(state.board, state.latestConfirmation);
    target.innerHTML = html;
    target.classList.toggle("hidden", !html);
  }

  function setAgent(agent) {
    ++state.seq;
    state.rootAgent = agent?.id ? normalizeAgent(agent) : null;
    state.selectedAgentId = state.rootAgent?.id || "";
    state.children = [];
    state.board = normalizeSpecBoard({}, state.selectedAgentId);
    state.latestConfirmation = null;
    state.loaded = false;
    state.saving = false;
    state.error = "";
    emit();
  }

  async function load({ includeChildren = true } = {}) {
    const rootId = state.rootAgent?.id || "";
    const agentId = state.selectedAgentId || rootId;
    if (!rootId || !agentId) return false;
    const seq = ++state.seq;
    state.loading = true;
    state.error = "";
    emit();
    try {
      const requests = [request(`/api/agents/${encodeURIComponent(agentId)}/spec`)];
      if (includeChildren) requests.push(request(`/api/agents/${encodeURIComponent(rootId)}/children`));
      const [boardResult, childrenResult] = await Promise.all(requests);
      if (seq !== state.seq) return false;
      state.board = normalizeSpecBoard(boardResult, agentId);
      if (includeChildren) state.children = (Array.isArray(childrenResult) ? childrenResult : []).slice(0, specBoardLimits.children).map(normalizeAgent);
      state.loaded = true;
      return true;
    } catch (error) {
      if (seq !== state.seq) return false;
      state.error = error?.message || String(error);
      showError?.(error);
      return false;
    } finally {
      if (seq === state.seq) {
        state.loading = false;
        emit();
      }
    }
  }

  async function selectAgent(agentId) {
    const id = text(agentId, 160);
    if (!id || id === state.selectedAgentId) return;
    ++state.seq;
    state.selectedAgentId = id;
    state.board = normalizeSpecBoard({}, id);
    state.latestConfirmation = null;
    state.loaded = false;
    state.saving = false;
    state.error = "";
    emit();
    await load({ includeChildren: false });
  }

  async function mutate(path, options, success) {
    if (state.saving || !state.selectedAgentId) return false;
    const seq = ++state.seq;
    const agentId = state.selectedAgentId;
    state.saving = true;
    state.error = "";
    emit();
    try {
      const result = await request(path, options);
      if (seq !== state.seq || agentId !== state.selectedAgentId) return false;
      state.board = normalizeSpecBoard(result, agentId);
      showToast?.(success, "success", { force: true });
      return true;
    } catch (error) {
      if (seq !== state.seq || agentId !== state.selectedAgentId) return false;
      state.error = error?.message || String(error);
      showError?.(error);
      state.saving = false;
      emit();
      await load({ includeChildren: false });
      return false;
    } finally {
      if (seq === state.seq && agentId === state.selectedAgentId) {
        state.saving = false;
        emit();
      }
    }
  }

  async function acknowledgeProtected(action) {
    if (!await confirm(t("workspace.spec.protectedConfirm", { action }))) return false;
    return Boolean(await confirm(t("workspace.spec.protectedConfirmAgain", { action })));
  }

  async function createTask(input) {
    const payload = { text: text(input.text, 16000), status: specTaskStatuses.includes(input.status) ? input.status : "todo", protected: Boolean(input.protected) };
    if (!payload.text) return false;
    return mutate(`/api/agents/${encodeURIComponent(state.selectedAgentId)}/spec/tasks`, { method: "POST", body: JSON.stringify(payload) }, t("workspace.spec.created"));
  }

  async function updateTask(taskId, input) {
    const task = state.board.tasks.find((item) => item.id === taskId);
    if (!task) return false;
    const acknowledgeProtected = task.protected ? await acknowledgeProtected(t("workspace.spec.modify")) : false;
    if (task.protected && !acknowledgeProtected) return false;
    const payload = {
      text: text(input.text, 16000),
      status: specTaskStatuses.includes(input.status) ? input.status : task.status,
      protected: Boolean(input.protected),
      expectedRevision: task.revision,
      acknowledgeProtected,
    };
    return mutate(`/api/agents/${encodeURIComponent(state.selectedAgentId)}/spec/tasks/${encodeURIComponent(task.id)}`, { method: "PATCH", body: JSON.stringify(payload)     }, t("workspace.spec.updated"));
  }

  async function deleteTask(taskId) {
    const task = state.board.tasks.find((item) => item.id === taskId);
    if (!task) return false;
    const acknowledgeProtected = task.protected ? await acknowledgeProtected(t("workspace.spec.delete")) : Boolean(await confirm(t("workspace.spec.deleteConfirm")));
    if (!acknowledgeProtected) return false;
    return mutate(`/api/agents/${encodeURIComponent(state.selectedAgentId)}/spec/tasks/${encodeURIComponent(task.id)}`, {
      method: "DELETE",
      body: JSON.stringify({ expectedRevision: task.revision, acknowledgeProtected: task.protected }),
    }, t("workspace.spec.deleted"));
  }

  async function moveTask(taskId, direction) {
    const ids = state.board.tasks.map((item) => item.id);
    const index = ids.indexOf(taskId);
    const target = direction === "up" ? index - 1 : index + 1;
    if (index < 0 || target < 0 || target >= ids.length) return false;
    [ids[index], ids[target]] = [ids[target], ids[index]];
    return mutate(`/api/agents/${encodeURIComponent(state.selectedAgentId)}/spec/tasks/order`, {
      method: "PUT",
      body: JSON.stringify({ taskIds: ids, expectedBoardRevision: state.board.revision }),
    }, t("workspace.spec.orderUpdated"));
  }

  async function handleGoalConfirmation(result, agentId = state.rootAgent?.id) {
    if (result?.kind !== "goal.confirmation") return false;
    const id = text(agentId, 160);
    const belongsToCurrentRoot = id === state.rootAgent?.id || state.children.some((child) => child.id === id);
    if (!id || !belongsToCurrentRoot) return false;
    state.latestConfirmation = normalizeGoalConfirmation(result.confirmation);
    if (result.board && id === state.selectedAgentId) state.board = normalizeSpecBoard(result.board, id);
    emit();
    if (id === state.selectedAgentId) await load({ includeChildren: false });
    return true;
  }

  function taskInput(card) {
    return {
      text: card.querySelector("[data-spec-task-text]")?.value || "",
      status: card.querySelector("[data-spec-task-status]")?.value || "todo",
      protected: Boolean(card.querySelector("[data-spec-task-protected]")?.checked),
    };
  }

  function bindBody() {
    const target = body();
    if (!target) return;
    target.querySelector("#refreshSpecBoardBtn")?.addEventListener("click", () => load());
    target.querySelector("#specAgentSelect")?.addEventListener("change", (event) => selectAgent(event.currentTarget.value));
    target.querySelector("#createSpecTaskForm")?.addEventListener("submit", (event) => {
      event.preventDefault();
      createTask({ text: target.querySelector("#newSpecTaskText")?.value, status: target.querySelector("#newSpecTaskStatus")?.value, protected: target.querySelector("#newSpecTaskProtected")?.checked });
    });
    target.querySelectorAll("[data-spec-task]").forEach((card) => {
      const id = card.dataset.specTask;
      card.querySelector("[data-spec-save]")?.addEventListener("click", () => updateTask(id, taskInput(card)));
      card.querySelector("[data-spec-delete]")?.addEventListener("click", () => deleteTask(id));
      card.querySelectorAll("[data-spec-move]").forEach((button) => button.addEventListener("click", () => moveTask(id, button.dataset.specMove)));
    });
  }

  function open() {
    if (!state.rootAgent?.id) return false;
    state.open = true;
    modal()?.classList.remove("hidden");
    // Docks into the right-hand utility column rather than opening as a centred
    // dialog over the conversation. onDockOpen is what allocates that column and
    // closes the other panels sharing it -- all of them occupy the same grid cell,
    // so two open at once would render on top of each other.
    modal()?.classList.add("shell-dock-mode");
    onDockOpen();
    renderButtonState();
    render();
    load();
    return true;
  }

  function close() {
    const wasOpen = state.open;
    state.open = false;
    modal()?.classList.add("hidden");
    modal()?.classList.remove("shell-dock-mode");
    // Only when it really was open: the column is shared, so releasing one this
    // board never claimed would tear down whichever panel is actually in it.
    if (wasOpen) onDockClose();
    renderButtonState();
  }

  function toggle() {
    if (state.open) {
      close();
      return false;
    }
    return open();
  }

  function bind() {
    // A toggle rather than an opener. renderButtonState already reports
    // aria-expanded and lights the button while the board is up, so pressing it
    // again has to close it; wiring open() directly made a lit, expanded button
    // do nothing, leaving the × as the only way out of a panel it had opened.
    documentImpl?.getElementById?.("specBoardBtn")?.addEventListener("click", toggle);
    documentImpl?.getElementById?.("closeSpecBoardBtn")?.addEventListener("click", close);
    modal()?.addEventListener("click", (event) => { if (event.target?.id === "specBoardModal") close(); });
    // Escape closes every other dialog in the app -- the workspace explorer, the
    // setup wizard, the context panel, the provider console, the screenshot editor.
    // This board was the one that trapped the reader with only the mouse.
    documentImpl?.addEventListener?.("keydown", (event) => {
      if (event.key === "Escape" && state.open) {
        close();
        event.preventDefault();
      }
    });
    renderButtonState();
  }

  return { bind, close, createTask, deleteTask, getState: snapshot, handleGoalConfirmation, load, moveTask, open, render, renderButtonState, selectAgent, setAgent, subscribe, toggle, unsubscribe, updateTask };
}
