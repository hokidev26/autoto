import { objectValue } from "./value-coercion.mjs";
const APP_API_ROOT = "/app/api";
const APP_AUTH_ROOT = "/app/auth";
const MAX_LABEL_LENGTH = 240;
const MAX_MESSAGE_LENGTH = 12000;

export const oauthAppRoutes = Object.freeze({
  session: `${APP_AUTH_ROOT}/session`,
  login: `${APP_AUTH_ROOT}/login`,
  logout: `${APP_AUTH_ROOT}/logout`,
  me: `${APP_API_ROOT}/me`,
  projects: `${APP_API_ROOT}/projects`,
});

function firstArray(value, keys) {
  if (Array.isArray(value)) return value;
  const source = objectValue(value);
  for (const key of keys) {
    if (Array.isArray(source[key])) return source[key];
  }
  return [];
}

function boundedText(value, fallback = "", maxLength = MAX_LABEL_LENGTH) {
  const text = String(value ?? "").trim();
  const normalized = text || String(fallback ?? "");
  if (!Number.isSafeInteger(maxLength) || maxLength < 1 || normalized.length <= maxLength) return normalized;
  return `${normalized.slice(0, Math.max(1, maxLength - 1))}…`;
}

/** Returns plain display text. Renderers must assign it with textContent, never innerHTML. */
export function toDisplayText(value, fallback = "", maxLength = MAX_MESSAGE_LENGTH) {
  return boundedText(value, fallback, maxLength);
}

/** Assigns untrusted values through the safe DOM text sink. */
export function setText(node, value, fallback = "", maxLength = MAX_MESSAGE_LENGTH) {
  if (!node) return "";
  const text = toDisplayText(value, fallback, maxLength);
  node.textContent = text;
  return text;
}

function encodedSegment(value, name) {
  const segment = String(value ?? "").trim();
  if (!segment) throw new TypeError(`${name} is required`);
  return encodeURIComponent(segment);
}

export function buildAppApiPath(resource, { projectId, agentId, cursor, limit } = {}) {
  if (resource === "me") return oauthAppRoutes.me;
  if (resource === "projects") return oauthAppRoutes.projects;
  const project = encodedSegment(projectId, "projectId");
  if (resource === "agents") return `${APP_API_ROOT}/projects/${project}/agents`;
  const agent = encodedSegment(agentId, "agentId");
  const base = `${APP_API_ROOT}/projects/${project}/agents/${agent}`;
  if (resource === "agent") return base;
  if (resource === "messages") {
    const params = new URLSearchParams();
    if (String(cursor ?? "").trim()) params.set("cursor", String(cursor));
    const numericLimit = Number(limit);
    if (Number.isSafeInteger(numericLimit) && numericLimit > 0) params.set("limit", String(Math.min(numericLimit, 200)));
    const query = params.toString();
    return `${base}/messages${query ? `?${query}` : ""}`;
  }
  throw new TypeError(`Unknown app API resource: ${resource}`);
}

export function buildAuthPath(action, { returnTo } = {}) {
  if (action === "session") return oauthAppRoutes.session;
  if (action === "logout") return oauthAppRoutes.logout;
  if (action !== "login") throw new TypeError(`Unknown auth action: ${action}`);
  const target = String(returnTo ?? "").trim();
  if (!target || !target.startsWith("/") || target.startsWith("//")) return oauthAppRoutes.login;
  const params = new URLSearchParams({ returnTo: target });
  return `${oauthAppRoutes.login}?${params.toString()}`;
}

export function mapCurrentUser(value) {
  const root = objectValue(value);
  const source = objectValue(root.user || root.currentUser || root.profile || root);
  const id = boundedText(source.id ?? source.userId ?? source.sub, "", MAX_LABEL_LENGTH);
  const displayName = boundedText(source.displayName ?? source.name ?? source.handle ?? source.email, id || "已登录", MAX_LABEL_LENGTH);
  const handle = boundedText(source.handle ?? source.username ?? source.email, "", MAX_LABEL_LENGTH);
  return { id, displayName, handle };
}

export function mapProjects(value) {
  return firstArray(value, ["projects", "items", "data"]).map((entry, index) => {
    const source = objectValue(entry);
    const id = boundedText(source.id ?? source.projectId ?? source.key, "", MAX_LABEL_LENGTH);
    if (!id) return null;
    return {
      id,
      name: boundedText(source.name ?? source.title ?? source.path, `项目 ${index + 1}`, MAX_LABEL_LENGTH),
      role: boundedText(source.role, "", MAX_LABEL_LENGTH),
      archived: source.archived === true,
      pinned: source.pinned === true,
    };
  }).filter(Boolean);
}

