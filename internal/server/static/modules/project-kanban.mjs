import { objectValue } from "./value-coercion.mjs";
import { escapeAttr, escapeHtml } from "./dom.mjs";
import { normalizeSpecBoard, normalizeSpecTask } from "./spec-board.mjs";

export const projectKanbanStatuses = Object.freeze(["todo", "doing", "blocked", "done"]);

export const projectKanbanFallbackLabels = Object.freeze({
  title: "当前 Agent 任务看板",
  loading: "加载中…",
  empty: "暂无任务",
  selectAgent: "请选择 Agent",
  createTask: "新建任务",
  taskText: "任务内容",
  add: "添加",
  protected: "保护",
  revision: "修订 {revision}",
  source: "来源：{source}",
  unknownSource: "未标记",
  moveTo: "移动到",
  edit: "编辑",
  save: "保存",
  cancel: "取消",
  delete: "删除",
  unavailable: "看板操作暂不可用",
  statuses: Object.freeze({ todo: "待办", doing: "进行中", blocked: "已阻塞", done: "已完成" }),
});

function labelAt(labels, key) {
  return key.split(".").reduce((value, part) => objectValue(value)[part], labels);
}

function format(value, params = {}) {
  return String(value ?? "").replace(/\{([\w-]+)\}/g, (_, key) => String(params[key] ?? ""));
}

function createLabels(labels, translate) {
  return (key, params = {}) => {
    const fallback = labelAt(projectKanbanFallbackLabels, key);
    let value = labelAt(labels, key);
    if (value === undefined && typeof translate === "function") value = translate(`projectKanban.${key}`, params, fallback);
    if (value === undefined || value === null || value === "") value = fallback;
    return format(value, params);
  };
}

function disabled(value) {
  return value ? " disabled" : "";
}

function taskStatusOptions(selected, label, { includeSelected = true, placeholder = "" } = {}) {
  const placeholderOption = placeholder ? `<option value="" disabled selected>${escapeHtml(placeholder)}</option>` : "";
  return placeholderOption + projectKanbanStatuses.filter((status) => includeSelected || status !== selected).map((status) => `<option value="${status}" ${!placeholder && status === selected ? "selected" : ""}>${escapeHtml(label(`statuses.${status}`))}</option>`).join("");
}

function taskCard(task, value, label) {
  const editing = value.editingTaskId === task.id && value.canMutate;
  const busy = Boolean(value.saving);
  const source = task.sourceType || label("unknownSource");
  const statusName = label(`statuses.${task.status}`);
  const controlId = escapeAttr(task.id);
  const taskText = editing
    ? `<textarea data-kanban-task-text rows="3" maxlength="16000" aria-label="${escapeAttr(label("taskText"))}">${escapeHtml(task.text)}</textarea>
      <label><input data-kanban-task-protected type="checkbox" ${task.protected ? "checked" : ""}${disabled(busy)} /> ${escapeHtml(label("protected"))}</label>`
    : `<p class="project-kanban-card-text">${escapeHtml(task.text)}</p>`;
  const actions = editing
    ? `<button type="button" data-kanban-save${disabled(busy)}>${escapeHtml(label("save"))}</button>
      <button type="button" data-kanban-cancel${disabled(busy)}>${escapeHtml(label("cancel"))}</button>`
    : `<button type="button" data-kanban-edit${disabled(busy)}>${escapeHtml(label("edit"))}</button>`;
  return `<article class="project-kanban-card ${task.protected ? "protected" : ""}" data-project-kanban-task="${controlId}">
    <header class="project-kanban-card-head">
      <span class="project-kanban-status">${escapeHtml(statusName)}</span>
      ${task.protected ? `<span class="project-kanban-protected">${escapeHtml(label("protected"))}</span>` : ""}
    </header>
    ${taskText}
    <footer class="project-kanban-card-meta">
      <small>${escapeHtml(label("revision", { revision: task.revision }))}</small>
      <small>${escapeHtml(label("source", { source }))}</small>
    </footer>
    <div class="project-kanban-card-actions">
      <label>${escapeHtml(label("moveTo"))}
        <select data-kanban-move aria-label="${escapeAttr(label("moveTo"))}"${disabled(busy)}>${taskStatusOptions(task.status, label, { includeSelected: false, placeholder: label("moveTo") })}</select>
      </label>
      ${actions}
      <button type="button" data-kanban-delete${disabled(busy)}>${escapeHtml(label("delete"))}</button>
    </div>
  </article>`;
}

