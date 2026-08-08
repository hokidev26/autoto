import { objectValue } from "./value-coercion.mjs";
import { escapeAttr, escapeHtml } from "./dom.mjs";
import { confirm as platformConfirm } from "./platform.mjs";

export const taskWorkspaceStatuses = Object.freeze(["todo", "doing", "blocked", "done"]);
export const taskWorkspaceScopes = Object.freeze(["dispatch", "project", "agent"]);

const fallbackLabels = Object.freeze({
  scopes: { dispatch: "调度", project: "项目", agent: "Agent" },
  dispatchTitle: "跨项目任务调度",
  dispatchDescription: "集中查看任务、Agent 负载与阻塞情况。",
  projectTitle: "项目任务",
  projectDescription: "查看当前项目的任务与 Agent 分配。",
  loading: "正在加载任务工作区…",
  loadFailed: "任务工作区加载失败",
  noProjects: "暂无可调度项目",
  noProjectsDescription: "请先创建项目与 Agent。",
  noTasks: "当前筛选条件下没有任务",
  noAgents: "此项目暂无可分配 Agent",
  allProjects: "全部项目",
  allAgents: "全部 Agent",
  activeStatuses: "活动任务",
  allStatuses: "全部状态",
  refresh: "刷新",
  newAssignment: "分配新任务",
  taskText: "任务内容",
  chooseProject: "选择项目",
  chooseAgent: "选择 Agent",
  protected: "受保护",
  assign: "分配",
  assigning: "正在分配…",
  filters: "任务筛选",
  projects: "项目",
  agents: "Agent",
  tasks: "任务",
  active: "活动",
  blocked: "阻塞",
  done: "完成",
  projectPath: "项目路径",
  model: "模型",
  source: "来源",
  updatedAt: "更新时间",
  openAgent: "打开 Agent",
  taskDetails: "任务详情",
  closeDetails: "关闭任务详情",
  changeStatus: "更新状态",
  reassign: "重新分配",
  reassigning: "正在重新分配…",
  selectProjectFirst: "请先选择项目",
  selectAgentFirst: "请先选择 Agent",
  assignmentCreated: "任务已分配",
  taskUpdated: "任务状态已更新",
  taskReassigned: "任务已重新分配",
  protectedConfirm: "此任务受保护。确认继续操作？",
  protectedConfirmAgain: "再次确认：受保护任务的修改会被记录。仍要继续？",
  statuses: { todo: "待办", doing: "进行中", blocked: "已阻塞", done: "已完成" },
});

function text(value, limit = 16000) {
  return String(value ?? "").trim().slice(0, limit);
}

function integer(value) {
  const number = Number(value);
  return Number.isSafeInteger(number) && number >= 0 ? number : 0;
}

function labelAt(labels, key) {
  return key.split(".").reduce((value, part) => objectValue(value)[part], labels);
}

function format(value, params = {}) {
  return String(value ?? "").replace(/\{([\w-]+)\}/g, (_, key) => String(params[key] ?? ""));
}

function createLabels(labels, translate) {
  return (key, params = {}) => {
    const fallback = labelAt(fallbackLabels, key);
    let value = labelAt(labels, key);
    if (value === undefined && typeof translate === "function") {
      const translationKey = `taskWorkspace.${key}`;
      const translated = translate(translationKey, params);
      if (translated !== translationKey) value = translated;
    }
    if (value === undefined || value === null || value === "") value = fallback;
    return format(value, params);
  };
}

function emptyCounts() {
  return { todo: 0, doing: 0, blocked: 0, done: 0, total: 0 };
}

function normalizeCounts(value = {}) {
  const source = objectValue(value);
  const counts = emptyCounts();
  taskWorkspaceStatuses.forEach((status) => { counts[status] = integer(source[status]); });
  counts.total = integer(source.total) || taskWorkspaceStatuses.reduce((sum, status) => sum + counts[status], 0);
  return counts;
}