export function mapAgents(value) {
  return firstArray(value, ["agents", "items", "data"]).map((entry, index) => {
    const source = objectValue(entry);
    const id = boundedText(source.id ?? source.agentId, "", MAX_LABEL_LENGTH);
    if (!id) return null;
    return {
      id,
      projectId: boundedText(source.projectId, "", MAX_LABEL_LENGTH),
      name: boundedText(source.name ?? source.title, `Agent ${index + 1}`, MAX_LABEL_LENGTH),
      type: boundedText(source.type ?? source.kind, "", MAX_LABEL_LENGTH),
      status: boundedText(source.status ?? source.state, "", MAX_LABEL_LENGTH),
    };
  }).filter(Boolean);
}

function messageText(source) {
  const direct = source.contentText ?? source.text ?? source.message ?? source.content;
  if (Array.isArray(direct)) {
    return direct.map((part) => {
      if (typeof part === "string") return part;
      const item = objectValue(part);
      return item.text ?? item.content ?? "";
    }).filter(Boolean).join("\n");
  }
  if (direct && typeof direct === "object") return objectValue(direct).text ?? objectValue(direct).content ?? "";
  return direct;
}

export function mapMessages(value) {
  return firstArray(value, ["messages", "items", "data"]).map((entry, index) => {
    const source = objectValue(entry);
    return {
      id: boundedText(source.id ?? source.messageId, `message-${index + 1}`, MAX_LABEL_LENGTH),
      role: boundedText(source.role ?? source.type, "unknown", 40).toLowerCase(),
      content: toDisplayText(messageText(source), "", MAX_MESSAGE_LENGTH),
      createdAt: boundedText(source.createdAt ?? source.timestamp ?? source.time, "", MAX_LABEL_LENGTH),
      completionState: boundedText(source.completionState ?? source.status, "", MAX_LABEL_LENGTH),
    };
  });
}

function capabilitySource(value) {
  const source = objectValue(value);
  return objectValue(source.capabilities || source.features || source.permissions || source);
}

/** Enables submission only after an explicit server capability grant. */
export function normalizeReadOnlySubmissionCapability(...values) {
  for (const value of values) {
    const root = objectValue(value);
    const source = capabilitySource(root);
    const allowed = source.readOnlyMessageSubmitAllowed === true
      || source.readOnlyTaskSubmission === true
      || source.submitReadOnlyTasks === true;
    if (!allowed) continue;
    const mode = boundedText(source.maxPermissionMode ?? root.maxPermissionMode, "", 40).toLowerCase().replace(/[-_]/g, "");
    if (mode && mode !== "readonly") continue;
    return true;
  }
  return false;
}

export function submissionControlState({ capability, authenticated, projectId, agentId, busy } = {}) {
  const visible = capability === true;
  let reason = "";
  if (!visible) reason = "服务端未启用只读任务提交";
  else if (!authenticated) reason = "请先登录";
  else if (!projectId || !agentId) reason = "请先选择项目和 Agent";
  else if (busy) reason = "正在提交";
  return { visible, disabled: !visible || Boolean(reason), reason };
}

function errorParts(body) {
  if (typeof body === "string") return { code: "", message: body };
  const source = objectValue(body);
  const nested = objectValue(source.error);
  return {
    code: boundedText(source.code ?? nested.code ?? source.errorCode, "", 100).toLowerCase(),
    message: boundedText(nested.message ?? source.message ?? (typeof source.error === "string" ? source.error : ""), "", 1000),
  };
}

function headerValue(headers, name) {
  if (typeof headers?.get === "function") return headers.get(name) || "";
  const source = objectValue(headers);
  const target = name.toLowerCase();
  const key = Object.keys(source).find((entry) => entry.toLowerCase() === target);
  return key ? String(source[key] ?? "") : "";
}

export function isOIDCDisabledError(value) {
  const source = objectValue(value);
  const { code, message } = errorParts(source.body ?? source);
  const combined = `${code} ${message}`.toLowerCase();
  return /(oidc|oauth).*(disabled|not.?configured|unavailable)|auth.*disabled/.test(combined);
}

