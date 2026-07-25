import { appMainT as am } from "./messages-app-main-extra.mjs";
import { shellExtraT as sx } from "./messages-shell-extra.mjs";
import { normalizeSkillContext } from "./skills-bootstrap.mjs";

// Tracks and mutates the active skill context (global/project/workspace
// scope) and refreshes the effective skills policy for the current agent
// against the skillsPhaseB controller.
export function createSkillsContext({
  state,
  showToast,
  notifyTerminal,
  getSkillsPhaseB,
  getEffectiveSkillContext,
}) {
  function getSkillContext(override = {}) {
    const hasProject = Object.prototype.hasOwnProperty.call(override, "projectId");
    const hasWorkline = Object.prototype.hasOwnProperty.call(override, "worklineId");
    return normalizeSkillContext({
      scope: override.scope || state.skillContextScope,
      projectId: hasProject ? override.projectId : state.project?.id || "",
      worklineId: hasWorkline ? override.worklineId : state.workline?.id || "",
    });
  }

  function setSkillContext(context = {}) {
    const requested = getSkillContext(context);
    if (requested.scope === "project" && !requested.projectId) {
      state.skillContextScope = "global";
      showToast(sx("app.projectSkillsRequired"), "warn");
      return getSkillContext();
    }
    if (requested.scope === "workspace" && !requested.worklineId) {
      state.skillContextScope = state.project?.id ? "project" : "global";
      showToast(sx("app.workspaceSkillsRequired"), "warn");
      return getSkillContext();
    }
    state.skillContextScope = requested.scope;
    return requested;
  }

  async function refreshEffectiveSkillsPolicy() {
    const agentId = state.agent?.id || "";
    const skillsPhaseB = getSkillsPhaseB();
    if (!agentId || !skillsPhaseB) return [];
    try {
      return await skillsPhaseB.loadEffective(agentId, getEffectiveSkillContext());
    } catch (error) {
      notifyTerminal(`[warn] ${am("effectiveSkillsRefreshFailed", { message: error?.message || error })}\n`);
      return [];
    }
  }

  function invalidateAndRefreshEffectiveSkillsPolicy() {
    getSkillsPhaseB()?.invalidateEffective({ drop: true });
    return refreshEffectiveSkillsPolicy();
  }

  return {
    getSkillContext,
    setSkillContext,
    refreshEffectiveSkillsPolicy,
    invalidateAndRefreshEffectiveSkillsPolicy,
  };
}