function countsFromTasks(tasks) {
  const counts = emptyCounts();
  tasks.forEach((task) => {
    if (taskWorkspaceStatuses.includes(task.status)) counts[task.status] += 1;
  });
  counts.total = taskWorkspaceStatuses.reduce((sum, status) => sum + counts[status], 0);
  return counts;
}

function addCounts(target, source) {
  taskWorkspaceStatuses.forEach((status) => { target[status] += integer(source?.[status]); });
  target.total = taskWorkspaceStatuses.reduce((sum, status) => sum + target[status], 0);
  return target;
}

function normalizeTask(value = {}, agent = {}, project = {}) {
  const source = objectValue(value);
  const status = text(source.status, 24);
  const agentId = text(source.agentId || agent.id, 160);
  return {
    id: text(source.id, 160),
    agentId,
    agentTitle: text(source.agentTitle || agent.title || agentId, 200),
    projectId: text(source.projectId || project.id, 160),
    projectName: text(source.projectName || project.name, 200),
    text: text(source.text),
    status: taskWorkspaceStatuses.includes(status) ? status : "todo",
    protected: Boolean(source.protected),
    position: integer(source.position),
    revision: Math.max(1, integer(source.revision) || 1),
    sourceType: text(source.sourceType, 40),
    updatedAt: text(source.updatedAt, 80),
  };
}

function normalizeAgent(value = {}, project = {}) {
  const source = objectValue(value);
  const id = text(source.id || source.agentId, 160);
  const agent = {
    id,
    title: text(source.title || source.agentTitle || id, 200),
    type: text(source.type || source.agentType, 80),
    status: text(source.status || source.agentStatus, 80) || "idle",
    model: text(source.model, 256),
    worklineId: text(source.worklineId, 160),
    worklineTitle: text(source.worklineTitle, 200),
    worklineBranch: text(source.worklineBranch, 256),
    projectId: text(source.projectId || project.id, 160),
    projectName: text(source.projectName || project.name, 200),
  };
  agent.tasks = (Array.isArray(source.tasks) ? source.tasks : []).map((task) => normalizeTask(task, agent, project)).filter((task) => task.id);
  agent.counts = normalizeCounts(source.counts);
  if (!agent.counts.total && agent.tasks.length) agent.counts = countsFromTasks(agent.tasks);
  return agent;
}

function normalizeProject(value = {}) {
  const source = objectValue(value);
  const id = text(source.id || source.projectId, 160);
  const project = {
    id,
    name: text(source.name || source.projectName || id, 200),
    gitPath: text(source.gitPath || source.projectPath, 2000),
    status: text(source.status, 80) || "active",
    updatedAt: text(source.updatedAt, 80),
  };
  project.agents = (Array.isArray(source.agents) ? source.agents : []).map((agent) => normalizeAgent(agent, project)).filter((agent) => agent.id);
  project.counts = normalizeCounts(source.counts);
  if (!project.counts.total) {
    project.counts = emptyCounts();
    project.agents.forEach((agent) => addCounts(project.counts, agent.counts));
  }
  return project;
}

export function normalizeTaskWorkspace(value = {}) {
  const source = objectValue(value);
  const projects = (Array.isArray(source.projects) ? source.projects : []).map(normalizeProject).filter((project) => project.id);
  const summary = normalizeCounts(source.summary);
  if (!summary.total) projects.forEach((project) => addCounts(summary, project.counts));
  summary.projectCount = integer(source.summary?.projectCount) || projects.length;
  summary.agentCount = integer(source.summary?.agentCount) || projects.reduce((sum, project) => sum + project.agents.length, 0);
  return { projects, summary };
}

export function flattenTaskWorkspace(value = {}) {
  const workspace = normalizeTaskWorkspace(value);
  return workspace.projects.flatMap((project) => project.agents.flatMap((agent) => agent.tasks.map((task) => ({ ...task, projectId: project.id, projectName: project.name, agentId: agent.id, agentTitle: agent.title }))));
}

function taskKey(task) {
  return `${text(task?.agentId, 160)}::${text(task?.id, 160)}`;
}

function activeCount(counts) {
  return integer(counts?.todo) + integer(counts?.doing) + integer(counts?.blocked);
}