export function normalizeAppError({ status = 0, statusText = "", body = null, headers = {}, path = "" } = {}) {
  const numericStatus = Number(status) || 0;
  const parts = errorParts(body);
  const retryAfter = boundedText(headerValue(headers, "retry-after"), "", 100);
  if (isOIDCDisabledError({ body })) {
    return { kind: "oidc_disabled", status: numericStatus, code: parts.code, path, retryAfter: "", message: "OIDC 登录尚未启用或配置不完整，请联系服务管理员。" };
  }
  if (numericStatus === 401) return { kind: "unauthenticated", status: 401, code: parts.code, path, retryAfter: "", message: "登录已失效，请重新登录。" };
  if (numericStatus === 403) return { kind: "forbidden", status: 403, code: parts.code, path, retryAfter: "", message: "当前账户没有访问此资源的权限。" };
  if (numericStatus === 429) {
    const suffix = retryAfter ? ` 请在 ${retryAfter} 秒后重试。` : " 请稍后重试。";
    return { kind: "rate_limited", status: 429, code: parts.code, path, retryAfter, message: `请求过于频繁。${suffix}` };
  }
  if (numericStatus >= 500) return { kind: "server_error", status: numericStatus, code: parts.code, path, retryAfter: "", message: "服务暂时不可用，请稍后重试。" };
  if (numericStatus >= 400) return { kind: "request_error", status: numericStatus, code: parts.code, path, retryAfter: "", message: parts.message || `${numericStatus} ${statusText || "请求失败"}` };
  return { kind: "network_error", status: 0, code: parts.code, path, retryAfter: "", message: parts.message || "无法连接到服务，请检查网络后重试。" };
}

export function createInitialOAuthAppState() {
  return {
    phase: "loading",
    authenticated: false,
    user: null,
    capability: false,
    projects: [],
    agents: [],
    messages: [],
    selectedProjectId: "",
    selectedAgentId: "",
    busy: false,
    error: null,
  };
}

/** Pure state transition helper used by the browser controller and Node tests. */
export function transitionOAuthAppState(state, action = {}) {
  const current = { ...createInitialOAuthAppState(), ...objectValue(state) };
  switch (action.type) {
    case "loading":
      return { ...current, phase: "loading", error: null };
    case "authenticated":
      return { ...current, phase: "ready", authenticated: true, user: mapCurrentUser(action.payload), capability: normalizeReadOnlySubmissionCapability(action.payload), error: null };
    case "signed_out":
      return { ...createInitialOAuthAppState(), phase: "signed_out", error: action.error || null };
    case "failed":
      return { ...current, phase: action.error?.kind || "error", authenticated: action.error?.kind === "unauthenticated" ? false : current.authenticated, error: action.error || normalizeAppError() };
    case "projects_loaded":
      return { ...current, projects: mapProjects(action.payload), error: null };
    case "project_selected":
      return { ...current, selectedProjectId: String(action.projectId || ""), selectedAgentId: "", agents: [], messages: [], error: null };
    case "agents_loaded":
      return { ...current, agents: mapAgents(action.payload), error: null };
    case "agent_selected":
      return { ...current, selectedAgentId: String(action.agentId || ""), messages: [], error: null };
    case "messages_loaded":
      return { ...current, messages: mapMessages(action.payload), error: null };
    case "busy":
      return { ...current, busy: Boolean(action.value) };
    default:
      return current;
  }
}

export function buildReadOnlyMessageSubmission(projectId, agentId, content) {
  const text = toDisplayText(content, "", MAX_MESSAGE_LENGTH);
  if (!text) throw new TypeError("content is required");
  return {
    path: buildAppApiPath("messages", { projectId, agentId }),
    options: { method: "POST", body: JSON.stringify({ content: text }) },
  };
}

export function isAllowedAppRequestPath(path) {
  const value = String(path || "");
  return value === APP_API_ROOT || value.startsWith(`${APP_API_ROOT}/`) || value === APP_AUTH_ROOT || value.startsWith(`${APP_AUTH_ROOT}/`);
}

async function responseBody(response) {
  if (response.status === 204 || response.status === 205) return null;
  const text = await response.text();
  if (!text) return null;
  try { return JSON.parse(text); } catch { return text; }
}