/**
 * Renders a board snapshot from createSpecBoardController without issuing requests.
 */
export function renderProjectKanban(value = {}, { labels, translate } = {}) {
  const source = objectValue(value);
  const label = createLabels(labels, translate);
  const board = normalizeSpecBoard(source.board, source.selectedAgentId);
  const canMutate = Boolean(source.canMutate);
  const renderValue = { ...source, canMutate, editingTaskId: String(source.editingTaskId || "") };
  const taskColumns = projectKanbanStatuses.map((status) => {
    const tasks = board.tasks.filter((task) => task.status === status);
    return `<section class="project-kanban-column" data-kanban-status="${status}">
      <h3>${escapeHtml(label(`statuses.${status}`))} <small>${tasks.length}</small></h3>
      <div class="project-kanban-column-cards">${tasks.length
        ? tasks.map((task) => taskCard(task, renderValue, label)).join("")
        : `<div class="project-kanban-empty">${escapeHtml(label("empty"))}</div>`}</div>
    </section>`;
  }).join("");
  const noAgent = !source.selectedAgentId;
  const unavailable = !canMutate;
  return `<section class="project-kanban" aria-label="${escapeAttr(label("title"))}" aria-busy="${source.loading ? "true" : "false"}">
    <header class="project-kanban-header"><h2>${escapeHtml(label("title"))}</h2></header>
    ${source.loading && !source.loaded ? `<div class="project-kanban-loading" role="status">${escapeHtml(label("loading"))}</div>` : ""}
    ${source.error ? `<div class="project-kanban-error" role="alert">${escapeHtml(source.error)}</div>` : ""}
    ${noAgent ? `<div class="project-kanban-empty">${escapeHtml(label("selectAgent"))}</div>` : `<form class="project-kanban-create" data-kanban-create>
      <textarea data-kanban-create-text rows="2" maxlength="16000" required placeholder="${escapeAttr(label("taskText"))}"${disabled(unavailable || source.saving)}></textarea>
      <select data-kanban-create-status${disabled(unavailable || source.saving)}>${taskStatusOptions("todo", label)}</select>
      <label><input data-kanban-create-protected type="checkbox"${disabled(unavailable || source.saving)} /> ${escapeHtml(label("protected"))}</label>
      <button type="submit"${disabled(unavailable || source.saving)}>${escapeHtml(label("add"))}</button>
    </form>`}
    ${unavailable && !noAgent ? `<div class="project-kanban-error" role="status">${escapeHtml(label("unavailable"))}</div>` : ""}
    <div class="project-kanban-columns">${taskColumns}</div>
  </section>`;
}

/**
 * Mounts an accessible Kanban view backed by the existing Spec board controller.
 * It owns only ephemeral edit selection; loading and CRUD remain in specBoard.
 */