function statusOptions(selected, label, { includeAll = false, activeOnly = false } = {}) {
  const prefix = includeAll
    ? `<option value="${activeOnly ? "active" : ""}">${escapeHtml(label(activeOnly ? "activeStatuses" : "allStatuses"))}</option>`
    : "";
  return prefix + taskWorkspaceStatuses.map((status) => `<option value="${status}" ${status === selected ? "selected" : ""}>${escapeHtml(label(`statuses.${status}`))}</option>`).join("");
}

function summaryCards(summary, label) {
  const cards = [
    [label("projects"), summary.projectCount],
    [label("agents"), summary.agentCount],
    [label("active"), activeCount(summary)],
    [label("blocked"), summary.blocked],
  ];
  return `<div class="task-workspace-summary">${cards.map(([name, value]) => `<div><strong>${escapeHtml(String(value || 0))}</strong><span>${escapeHtml(name)}</span></div>`).join("")}</div>`;
}

function assignmentForm(projects, state, label, fixedProjectId = "") {
  const selectedProjectId = fixedProjectId || state.assignmentProjectId || projects[0]?.id || "";
  const project = projects.find((item) => item.id === selectedProjectId) || projects[0] || null;
  const selectedAgentId = project?.agents.some((agent) => agent.id === state.assignmentAgentId) ? state.assignmentAgentId : project?.agents[0]?.id || "";
  return `<form class="task-assignment-form" data-task-assignment>
    <div class="task-assignment-copy"><strong>${escapeHtml(label("newAssignment"))}</strong><span>${escapeHtml(label("dispatchDescription"))}</span></div>
    ${fixedProjectId ? `<input type="hidden" data-task-assignment-project value="${escapeAttr(fixedProjectId)}" />` : `<select data-task-assignment-project aria-label="${escapeAttr(label("chooseProject"))}">
      ${projects.map((item) => `<option value="${escapeAttr(item.id)}" ${item.id === selectedProjectId ? "selected" : ""}>${escapeHtml(item.name)}</option>`).join("")}
    </select>`}
    <select data-task-assignment-agent aria-label="${escapeAttr(label("chooseAgent"))}" ${project?.agents.length ? "" : "disabled"}>
      ${(project?.agents || []).map((agent) => `<option value="${escapeAttr(agent.id)}" ${agent.id === selectedAgentId ? "selected" : ""}>${escapeHtml(agent.title)}</option>`).join("") || `<option value="">${escapeHtml(label("noAgents"))}</option>`}
    </select>
    <select data-task-assignment-status aria-label="${escapeAttr(label("changeStatus"))}">${statusOptions("todo", label)}</select>
    <textarea data-task-assignment-text rows="2" maxlength="16000" required placeholder="${escapeAttr(label("taskText"))}"></textarea>
    <label class="task-workspace-protected"><input data-task-assignment-protected type="checkbox" /> ${escapeHtml(label("protected"))}</label>
    <button type="submit" ${state.saving || !selectedAgentId ? "disabled" : ""}>${escapeHtml(state.saving ? label("assigning") : label("assign"))}</button>
  </form>`;
}

function taskRows(tasks, state, label, { showProject = true } = {}) {
  if (!tasks.length) return `<div class="task-workspace-empty">${escapeHtml(label("noTasks"))}</div>`;
  return `<div class="task-workspace-list" role="list">${tasks.map((task) => {
    const selected = state.selectedTaskKey === taskKey(task);
    return `<button class="task-workspace-row ${selected ? "selected" : ""}" type="button" role="listitem" data-task-workspace-task="${escapeAttr(taskKey(task))}">
      <span class="task-workspace-status status-${escapeAttr(task.status)}">${escapeHtml(label(`statuses.${task.status}`))}</span>
      <span class="task-workspace-row-main"><strong>${escapeHtml(task.text)}</strong><small>${escapeHtml([showProject ? task.projectName : "", task.agentTitle, task.sourceType].filter(Boolean).join(" · "))}</small></span>
      ${task.protected ? `<span class="task-workspace-lock">${escapeHtml(label("protected"))}</span>` : ""}
    </button>`;
  }).join("")}</div>`;
}