/** Same-origin BFF transport. It never accepts or creates bearer/local-token headers. */
export async function requestAppJSON(path, options = {}, fetchImpl = globalThis.fetch) {
  if (!isAllowedAppRequestPath(path)) throw new TypeError("OAuth App requests must stay under /app/api/* or /app/auth/*");
  if (typeof fetchImpl !== "function") throw new TypeError("fetch is unavailable");
  const suppliedHeaders = objectValue(options.headers);
  for (const name of Object.keys(suppliedHeaders)) {
    if (/^(authorization|x-autoto-token|x-gateway-key)$/i.test(name)) throw new TypeError("Credential headers are not accepted by the OAuth App transport");
  }
  const headers = { Accept: "application/json", ...suppliedHeaders };
  if (options.body !== undefined && options.body !== null && !Object.keys(headers).some((name) => name.toLowerCase() === "content-type")) {
    headers["Content-Type"] = "application/json";
  }
  let response;
  try {
    response = await fetchImpl(path, { ...options, headers, credentials: "same-origin", referrerPolicy: "same-origin" });
  } catch (cause) {
    const error = normalizeAppError({ path, body: { message: cause?.message } });
    error.cause = cause;
    throw error;
  }
  const body = await responseBody(response);
  if (!response.ok) throw normalizeAppError({ status: response.status, statusText: response.statusText, body, headers: response.headers, path });
  return body;
}

function element(documentImpl, id) {
  return documentImpl.getElementById(id);
}

function createSelectionButton(documentImpl, { id, title, meta }, selected, onSelect) {
  const button = documentImpl.createElement("button");
  button.type = "button";
  button.className = "selection-item";
  button.dataset.id = id;
  button.setAttribute("aria-current", selected ? "true" : "false");
  const titleNode = documentImpl.createElement("span");
  titleNode.className = "selection-title";
  setText(titleNode, title, "未命名");
  const metaNode = documentImpl.createElement("span");
  metaNode.className = "selection-meta";
  setText(metaNode, meta);
  button.append(titleNode, metaNode);
  button.addEventListener("click", () => onSelect(id));
  return button;
}

function messageRoleLabel(role) {
  if (role === "assistant") return "Assistant";
  if (role === "user") return "User";
  if (role === "system") return "System";
  if (role === "tool") return "Tool";
  return role || "Unknown";
}