export function createProjectKanbanController({
  specBoard,
  boardState,
  host,
  labels,
  translate,
  showError,
  showToast,
  document: documentImpl = globalThis.document,
} = {}) {
  const boardController = specBoard && typeof specBoard.getState === "function" ? specBoard : null;
  const readBoardState = () => {
    if (boardController) return boardController.getState();
    return typeof boardState === "function" ? boardState() : objectValue(boardState);
  };
  const target = () => typeof host === "string" ? documentImpl?.querySelector?.(host) : host;
  let editingTaskId = "";
  let observedAgentId = "";
  let unsubscribe = null;

  function currentState() {
    const state = objectValue(readBoardState());
    return { ...state, editingTaskId, canMutate: Boolean(boardController) };
  }

  function reportUnavailable() {
    showError?.(new Error(createLabels(labels, translate)("unavailable")));
    return false;
  }

  async function invoke(method, ...args) {
    if (!boardController || typeof boardController[method] !== "function") return reportUnavailable();
    try {
      return Boolean(await boardController[method](...args));
    } catch (error) {
      showError?.(error);
      return false;
    }
  }

  function taskById(taskId) {
    return normalizeSpecBoard(currentState().board, currentState().selectedAgentId).tasks.find((task) => task.id === taskId) || null;
  }

  function render() {
    const element = target();
    if (!element) return "";
    const html = renderProjectKanban(currentState(), { labels, translate });
    element.innerHTML = html;
    bindHost(element);
    return html;
  }

  function receiveState(state) {
    const agentId = String(state?.selectedAgentId || "");
    if (agentId !== observedAgentId) {
      observedAgentId = agentId;
      editingTaskId = "";
    }
    render();
  }

  function focusCreate({ behavior = "smooth" } = {}) {
    if (!currentState().selectedAgentId) return false;
    const element = target();
    if (!element?.querySelector) return false;
    let input = element.querySelector("[data-kanban-create-text]");
    if (!input) {
      render();
      input = element.querySelector("[data-kanban-create-text]");
    }
    if (!input || input.disabled) return false;
    input.scrollIntoView?.({ block: "center", behavior });
    input.focus?.();
    return true;
  }

  async function createTask(input) {
    return invoke("createTask", input);
  }

  async function changeStatus(taskId, status) {
    const task = taskById(taskId);
    if (!task || !projectKanbanStatuses.includes(status) || task.status === status) return false;
    return invoke("updateTask", task.id, { text: task.text, status, protected: task.protected });
  }

  async function saveTask(taskId, input) {
    const task = taskById(taskId);
    if (!task) return false;
    const updated = await invoke("updateTask", task.id, {
      text: String(input?.text ?? task.text),
      status: projectKanbanStatuses.includes(input?.status) ? input.status : task.status,
      protected: Boolean(input?.protected),
    });
    if (updated) editingTaskId = "";
    render();
    return updated;
  }

  async function deleteTask(taskId) {
    const deleted = await invoke("deleteTask", taskId);
    if (deleted && editingTaskId === taskId) editingTaskId = "";
    render();
    return deleted;
  }

  function startEdit(taskId) {
    if (!taskById(taskId) || !boardController) return false;
    editingTaskId = taskId;
    render();
    return true;
  }

  function cancelEdit() {
    editingTaskId = "";
    render();
  }

  function bindHost(element = target()) {
    if (!element?.querySelector) return;
    element.querySelector("[data-kanban-create]")?.addEventListener("submit", (event) => {
      event.preventDefault();
      createTask({
        text: element.querySelector("[data-kanban-create-text]")?.value || "",
        status: element.querySelector("[data-kanban-create-status]")?.value || "todo",
        protected: Boolean(element.querySelector("[data-kanban-create-protected]")?.checked),
      });
    });
    element.querySelectorAll("[data-project-kanban-task]").forEach((card) => {
      const taskId = card.dataset.projectKanbanTask;
      card.querySelector("[data-kanban-move]")?.addEventListener("change", (event) => changeStatus(taskId, event.currentTarget.value));
      card.querySelector("[data-kanban-edit]")?.addEventListener("click", () => startEdit(taskId));
      card.querySelector("[data-kanban-cancel]")?.addEventListener("click", cancelEdit);
      card.querySelector("[data-kanban-save]")?.addEventListener("click", () => saveTask(taskId, {
        text: card.querySelector("[data-kanban-task-text]")?.value || "",
        protected: Boolean(card.querySelector("[data-kanban-task-protected]")?.checked),
      }));
      card.querySelector("[data-kanban-delete]")?.addEventListener("click", () => deleteTask(taskId));
    });
  }

  function setAgent(agent) {
    editingTaskId = "";
    observedAgentId = String(agent?.id || "");
    if (!boardController || typeof boardController.setAgent !== "function") return reportUnavailable();
    boardController.setAgent(agent);
    return true;
  }

  function bind() {
    unsubscribe?.();
    observedAgentId = String(readBoardState()?.selectedAgentId || "");
    if (boardController?.subscribe) unsubscribe = boardController.subscribe(receiveState);
    else if (typeof boardState?.subscribe === "function") unsubscribe = boardState.subscribe(receiveState);
    else render();
    return destroy;
  }

  function destroy() {
    unsubscribe?.();
    unsubscribe = null;
    editingTaskId = "";
  }

  void showToast;
  return { bind, cancelEdit, changeStatus, createTask, deleteTask, destroy, focusCreate, getState: currentState, render, saveTask, setAgent, startEdit };
}