function filteredTasks(workspace, state) {
  let tasks = flattenTaskWorkspace(workspace);
  if (state.filterProjectId) tasks = tasks.filter((task) => task.projectId === state.filterProjectId);
  if (state.filterAgentId) tasks = tasks.filter((task) => task.agentId === state.filterAgentId);
  if (state.filterStatus === "active" || !state.filterStatus) tasks = tasks.filter((task) => task.status !== "done");
  else if (taskWorkspaceStatuses.includes(state.filterStatus)) tasks = tasks.filter((task) => task.status === state.filterStatus);
  const rank = { blocked: 0, doing: 1, todo: 2, done: 3 };
  return tasks.sort((left, right) => rank[left.status] - rank[right.status] || String(right.updatedAt).localeCompare(String(left.updatedAt)) || left.text.localeCompare(right.text));
}

function dispatchView(workspace, state, label) {
  const projects = workspace.projects;
  const agents = projects.flatMap((project) => project.agents.map((agent) => ({ ...agent, projectName: project.name })));
  const tasks = filteredTasks(workspace, state);
  return `<section class="task-workspace-view task-dispatch-view">
    <header class="task-workspace-hero"><div><span>${escapeHtml(label("scopes.dispatch"))}</span><h2>${escapeHtml(label("dispatchTitle"))}</h2><p>${escapeHtml(label("dispatchDescription"))}</p></div><button type="button" data-task-workspace-refresh>${escapeHtml(label("refresh"))}</button></header>
    ${summaryCards(workspace.summary, label)}
    ${projects.length ? assignmentForm(projects, state, label) : `<div class="task-workspace-empty"><strong>${escapeHtml(label("noProjects"))}</strong><span>${escapeHtml(label("noProjectsDescription"))}</span></div>`}
    <div class="task-workspace-filterbar" aria-label="${escapeAttr(label("filters"))}">
      <select data-task-filter-project><option value="">${escapeHtml(label("allProjects"))}</option>${projects.map((project) => `<option value="${escapeAttr(project.id)}" ${project.id === state.filterProjectId ? "selected" : ""}>${escapeHtml(project.name)}</option>`).join("")}</select>
      <select data-task-filter-agent><option value="">${escapeHtml(label("allAgents"))}</option>${agents.filter((agent) => !state.filterProjectId || agent.projectId === state.filterProjectId).map((agent) => `<option value="${escapeAttr(agent.id)}" ${agent.id === state.filterAgentId ? "selected" : ""}>${escapeHtml(`${agent.projectName} · ${agent.title}`)}</option>`).join("")}</select>
      <select data-task-filter-status>${statusOptions(state.filterStatus || "active", label, { includeAll: true, activeOnly: true })}</select>
    </div>
    ${taskRows(tasks, state, label)}
  </section>`;
}

function agentCards(project, label) {
  if (!project.agents.length) return `<div class="task-workspace-empty">${escapeHtml(label("noAgents"))}</div>`;
  return `<div class="task-workspace-agent-grid">${project.agents.map((agent) => `<button type="button" class="task-workspace-agent-card" data-task-workspace-agent="${escapeAttr(agent.id)}">
    <span class="task-workspace-agent-status status-${escapeAttr(agent.status)}"></span>
    <span><strong>${escapeHtml(agent.title)}</strong><small>${escapeHtml([agent.worklineTitle || agent.worklineBranch, agent.model].filter(Boolean).join(" · "))}</small></span>
    <span class="task-workspace-agent-load"><strong>${escapeHtml(String(activeCount(agent.counts)))}</strong><small>${escapeHtml(label("active"))}</small></span>
    ${agent.counts.blocked ? `<span class="task-workspace-agent-blocked">${escapeHtml(`${agent.counts.blocked} ${label("blocked")}`)}</span>` : ""}
  </button>`).join("")}</div>`;
}

