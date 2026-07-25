import { $, escapeAttr, escapeHtml } from "./dom.mjs";
import { addRecentConversation, normalizeRecentConversations } from "./conversation-navigation.mjs";
import { canonicalLocalPath, shortPath } from "./directory-browser.mjs";
import { t } from "./i18n.mjs";
import { appMainT as am } from "./messages-app-main-extra.mjs";
import { readLocalPreference, recentConversationsKey } from "./preferences-data.mjs";
import { isComposingInput } from "./ui-shell.mjs";

// Conversation/workbench title editor state (element lookup, draft
// tracking, editor visibility) plus the workspace meta pills row and the
// recent-conversations local-storage cache. renderConversationHeaderIdentity,
// renderWorkbenchHeaderIdentity, beginConversationTitleEdit,
// saveConversationTitle and renderRecentSidebarConversations stay in
// app-main.mjs (source-pinned) but call back into these helpers.
export function createConversationTitleHelpers({
  state,
  selectedModelValue,
  currentModelValue,
  projectOperationContextActive,
  effectivePermissionForDisplay,
  connectionModeSummary,
  permissionLabel,
  renderConversationHeaderIdentity,
  renderWorkbenchHeaderIdentity,
  renderRecentSidebarConversations,
  saveConversationTitle,
  showError,
}) {
  function currentWorkspaceModel() {
    return state.agent?.model || selectedModelValue() || currentModelValue() || am("noModelSelected");
  }

  const maxDerivedTitleCharacters = 28;

  // An untitled conversation used to fall back to the project name, which is an
  // absolute path -- unhelpful as a heading, and the sidebar entry already shows
  // the path underneath. The opening user message describes what the
  // conversation is actually about, so it is preferred. Purely presentational:
  // nothing is persisted, and an explicit agent title always wins.
  function derivedConversationTitle() {
    const messages = Array.isArray(state.currentMessages) ? state.currentMessages : [];
    const first = messages.find((message) => String(message?.role || "").toLowerCase() === "user"
      && String(message?.content || "").trim());
    if (!first) return "";
    const text = String(first.content || "")
      .replace(/```[\s\S]*?```/g, " ")
      .replace(/\s+/g, " ")
      .trim();
    if (!text) return "";
    const characters = Array.from(text);
    return characters.length > maxDerivedTitleCharacters
      ? `${characters.slice(0, maxDerivedTitleCharacters).join("")}…`
      : text;
  }

  function conversationHeaderTitle() {
    // A conversation with nothing in it yet has nothing to derive a title from,
    // and the project path is not a useful heading, so it reads as new until the
    // first message arrives.
    return state.agent?.title
      || state.navigationTransitionTitle
      || derivedConversationTitle()
      || t("shell.newConversation");
  }

  function titleEditorElements(surface) {
    const workbench = surface === "workbench";
    return {
      display: $(workbench ? "workbenchTitle" : "currentTitle"),
      input: $(workbench ? "workbenchTitleInput" : "currentTitleInput"),
      edit: $(workbench ? "editWorkbenchTitleBtn" : "editConversationTitleBtn"),
      save: $(workbench ? "saveWorkbenchTitleBtn" : "saveConversationTitleBtn"),
      cancel: $(workbench ? "cancelWorkbenchTitleBtn" : "cancelConversationTitleBtn"),
      editLabel: am(workbench ? "editWorkbenchTitle" : "editConversationTitle"),
      fieldLabel: am(workbench ? "workbenchTitleLabel" : "conversationTitle"),
    };
  }

  function titleForSurface(surface) {
    if (surface === "workbench") return state.agent?.title || state.navigationTransitionTitle || derivedConversationTitle() || t("workbench.title");
    return conversationHeaderTitle();
  }

  function renderAllTitleEditors() {
    renderConversationHeaderIdentity();
    renderWorkbenchHeaderIdentity();
  }

  function normalizedTitleEditSurface(surface) {
    return surface === "workbench" ? "workbench" : "conversation";
  }

  function cancelConversationTitleEdit() {
    if (state.titleSaving) return;
    state.titleEditing = false;
    state.titleDraft = "";
    renderAllTitleEditors();
  }

  function updateTitleDraft(surface, event) {
    state.titleEditSurface = normalizedTitleEditSurface(surface);
    state.titleDraft = event.target.value;
  }

  function handleTitleEditorKeydown(surface, event) {
    if (isComposingInput(event)) return;
    if (event.key === "Enter") {
      event.preventDefault();
      saveConversationTitle(surface).catch(showError);
    } else if (event.key === "Escape") {
      event.preventDefault();
      event.stopPropagation();
      cancelConversationTitleEdit();
    }
  }

  function updateWorkspaceMetaPills() {
    const el = $("workspaceMetaPills");
    if (!el) return;
    if (!state.project && !state.agent) {
      el.classList.add("hidden");
      el.innerHTML = "";
      return;
    }
    const model = currentWorkspaceModel();
    if (!projectOperationContextActive()) {
      el.innerHTML = `
        <span class="workspace-pill">${escapeHtml(`${t("shell.filters.conversations")} · ${t("chat.permission.readOnly")}`)}</span>
        <span class="workspace-pill" title="${escapeAttr(model)}">${escapeHtml(t("workspace.main.modelLabel", { model }))}</span>
      `;
      el.classList.remove("hidden");
      return;
    }
    const cwd = canonicalLocalPath(state.agent?.cwd || state.project?.gitPath || "");
    const permission = effectivePermissionForDisplay(state.agent?.permissionMode || $("permissionMode")?.value || state.settings?.agent?.defaultPermissionMode || "acceptEdits");
    const securityText = connectionModeSummary().label;
    el.innerHTML = `
      <span class="workspace-pill" title="${escapeAttr(cwd)}">${escapeHtml(t("workspace.main.directoryLabel", { path: shortPath(cwd) }))}</span>
      <span class="workspace-pill">${escapeHtml(t("workspace.main.permissionLabel", { permission: permissionLabel(permission) }))}</span>
      <span class="workspace-pill security-workspace-pill">${escapeHtml(t("workspace.main.modeLabel", { mode: securityText }))}</span>
      <span class="workspace-pill" title="${escapeAttr(model)}">${escapeHtml(t("workspace.main.modelLabel", { model }))}</span>
    `;
    el.classList.remove("hidden");
  }

  function loadRecentConversations() {
    try {
      return normalizeRecentConversations(JSON.parse(readLocalPreference(recentConversationsKey) || "[]"));
    } catch {
      return [];
    }
  }

  function rememberCurrentConversation() {
    if (!state.agent?.id) return;
    const navigationConversation = state.navigationConversations.find((item) => item.agentId === state.agent.id);
    const target = navigationConversation?.targetId || (state.project?.id && state.workline?.id
      ? { projectId: state.project.id, worklineId: state.workline.id, agentId: state.agent.id }
      : { projectId: "", worklineId: "", agentId: state.agent.id });
    state.recentConversations = addRecentConversation(state.recentConversations, target);
    try {
      localStorage.setItem(recentConversationsKey, JSON.stringify(state.recentConversations));
    } catch {}
    renderRecentSidebarConversations();
  }

  return {
    currentWorkspaceModel,
    conversationHeaderTitle,
    titleEditorElements,
    titleForSurface,
    renderAllTitleEditors,
    normalizedTitleEditSurface,
    cancelConversationTitleEdit,
    updateTitleDraft,
    handleTitleEditorKeydown,
    updateWorkspaceMetaPills,
    loadRecentConversations,
    rememberCurrentConversation,
  };
}