export function mountOAuthApp({ document: documentImpl = globalThis.document, location: locationImpl = globalThis.location, fetch: fetchImpl = globalThis.fetch } = {}) {
  if (!documentImpl) return () => {};
  const nodes = {
    currentUser: element(documentImpl, "currentUser"), login: element(documentImpl, "loginLink"), logout: element(documentImpl, "logoutButton"),
    status: element(documentImpl, "statusPanel"), statusTitle: element(documentImpl, "statusTitle"), statusMessage: element(documentImpl, "statusMessage"), retry: element(documentImpl, "retryButton"),
    workspace: element(documentImpl, "browserWorkspace"), projects: element(documentImpl, "projectsList"), projectsEmpty: element(documentImpl, "projectsEmpty"), refreshProjects: element(documentImpl, "refreshProjectsButton"),
    agents: element(documentImpl, "agentsList"), agentsHint: element(documentImpl, "agentsHint"), messages: element(documentImpl, "messagesList"), messagesHint: element(documentImpl, "messagesHint"), refreshMessages: element(documentImpl, "refreshMessagesButton"),
    taskForm: element(documentImpl, "taskForm"), taskContent: element(documentImpl, "taskContent"), taskHelp: element(documentImpl, "taskFormHelp"), submitTask: element(documentImpl, "submitTaskButton"),
  };
  let state = createInitialOAuthAppState();
  let destroyed = false;
  let projectRequest = 0;
  let agentRequest = 0;
  let messageRequest = 0;

  const setState = (action) => {
    state = transitionOAuthAppState(state, action);
    render();
  };

  function renderStatus() {
    const ready = state.phase === "ready";
    nodes.status.hidden = ready;
    nodes.workspace.hidden = !ready;
    nodes.retry.hidden = true;
    nodes.status.dataset.kind = "";
    if (ready) return;
    const content = {
      loading: ["正在连接", "正在从同源 BFF 获取会话信息。", ""],
      signed_out: ["需要登录", "使用组织账户登录后即可浏览获授权的项目与 Agent。", ""],
      oidc_disabled: ["OIDC 尚未启用", state.error?.message, "warning"],
      forbidden: ["没有访问权限", state.error?.message, "error"],
      rate_limited: ["请求受限", state.error?.message, "warning"],
      server_error: ["服务暂时不可用", state.error?.message, "error"],
      network_error: ["无法连接", state.error?.message, "error"],
      request_error: ["请求失败", state.error?.message, "error"],
      error: ["请求失败", state.error?.message, "error"],
    }[state.phase] || ["请求失败", state.error?.message || "未知错误", "error"];
    setText(nodes.statusTitle, content[0]);
    setText(nodes.statusMessage, content[1]);
    nodes.status.dataset.kind = content[2];
    nodes.retry.hidden = state.phase === "signed_out" || state.phase === "oidc_disabled" || state.phase === "forbidden";
  }

  function renderAccount() {
    if (state.authenticated) {
      const handle = state.user?.handle && state.user.handle !== state.user.displayName ? ` · ${state.user.handle}` : "";
      setText(nodes.currentUser, `${state.user?.displayName || "已登录"}${handle}`);
    } else {
      setText(nodes.currentUser, state.phase === "oidc_disabled" ? "登录服务未配置" : "未登录");
    }
    nodes.login.hidden = state.authenticated || state.phase === "oidc_disabled";
    nodes.logout.hidden = !state.authenticated;
  }

  function renderProjects() {
    nodes.projects.replaceChildren(...state.projects.map((project) => createSelectionButton(documentImpl, {
      id: project.id,
      title: project.name,
      meta: [project.role, project.archived ? "已归档" : ""].filter(Boolean).join(" · "),
    }, project.id === state.selectedProjectId, selectProject)));
    nodes.projectsEmpty.hidden = state.projects.length > 0;
  }

  function renderAgents() {
    nodes.agents.replaceChildren(...state.agents.map((agent) => createSelectionButton(documentImpl, {
      id: agent.id,
      title: agent.name,
      meta: [agent.type, agent.status].filter(Boolean).join(" · "),
    }, agent.id === state.selectedAgentId, selectAgent)));
    nodes.agentsHint.hidden = Boolean(state.selectedProjectId && state.agents.length);
    setText(nodes.agentsHint, state.selectedProjectId ? "此项目没有可访问的 Agent。" : "请先选择项目。");
  }

  function renderMessages() {
    const fragments = state.messages.map((message) => {
      const article = documentImpl.createElement("article");
      article.className = "message";
      article.dataset.role = message.role;
      article.setAttribute("role", "listitem");
      const header = documentImpl.createElement("header");
      header.className = "message-header";
      const role = documentImpl.createElement("span");
      role.className = "message-role";
      setText(role, messageRoleLabel(message.role));
      const time = documentImpl.createElement("time");
      if (message.createdAt) time.dateTime = message.createdAt;
      setText(time, [message.createdAt, message.completionState].filter(Boolean).join(" · "));
      const content = documentImpl.createElement("p");
      content.className = "message-content";
      setText(content, message.content, "（空消息）");
      header.append(role, time);
      article.append(header, content);
      return article;
    });
    nodes.messages.replaceChildren(...fragments);
    nodes.messagesHint.hidden = Boolean(state.selectedAgentId && state.messages.length);
    setText(nodes.messagesHint, state.selectedAgentId ? "此 Agent 暂无消息。" : "请选择 Agent 查看消息。");
    nodes.refreshMessages.disabled = !state.selectedAgentId || state.busy;
    const control = submissionControlState({ capability: state.capability, authenticated: state.authenticated, projectId: state.selectedProjectId, agentId: state.selectedAgentId, busy: state.busy });
    nodes.taskForm.hidden = !control.visible;
    nodes.taskContent.disabled = control.disabled;
    nodes.submitTask.disabled = control.disabled;
    setText(nodes.taskHelp, control.reason || "任务固定使用只读权限；不会在浏览器存储身份凭据或任务草稿。");
  }

  function render() {
    if (destroyed) return;
    renderStatus();
    renderAccount();
    if (state.phase === "ready") {
      renderProjects();
      renderAgents();
      renderMessages();
    }
  }

  function handleError(error) {
    const normalized = error?.kind ? error : normalizeAppError({ body: { message: error?.message } });
    if (normalized.kind === "unauthenticated") setState({ type: "signed_out", error: normalized });
    else setState({ type: "failed", error: normalized });
  }

  async function loadSession() {
    setState({ type: "loading" });
    try {
      let session;
      try {
        session = await requestAppJSON(buildAuthPath("session"), {}, fetchImpl);
      } catch (error) {
        if (error.status !== 404 || error.kind === "oidc_disabled") throw error;
        session = await requestAppJSON(buildAppApiPath("me"), {}, fetchImpl);
      }
      if (session?.authenticated === false) {
        setState({ type: "signed_out" });
        return;
      }
      setState({ type: "authenticated", payload: session });
      await loadProjects();
    } catch (error) {
      handleError(error);
    }
  }

  async function loadProjects() {
    const requestId = ++projectRequest;
    nodes.projects.setAttribute("aria-busy", "true");
    try {
      const payload = await requestAppJSON(buildAppApiPath("projects"), {}, fetchImpl);
      if (destroyed || requestId !== projectRequest) return;
      setState({ type: "projects_loaded", payload });
    } catch (error) {
      handleError(error);
    } finally {
      if (requestId === projectRequest) nodes.projects.setAttribute("aria-busy", "false");
    }
  }

  async function selectProject(projectId) {
    const requestId = ++agentRequest;
    ++messageRequest;
    setState({ type: "project_selected", projectId });
    nodes.agents.setAttribute("aria-busy", "true");
    try {
      const payload = await requestAppJSON(buildAppApiPath("agents", { projectId }), {}, fetchImpl);
      if (destroyed || requestId !== agentRequest) return;
      setState({ type: "agents_loaded", payload });
    } catch (error) {
      handleError(error);
    } finally {
      if (requestId === agentRequest) nodes.agents.setAttribute("aria-busy", "false");
    }
  }

  async function selectAgent(agentId) {
    setState({ type: "agent_selected", agentId });
    await loadMessages();
  }

  async function loadMessages() {
    if (!state.selectedProjectId || !state.selectedAgentId) return;
    const requestId = ++messageRequest;
    const projectId = state.selectedProjectId;
    const agentId = state.selectedAgentId;
    nodes.messages.setAttribute("aria-busy", "true");
    try {
      const payload = await requestAppJSON(buildAppApiPath("messages", { projectId, agentId, limit: 100 }), {}, fetchImpl);
      if (destroyed || requestId !== messageRequest || projectId !== state.selectedProjectId || agentId !== state.selectedAgentId) return;
      setState({ type: "messages_loaded", payload });
    } catch (error) {
      handleError(error);
    } finally {
      if (requestId === messageRequest) nodes.messages.setAttribute("aria-busy", "false");
    }
  }

  async function submitTask(event) {
    event.preventDefault();
    const control = submissionControlState({ capability: state.capability, authenticated: state.authenticated, projectId: state.selectedProjectId, agentId: state.selectedAgentId, busy: state.busy });
    if (control.disabled) return;
    let request;
    try {
      request = buildReadOnlyMessageSubmission(state.selectedProjectId, state.selectedAgentId, nodes.taskContent.value);
    } catch (error) {
      setText(nodes.taskHelp, error.message);
      nodes.taskContent.focus();
      return;
    }
    setState({ type: "busy", value: true });
    try {
      await requestAppJSON(request.path, request.options, fetchImpl);
      nodes.taskContent.value = "";
      setText(nodes.taskHelp, "已以只读权限提交。正在刷新消息…");
      await loadMessages();
    } catch (error) {
      handleError(error);
    } finally {
      if (!destroyed) setState({ type: "busy", value: false });
    }
  }

  async function logout() {
    nodes.logout.disabled = true;
    try {
      await requestAppJSON(buildAuthPath("logout"), { method: "POST" }, fetchImpl);
      setState({ type: "signed_out" });
    } catch (error) {
      handleError(error);
    } finally {
      nodes.logout.disabled = false;
    }
  }

  const returnTo = `${locationImpl?.pathname || "/app"}${locationImpl?.search || ""}`;
  nodes.login.href = buildAuthPath("login", { returnTo });
  nodes.retry.addEventListener("click", loadSession);
  nodes.refreshProjects.addEventListener("click", loadProjects);
  nodes.refreshMessages.addEventListener("click", loadMessages);
  nodes.taskForm.addEventListener("submit", submitTask);
  nodes.logout.addEventListener("click", logout);
  render();
  loadSession();

  return () => {
    destroyed = true;
    projectRequest += 1;
    agentRequest += 1;
    messageRequest += 1;
    nodes.retry.removeEventListener("click", loadSession);
    nodes.refreshProjects.removeEventListener("click", loadProjects);
    nodes.refreshMessages.removeEventListener("click", loadMessages);
    nodes.taskForm.removeEventListener("submit", submitTask);
    nodes.logout.removeEventListener("click", logout);
  };
}

if (typeof document !== "undefined") {
  if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", () => mountOAuthApp(), { once: true });
  else mountOAuthApp();
}