function projectView(workspace, state, label) {
  const project = workspace.projects.find((item) => item.id === state.projectId) || null;
  if (!project) return `<section class="task-workspace-view"><div class="task-workspace-empty"><strong>${escapeHtml(label("selectProjectFirst"))}</strong><button type="button" data-task-workspace-scope-target="dispatch">${escapeHtml(label("scopes.dispatch"))}</button></div></section>`;
  const tasks = project.agents.flatMap((agent) => agent.tasks.map((task) => ({ ...task, projectId: project.id, projectName: project.name, agentId: agent.id, agentTitle: agent.title }))).sort((left, right) => left.position - right.position || left.text.localeCompare(right.text));
  const projectSummary = { ...project.counts, projectCount: 1, agentCount: project.agents.length };
  return `<section class="task-workspace-view task-project-view">
    <header class="task-workspace-hero"><div><span>${escapeHtml(label("scopes.project"))}</span><h2>${escapeHtml(project.name)}</h2><p>${escapeHtml(project.gitPath || label("projectDescription"))}</p></div><button type="button" data-task-workspace-refresh>${escapeHtml(label("refresh"))}</button></header>
    ${summaryCards(projectSummary, label)}
    ${assignmentForm(workspace.projects, state, label, project.id)}
    <section class="task-workspace-section"><header><h3>${escapeHtml(label("agents"))}</h3><span>${escapeHtml(String(project.agents.length))}</span></header>${agentCards(project, label)}</section>
    <section class="task-workspace-section"><header><h3>${escapeHtml(label("tasks"))}</h3><span>${escapeHtml(String(tasks.length))}</span></header>${taskRows(tasks, state, label, { showProject: false })}</section>
  </section>`;
}

function inspector(workspace, state, label) {
  const task = flattenTaskWorkspace(workspace).find((item) => taskKey(item) === state.selectedTaskKey) || null;
  if (!task) return "";
  const project = workspace.projects.find((item) => item.id === task.projectId);
  const agents = project?.agents || [];
  return `<aside class="task-workspace-inspector" data-task-workspace-inspector aria-label="${escapeAttr(label("taskDetails"))}">
    <header><div><span>${escapeHtml(label("taskDetails"))}</span><strong>${escapeHtml(task.projectName)}</strong></div><button type="button" data-task-inspector-close aria-label="${escapeAttr(label("closeDetails"))}">×</button></header>
    <div class="task-workspace-inspector-body">
      <p>${escapeHtml(task.text)}</p>
      <dl>
        <div><dt>${escapeHtml(label("projects"))}</dt><dd>${escapeHtml(task.projectName)}</dd></div>
        <div><dt>${escapeHtml(label("agents"))}</dt><dd>${escapeHtml(task.agentTitle)}</dd></div>
        <div><dt>${escapeHtml(label("source"))}</dt><dd>${escapeHtml(task.sourceType || "—")}</dd></div>
        <div><dt>${escapeHtml(label("updatedAt"))}</dt><dd>${escapeHtml(task.updatedAt || "—")}</dd></div>
      </dl>
      <section><label>${escapeHtml(label("changeStatus"))}<select data-task-inspector-status>${statusOptions(task.status, label)}</select></label><button type="button" data-task-inspector-update ${state.saving ? "disabled" : ""}>${escapeHtml(label("changeStatus"))}</button></section>
      <section><label>${escapeHtml(label("chooseAgent"))}<select data-task-inspector-agent>${agents.map((agent) => `<option value="${escapeAttr(agent.id)}" ${agent.id === task.agentId ? "selected" : ""}>${escapeHtml(agent.title)}</option>`).join("")}</select></label><button type="button" data-task-inspector-assign ${state.saving || agents.length < 2 ? "disabled" : ""}>${escapeHtml(state.saving ? label("reassigning") : label("reassign"))}</button></section>
    </div>
    <footer><button type="button" data-task-inspector-open-agent>${escapeHtml(label("openAgent"))}</button></footer>
  </aside>`;
}

