export const CONV_ORDER_KEY_PREFIX = "autoto:conv_order:";
export const PROJ_ORDER_KEY = "autoto:project_order";
export const NAV_COLLAPSED_KEY = "autoto:nav_collapsed";

// Only collapsed nodes are stored, so a project or conversation the reader has
// never touched starts open and newly arriving ones are not hidden by a stale
// record. Entries are "scope:id".
export function getCollapsedNavNodes() {
  try {
    const raw = localStorage.getItem(NAV_COLLAPSED_KEY);
    const parsed = raw ? JSON.parse(raw) : null;
    if (Array.isArray(parsed)) return new Set(parsed.map((entry) => String(entry || "")).filter(Boolean));
  } catch {}
  return new Set();
}

export function saveCollapsedNavNodes(nodes) {
  try {
    localStorage.setItem(NAV_COLLAPSED_KEY, JSON.stringify([...nodes]));
  } catch {}
}

export function toggleCollapsedNavNode(key) {
  const id = String(key || "").trim();
  if (!id) return;
  const nodes = getCollapsedNavNodes();
  if (nodes.has(id)) nodes.delete(id);
  else nodes.add(id);
  saveCollapsedNavNodes(nodes);
}

export function getProjectOrder() {
  try {
    const val = localStorage.getItem(PROJ_ORDER_KEY);
    if (val) return JSON.parse(val);
  } catch {}
  return null;
}

export function saveProjectOrder(projectIds) {
  try {
    localStorage.setItem(PROJ_ORDER_KEY, JSON.stringify(projectIds));
  } catch {}
}

export function getConversationOrders() {
  const orders = {};
  try {
    for (let i = 0; i < localStorage.length; i++) {
      const key = localStorage.key(i);
      if (!key?.startsWith(CONV_ORDER_KEY_PREFIX)) continue;
      const projectId = key.slice(CONV_ORDER_KEY_PREFIX.length);
      const val = localStorage.getItem(key);
      if (val) orders[projectId] = JSON.parse(val);
    }
  } catch {}
  return orders;
}

export function saveConversationOrder(projectId, agentIds) {
  try {
    localStorage.setItem(CONV_ORDER_KEY_PREFIX + projectId, JSON.stringify(agentIds));
  } catch {}
}