export function renderTaskWorkspace(value = {}, { labels, translate } = {}) {
  const state = objectValue(value);
  const label = createLabels(labels, translate);
  const workspace = normalizeTaskWorkspace(state.workspace);
  if (state.loading && !state.loaded) return `<div class="task-workspace-loading" aria-busy="true">${escapeHtml(label("loading"))}</div>`;
  if (state.error && !state.loaded) return `<div class="task-workspace-error" role="alert"><strong>${escapeHtml(label("loadFailed"))}</strong><span>${escapeHtml(state.error)}</span></div>`;
  const view = state.scope === "project" ? projectView(workspace, state, label) : dispatchView(workspace, state, label);
  return `${state.error ? `<div class="task-workspace-error compact" role="alert">${escapeHtml(state.error)}</div>` : ""}${view}${inspector(workspace, state, label)}`;
}

export function createTaskWorkspaceController({
  request,
  host,
  kanbanHost,
  scopeHost,
  labels,
  translate,
  showError,
  showToast,
  confirmAction,
  onChange,
  onOpenProject,
  onOpenAgent,
  document: documentImpl = globalThis.document,
} = {}) {
  if (typeof request !== "function") throw new TypeError("Task workspace request must be a function");
  const confirm = confirmAction || platformConfirm;
  const state = {
    workspace: normalizeTaskWorkspace({}),
    scope: "dispatch",
    projectId: "",
    agentId: "",
    assignmentProjectId: "",
    assignmentAgentId: "",
    filterProjectId: "",
    filterAgentId: "",
    filterStatus: "active",
    selectedTaskKey: "",
    loading: false,
    loaded: false,
    saving: false,
    error: "",
    seq: 0,
  };
  const subscribers = new Set();
  const target = () => typeof host === "string" ? documentImpl?.querySelector?.(host) : host;
  const kanban = () => typeof kanbanHost === "string" ? documentImpl?.querySelector?.(kanbanHost) : kanbanHost;
  const scopeRoot = () => typeof scopeHost === "string" ? documentImpl?.querySelector?.(scopeHost) : scopeHost;
  const label = createLabels(labels, translate);

  function snapshot() {
    return { ...state, workspace: normalizeTaskWorkspace(state.workspace) };
  }

  function emit() {
    const value = snapshot();
    onChange?.(value);
    subscribers.forEach((subscriber) => subscriber(value));
    render();
  }

  function syncScopes() {
    scopeRoot()?.querySelectorAll?.("[data-task-workspace-scope]").forEach((button) => {
      const scope = button.dataset.taskWorkspaceScope;
      const disabled = scope === "project" && !state.projectId || scope === "agent" && !state.agentId;
      button.disabled = disabled;
      button.classList.toggle("active", scope === state.scope);
      button.setAttribute("aria-pressed", scope === state.scope ? "true" : "false");
    });
  }

  function render() {
    const element = target();
    const board = kanban();
    const agentScope = state.scope === "agent";
    element?.classList.toggle("hidden", agentScope);
    board?.classList.toggle("hidden", !agentScope);
    if (element && !agentScope) {
      element.innerHTML = renderTaskWorkspace(snapshot(), { labels, translate });
      bindHost(element);
    }
    syncScopes();
    return element?.innerHTML || "";
  }

  function setContext({ projectId = state.projectId, agentId = state.agentId, scope } = {}) {
    state.projectId = text(projectId, 160);
    state.agentId = text(agentId, 160);
    if (scope) setScope(scope, { emitChange: false });
    else if (state.scope === "project" && !state.projectId || state.scope === "agent" && !state.agentId) state.scope = "dispatch";
    emit();
    return snapshot();
  }

  function setScope(scope, { emitChange = true } = {}) {
    const normalized = taskWorkspaceScopes.includes(scope) ? scope : "dispatch";
    if (normalized === "project" && !state.projectId || normalized === "agent" && !state.agentId) return false;
    state.scope = normalized;
    state.selectedTaskKey = "";
    if (emitChange) emit();
    return true;
  }

  async function load({ silent = false } = {}) {
    const seq = ++state.seq;
    state.loading = true;
    state.error = "";
    if (!silent) emit();
    try {
      const result = await request("/api/task-workspace");
      if (seq !== state.seq) return false;
      state.workspace = normalizeTaskWorkspace(result);
      state.loaded = true;
      const projects = state.workspace.projects;
      if (state.projectId && !projects.some((project) => project.id === state.projectId)) state.projectId = "";
      if (state.agentId && !projects.some((project) => project.agents.some((agent) => agent.id === state.agentId))) state.agentId = "";
      if (!state.assignmentProjectId) state.assignmentProjectId = state.projectId || projects[0]?.id || "";
      if (state.scope === "project" && !state.projectId || state.scope === "agent" && !state.agentId) state.scope = "dispatch";
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

  function projectById(projectId) {
    return state.workspace.projects.find((project) => project.id === projectId) || null;
  }

  function agentById(agentId) {
    for (const project of state.workspace.projects) {
      const agent = project.agents.find((item) => item.id === agentId);
      if (agent) return { project, agent };
    }
    return null;
  }

  function selectedTask() {
    return flattenTaskWorkspace(state.workspace).find((task) => taskKey(task) === state.selectedTaskKey) || null;
  }

  function selectTask(agentId, taskId) {
    const key = `${text(agentId, 160)}::${text(taskId, 160)}`;
    if (!flattenTaskWorkspace(state.workspace).some((task) => taskKey(task) === key)) return false;
    state.selectedTaskKey = key;
    emit();
    return true;
  }

  async function protectedAcknowledgement(task) {
    if (!task?.protected) return false;
    if (!await confirm(label("protectedConfirm"))) return null;
    if (!await confirm(label("protectedConfirmAgain"))) return null;
    return true;
  }

  async function createTask(input = {}) {
    const agentId = text(input.agentId || state.assignmentAgentId, 160);
    const content = text(input.text);
    if (!agentId || !content || state.saving) return false;
    state.saving = true;
    emit();
    try {
      await request(`/api/agents/${encodeURIComponent(agentId)}/spec/tasks`, {
        method: "POST",
        body: JSON.stringify({ text: content, status: taskWorkspaceStatuses.includes(input.status) ? input.status : "todo", protected: Boolean(input.protected) }),
      });
      showToast?.(label("assignmentCreated"), "success", { force: true });
      await load({ silent: true });
      return true;
    } catch (error) {
      state.error = error?.message || String(error);
      showError?.(error);
      return false;
    } finally {
      state.saving = false;
      emit();
    }
  }

  async function updateSelectedTask(status) {
    const task = selectedTask();
    if (!task || !taskWorkspaceStatuses.includes(status) || status === task.status || state.saving) return false;
    const acknowledgeProtected = await protectedAcknowledgement(task);
    if (acknowledgeProtected === null) return false;
    state.saving = true;
    emit();
    try {
      await request(`/api/agents/${encodeURIComponent(task.agentId)}/spec/tasks/${encodeURIComponent(task.id)}`, {
        method: "PATCH",
        body: JSON.stringify({ text: task.text, status, protected: task.protected, expectedRevision: task.revision, acknowledgeProtected }),
      });
      showToast?.(label("taskUpdated"), "success", { force: true });
      await load({ silent: true });
      return true;
    } catch (error) {
      state.error = error?.message || String(error);
      showError?.(error);
      return false;
    } finally {
      state.saving = false;
      emit();
    }
  }

  async function reassignSelectedTask(targetAgentId) {
    const task = selectedTask();
    const target = agentById(targetAgentId);
    if (!task || !target || target.project.id !== task.projectId || target.agent.id === task.agentId || state.saving) return false;
    const acknowledgeProtected = await protectedAcknowledgement(task);
    if (acknowledgeProtected === null) return false;
    state.saving = true;
    emit();
    try {
      await request(`/api/agents/${encodeURIComponent(task.agentId)}/spec/tasks/${encodeURIComponent(task.id)}/assign`, {
        method: "POST",
        body: JSON.stringify({ targetAgentId: target.agent.id, expectedRevision: task.revision, acknowledgeProtected }),
      });
      state.selectedTaskKey = `${target.agent.id}::${task.id}`;
      showToast?.(label("taskReassigned"), "success", { force: true });
      await load({ silent: true });
      return true;
    } catch (error) {
      state.error = error?.message || String(error);
      showError?.(error);
      return false;
    } finally {
      state.saving = false;
      emit();
    }
  }

  function focusCreate({ behavior = "smooth" } = {}) {
    if (state.scope === "agent") return false;
    const input = target()?.querySelector?.("[data-task-assignment-text]");
    if (!input || input.disabled) return false;
    input.scrollIntoView?.({ block: "center", behavior });
    input.focus?.();
    return true;
  }

  function bindHost(element = target()) {
    if (!element?.querySelector) return;
    element.querySelectorAll("[data-task-workspace-refresh]").forEach((button) => button.addEventListener("click", () => load()));
    element.querySelectorAll("[data-task-workspace-scope-target]").forEach((button) => button.addEventListener("click", () => setScope(button.dataset.taskWorkspaceScopeTarget)));
    element.querySelector("[data-task-filter-project]")?.addEventListener("change", (event) => {
      state.filterProjectId = event.currentTarget.value;
      state.filterAgentId = "";
      emit();
    });
    element.querySelector("[data-task-filter-agent]")?.addEventListener("change", (event) => { state.filterAgentId = event.currentTarget.value; emit(); });
    element.querySelector("[data-task-filter-status]")?.addEventListener("change", (event) => { state.filterStatus = event.currentTarget.value; emit(); });
    element.querySelector("[data-task-assignment-project]")?.addEventListener("change", (event) => {
      state.assignmentProjectId = event.currentTarget.value;
      state.assignmentAgentId = "";
      emit();
    });
    element.querySelector("[data-task-assignment-agent]")?.addEventListener("change", (event) => { state.assignmentAgentId = event.currentTarget.value; });
    element.querySelector("[data-task-assignment]")?.addEventListener("submit", (event) => {
      event.preventDefault();
      createTask({
        agentId: element.querySelector("[data-task-assignment-agent]")?.value,
        text: element.querySelector("[data-task-assignment-text]")?.value,
        status: element.querySelector("[data-task-assignment-status]")?.value,
        protected: element.querySelector("[data-task-assignment-protected]")?.checked,
      });
    });
    element.querySelectorAll("[data-task-workspace-task]").forEach((button) => button.addEventListener("click", () => { state.selectedTaskKey = button.dataset.taskWorkspaceTask; emit(); }));
    element.querySelectorAll("[data-task-workspace-agent]").forEach((button) => button.addEventListener("click", () => {
      const found = agentById(button.dataset.taskWorkspaceAgent);
      if (found) onOpenAgent?.(found.agent, found.project);
    }));
    element.querySelector("[data-task-inspector-close]")?.addEventListener("click", () => { state.selectedTaskKey = ""; emit(); });
    element.querySelector("[data-task-inspector-update]")?.addEventListener("click", () => updateSelectedTask(element.querySelector("[data-task-inspector-status]")?.value));
    element.querySelector("[data-task-inspector-assign]")?.addEventListener("click", () => reassignSelectedTask(element.querySelector("[data-task-inspector-agent]")?.value));
    element.querySelector("[data-task-inspector-open-agent]")?.addEventListener("click", () => {
      const task = selectedTask();
      const found = task ? agentById(task.agentId) : null;
      if (found) onOpenAgent?.(found.agent, found.project);
    });
  }

  function bind() {
    scopeRoot()?.querySelectorAll?.("[data-task-workspace-scope]").forEach((button) => button.addEventListener("click", () => setScope(button.dataset.taskWorkspaceScope)));
    render();
    return destroy;
  }

  function destroy() {
    subscribers.clear();
  }

  function subscribe(subscriber, { immediate = true } = {}) {
    if (typeof subscriber !== "function") throw new TypeError("Task workspace subscriber must be a function");
    subscribers.add(subscriber);
    if (immediate) subscriber(snapshot());
    return () => subscribers.delete(subscriber);
  }

  void onOpenProject;
  return { bind, createTask, destroy, focusCreate, getState: snapshot, load, reassignSelectedTask, render, selectTask, setContext, setScope, subscribe, updateSelectedTask };
}
