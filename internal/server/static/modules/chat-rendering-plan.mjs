import { escapeAttr, escapeHtml } from "./dom.mjs";
import { t as cr } from "./messages-chat-rendering-extra.mjs";

const PLAN_DRAFT_KEYS = Object.freeze(["goal", "assumptions", "steps", "risks", "tests", "rollback"]);
const APPROVED_PLAN_EXECUTE_PREFIX = "Execute the approved plan exactly as reviewed.";
const PLAN_REFLECTION_PREFIX = "PLAN_REFLECTION_REPLAN";

function planText(value, fallback = "") {
  if (value === null || value === undefined) return fallback;
  if (typeof value === "string" || typeof value === "number") return String(value).trim() || fallback;
  if (typeof value === "object") return planText(value.title ?? value.text ?? value.message ?? value.description ?? value.name, fallback);
  return fallback;
}

function planList(value) {
  if (Array.isArray(value)) return value.filter((item) => item !== null && item !== undefined);
  return value === null || value === undefined || value === "" ? [] : [value];
}

function planStatus(value, fallback = "draft") {
  const status = String(value || "").trim().toLowerCase().replace(/[\s-]+/g, "_");
  return status || fallback;
}

function isStringArray(value) {
  return Array.isArray(value) && value.every((item) => typeof item === "string");
}

function messageRunId(message) {
  return String(message?.runId ?? message?.run_id ?? "").trim();
}

function planSourceRunId(plan) {
  return String(plan?.sourceRunId ?? plan?.source_run_id ?? "").trim();
}

function planMessageText(message) {
  return String(message?.contentText ?? message?.content_text ?? "").trim();
}

export function normalizeAgentPlan(value, agentId = "") {
  const wrapper = value && typeof value === "object" ? value : {};
  const source = wrapper.plan && typeof wrapper.plan === "object" ? wrapper.plan : wrapper;
  const review = source.review && typeof source.review === "object" ? source.review : {};
  const steps = planList(source.steps ?? source.planSteps ?? source.plan_steps).map((step, index) => ({
    title: planText(step, cr("plan.stepFallback", { index: index + 1 })),
    detail: typeof step === "object" ? planText(step.detail ?? step.description ?? step.reason) : "",
    status: typeof step === "object" ? planStatus(step.status, "") : "",
  }));
  const risks = planList(source.risks ?? source.riskItems ?? source.risk_items).map((risk) => planText(risk)).filter(Boolean);
  const reviewFindings = planList(source.reviewFindings ?? source.review_findings ?? review.findings ?? review.items)
    .map((finding) => planText(finding))
    .filter(Boolean);
  const rawRevision = Number(source.revision ?? source.planRevision ?? source.plan_revision);
  const plan = {
    id: planText(source.id ?? source.planId ?? source.plan_id),
    agentId: planText(source.agentId ?? source.agent_id, agentId),
    sourceRunId: planText(source.sourceRunId ?? source.source_run_id),
    revision: Number.isSafeInteger(rawRevision) && rawRevision > 0 ? rawRevision : 0,
    goal: planText(source.goal ?? source.objective ?? source.title ?? source.summary),
    status: planStatus(source.status ?? source.state, wrapper.pendingApproval === true || wrapper.pendingPlanApproval === true ? "pending_approval" : "draft"),
    steps,
    risks,
    reviewVerdict: planText(source.reviewVerdict ?? source.review_verdict ?? review.verdict ?? review.status),
    reviewFindings,
    staleReason: planText(source.staleReason ?? source.stale_reason ?? source.invalidReason ?? source.invalid_reason),
    createdAt: planText(source.createdAt ?? source.created_at),
    updatedAt: planText(source.updatedAt ?? source.updated_at),
  };
  return plan.id || plan.goal || plan.steps.length || plan.risks.length || plan.reviewVerdict || plan.staleReason ? plan : null;
}

export function compactPlanStatus(status) {
  const value = planStatus(status);
  if (["in_review", "pending_approval", "awaiting_approval", "approval_required"].includes(value)) return "pending_approval";
  if (["approved", "ready", "accepted"].includes(value)) return "approved";
  if (["executing", "running", "in_progress"].includes(value)) return "executing";
  if (["executed", "completed", "done"].includes(value)) return "executed";
  if (["cancelled", "canceled", "rejected"].includes(value)) return "cancelled";
  if (["stale", "invalid", "outdated"].includes(value)) return "stale";
  if (value === "draft" || value === "planning") return "draft";
  return "unknown";
}

function compactReviewVerdict(verdict) {
  const value = planStatus(verdict, "");
  if (["pass", "needs_human", "block_recommended", "unavailable"].includes(value)) return value;
  return "";
}

function planReviewVerdictLabel(verdict) {
  const value = compactReviewVerdict(verdict);
  if (!value) return planText(verdict);
  return cr(`plan.verdict.${value}`);
}

function planReviewFindingTexts(plan) {
  if (compactReviewVerdict(plan?.reviewVerdict) === "unavailable") return [cr("plan.reasonUnavailable")];
  return Array.isArray(plan?.reviewFindings) ? plan.reviewFindings : [];
}

export function parsePlanDraftText(text) {
  const raw = String(text || "").trim();
  if (!raw.startsWith("{") || !raw.endsWith("}")) return null;
  let parsed;
  try {
    parsed = JSON.parse(raw);
  } catch {
    return null;
  }
  if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) return null;
  const keys = Object.keys(parsed);
  if (keys.length !== PLAN_DRAFT_KEYS.length) return null;
  if (!PLAN_DRAFT_KEYS.every((key) => Object.prototype.hasOwnProperty.call(parsed, key))) return null;
  if (typeof parsed.goal !== "string" || !parsed.goal.trim()) return null;
  if (!isStringArray(parsed.assumptions) || !isStringArray(parsed.steps) || !isStringArray(parsed.risks) || !isStringArray(parsed.tests) || !isStringArray(parsed.rollback)) {
    return null;
  }
  return normalizeAgentPlan({
    goal: parsed.goal,
    steps: parsed.steps,
    risks: parsed.risks,
    status: "draft",
  });
}

export function isApprovedPlanExecuteMessage(text) {
  return String(text || "").trim().startsWith(APPROVED_PLAN_EXECUTE_PREFIX);
}

export function isPlanReflectionMessage(text) {
  return String(text || "").trim().startsWith(PLAN_REFLECTION_PREFIX);
}

export function looksLikePlanDraft(text) {
  const raw = String(text || "").trim();
  return raw.startsWith("{") && raw.includes('"goal"');
}

function collectCandidatePlans(state = {}, agentId = "") {
  const list = [];
  const seen = new Set();
  const add = (value) => {
    const plan = normalizeAgentPlan(value, agentId);
    if (!plan) return;
    if (plan.id) {
      if (seen.has(plan.id)) return;
      seen.add(plan.id);
    }
    list.push(plan);
  };
  add(state.pendingPlanApproval);
  add(state.activePlan);
  for (const item of Array.isArray(state.transcriptPlans) ? state.transcriptPlans : []) add(item);
  return list;
}

function overlayDraftWithLive(draft, live) {
  if (!live) return draft;
  return {
    ...draft,
    id: live.id || draft.id,
    agentId: live.agentId || draft.agentId,
    sourceRunId: live.sourceRunId || draft.sourceRunId,
    revision: live.revision || draft.revision,
    status: live.status || draft.status,
    reviewVerdict: live.reviewVerdict || draft.reviewVerdict,
    reviewFindings: live.reviewFindings?.length ? live.reviewFindings : draft.reviewFindings,
    staleReason: live.staleReason || draft.staleReason,
    createdAt: live.createdAt || draft.createdAt,
    updatedAt: live.updatedAt || draft.updatedAt,
  };
}

export function planForMessage(message, state = {}) {
  if (!message || typeof message !== "object") return null;
  const role = String(message.role || "").trim().toLowerCase();
  if (role && role !== "assistant") return null;
  const agentId = state.agent?.id || "";
  const candidates = collectCandidatePlans(state, agentId);
  const runId = messageRunId(message);
  if (runId) {
    const matched = candidates.find((plan) => planSourceRunId(plan) === runId);
    if (matched) return matched;
  }
  const draft = parsePlanDraftText(planMessageText(message));
  if (!draft) return null;
  const live = candidates.find((plan) => plan.goal && plan.goal === draft.goal);
  return live ? overlayDraftWithLive(draft, live) : draft;
}

export function planCopyMarkdown(plan) {
  const title = String(plan?.goal || cr("plan.untitled")).trim();
  const steps = Array.isArray(plan?.steps) ? plan.steps : [];
  if (!steps.length) return title;
  const lines = [title, ""];
  steps.forEach((step, index) => {
    const label = typeof step === "string" ? step.trim() : String(step?.title || "").trim();
    if (label) lines.push(`${index + 1}. ${label}`);
  });
  return lines.join("\n");
}

export function renderPlanSystemNoticeHTML({ kind } = {}) {
  const key = kind === "reflect" ? "plan.reflectNotice" : "plan.executeNotice";
  return `<p class="plan-system-notice-text">${escapeHtml(cr(key))}</p>`;
}

function mergePlanList(existing, extras, agentId) {
  const list = [];
  const indexById = new Map();
  const add = (value) => {
    const plan = normalizeAgentPlan(value, agentId);
    if (!plan?.id) return;
    if (indexById.has(plan.id)) {
      const index = indexById.get(plan.id);
      const prev = list[index];
      list[index] = {
        ...prev,
        ...plan,
        sourceRunId: plan.sourceRunId || prev.sourceRunId,
      };
      return;
    }
    indexById.set(plan.id, list.length);
    list.push(plan);
  };
  for (const item of existing) add(item);
  for (const item of extras) add(item);
  return list;
}

export function createChatRenderingPlanCards({
  state,
  request,
  applyMessageSnapshot,
  scheduleMessageRefresh,
  showError,
  showToast,
} = {}) {
  function currentPlanForAgent(agentId = state.agent?.id) {
    const active = normalizeAgentPlan(state.activePlan, agentId);
    const pending = normalizeAgentPlan(state.pendingPlanApproval, agentId);
    if (pending && (!pending.agentId || pending.agentId === agentId)) return pending;
    if (active && (!active.agentId || active.agentId === agentId)) return active;
    return null;
  }

  function planStatusLabel(status) {
    const value = compactPlanStatus(status);
    return cr(`plan.status.${value}`);
  }

  function planStatusClass(status) {
    const value = compactPlanStatus(status);
    if (["approved", "executed"].includes(value)) return "status-completed";
    if (["pending_approval", "stale"].includes(value)) return "status-warn";
    if (["cancelled"].includes(value)) return "status-error";
    return "status-neutral";
  }

  function renderPlanCardHTML(plan) {
    if (!plan) return "";
    const status = compactPlanStatus(plan.status);
    const pending = Boolean(plan.id) && (status === "pending_approval" || normalizeAgentPlan(state.pendingPlanApproval)?.id === plan.id);
    const busy = Boolean(plan.id && state.planActionBusy?.[plan.id]);
    const executable = Boolean(plan.id) && ["approved", "ready", "accepted"].includes(status);
    const cancellable = Boolean(plan.id) && !["executed", "cancelled"].includes(status);
    // Mirrors the backend transition rule: replan is a 409 from these states,
    // so the button and the feedback box should not be offered at all.
    const replannable = Boolean(plan.id) && !["executing", "executed", "cancelled"].includes(status);
    const feedbackDraft = plan.id ? String(state.planFeedbackDrafts?.[plan.id] || "") : "";
    const title = plan.goal || cr("plan.untitled");
    const steps = plan.steps.length ? `
      <section class="plan-card-section">
        <h4>${escapeHtml(cr("plan.steps"))}</h4>
        <ol class="plan-card-steps">${plan.steps.map((step) => `<li class="${escapeAttr(step.status ? `is-${compactPlanStatus(step.status)}` : "")}"><strong>${escapeHtml(step.title)}</strong>${step.detail ? `<span>${escapeHtml(step.detail)}</span>` : ""}</li>`).join("")}</ol>
      </section>
    ` : "";
    const risks = plan.risks.length ? `
      <section class="plan-card-section">
        <h4>${escapeHtml(cr("plan.risks"))}</h4>
        <ul class="plan-card-list risk">${plan.risks.map((risk) => `<li>${escapeHtml(risk)}</li>`).join("")}</ul>
      </section>
    ` : "";
    const reviewFindings = planReviewFindingTexts(plan);
    const review = plan.reviewVerdict || reviewFindings.length ? `
      <section class="plan-card-section plan-card-review">
        <h4>${escapeHtml(cr("plan.review"))}</h4>
        ${plan.reviewVerdict ? `<div class="plan-review-verdict">${escapeHtml(planReviewVerdictLabel(plan.reviewVerdict))}</div>` : ""}
        ${reviewFindings.length ? `<ul class="plan-card-list">${reviewFindings.map((finding) => `<li>${escapeHtml(finding)}</li>`).join("")}</ul>` : `<p>${escapeHtml(cr("plan.noFindings"))}</p>`}
      </section>
    ` : "";
    const stale = plan.staleReason ? `<div class="plan-card-stale" role="status"><strong>${escapeHtml(cr("plan.staleReason"))}</strong><span>${escapeHtml(plan.staleReason)}</span></div>` : "";
    return `
      <section class="plan-card chat-flow-item chat-flow-left chat-report-card ${escapeAttr(planStatusClass(status))}" data-chat-alignment="left" data-chat-report="agent-plan" data-plan-card="${escapeAttr(plan.id)}">
        <div class="plan-card-head">
          <div>
            <div class="plan-card-kicker">${escapeHtml(cr("plan.kicker"))}</div>
            <div class="plan-card-title">${escapeHtml(title)}</div>
          </div>
          <span class="plan-card-status">${escapeHtml(planStatusLabel(status))}</span>
        </div>
        <section class="plan-card-section plan-card-goal"><h4>${escapeHtml(cr("plan.goal"))}</h4><p>${escapeHtml(title)}</p></section>
        ${steps}${risks}${review}${stale}
        ${replannable ? `
        <div class="plan-card-feedback">
          <label for="plan-feedback-${escapeAttr(plan.id)}">${escapeHtml(cr("plan.feedbackLabel"))}</label>
          <textarea id="plan-feedback-${escapeAttr(plan.id)}" data-plan-feedback="${escapeAttr(plan.id)}" rows="2" placeholder="${escapeAttr(cr("plan.feedbackPlaceholder"))}" ${busy ? "disabled" : ""}>${escapeHtml(feedbackDraft)}</textarea>
        </div>` : ""}
        <div class="plan-card-actions">
          ${pending ? `<button class="ghost-btn mini" type="button" data-plan-action="approve" data-plan-id="${escapeAttr(plan.id)}" ${busy ? "disabled" : ""}>${escapeHtml(cr("plan.approve"))}</button>` : ""}
          ${executable ? `<button class="ghost-btn mini primary" type="button" data-plan-action="execute" data-plan-id="${escapeAttr(plan.id)}" ${busy ? "disabled" : ""}>${escapeHtml(busy ? cr("plan.working") : cr("plan.execute"))}</button>` : ""}
          ${cancellable ? `<button class="ghost-btn mini danger" type="button" data-plan-action="cancel" data-plan-id="${escapeAttr(plan.id)}" ${busy ? "disabled" : ""}>${escapeHtml(cr("plan.cancel"))}</button>` : ""}
          ${replannable ? `<button class="ghost-btn mini" type="button" data-plan-action="replan" data-plan-id="${escapeAttr(plan.id)}" ${busy ? "disabled" : ""}>${escapeHtml(cr("plan.replan"))}</button>` : ""}
        </div>
      </section>
    `;
  }

  function planRepresentedInVisibleMessages(plan) {
    if (!plan) return false;
    const messages = Array.isArray(state.currentMessages) ? state.currentMessages : [];
    return messages.some((message) => {
      const resolved = planForMessage(message, state);
      if (!resolved) return false;
      if (plan.id && resolved.id && plan.id === resolved.id) return true;
      const runId = messageRunId(message);
      const source = planSourceRunId(plan);
      return Boolean(runId && source && runId === source);
    });
  }

  function renderPlanCardsHTML() {
    const plan = currentPlanForAgent();
    if (!plan || planRepresentedInVisibleMessages(plan)) return "";
    return renderPlanCardHTML(plan);
  }

  function renderPlanCards() {
    if (state.chatHydrating || !state.agent?.id) return;
    // Plan state can change at the end of a run while the reader is looking at
    // history. Let the normal follow intent decide whether to stay at the tail;
    // forceRender here used to pull that reader back to the newest message.
    applyMessageSnapshot(state.currentMessages, state.agent.id);
  }

  function replacePlanState(activePlan, pendingPlanApproval, agentId = state.agent?.id, transcriptPlans) {
    if (!agentId || state.agent?.id !== agentId) return false;
    const active = normalizeAgentPlan(activePlan, agentId);
    const pending = normalizeAgentPlan(pendingPlanApproval, agentId);
    state.activePlan = active;
    state.pendingPlanApproval = pending || (compactPlanStatus(active?.status) === "pending_approval" ? active : null);
    const base = Array.isArray(transcriptPlans)
      ? transcriptPlans
      : (Array.isArray(state.transcriptPlans) ? state.transcriptPlans : []);
    state.transcriptPlans = mergePlanList(base, [pending, active], agentId);
    renderPlanCards();
    return true;
  }

  function clearPlanState(agentId = state.agent?.id) {
    if (!agentId || state.agent?.id !== agentId) return false;
    state.transcriptPlans = [];
    return replacePlanState(null, null, agentId);
  }

  function applyPlanEvent(event) {
    const type = String(event?.type || "").toLowerCase();
    if (!type.startsWith("plan.")) return false;
    const data = event?.data && typeof event.data === "object" ? event.data : {};
    const received = normalizeAgentPlan(data.activePlan ?? data.pendingPlanApproval ?? data.pendingPlan ?? data.plan ?? data, event?.agentId || state.agent?.id);
    const current = currentPlanForAgent(event?.agentId || state.agent?.id);
    if (!received && !current) return false;
    const eventStatus = {
      "plan.approval_required": "pending_approval",
      "plan.approved": "approved",
      "plan.executing": "executing",
      "plan.executed": "executed",
      "plan.cancelled": "cancelled",
      "plan.canceled": "cancelled",
      "plan.stale": "stale",
      "plan.replanned": "draft",
    }[type] || "";
    const plan = {
      ...(current || {}),
      ...(received || {}),
      status: eventStatus || received?.status || current?.status || "draft",
      staleReason: received?.staleReason || data.staleReason || data.stale_reason || (type === "plan.stale" ? event?.text || current?.staleReason : current?.staleReason || ""),
    };
    const pending = data.pendingPlanApproval ?? data.pendingPlan ?? (compactPlanStatus(plan.status) === "pending_approval" ? plan : null);
    return replacePlanState(plan, pending, event?.agentId || state.agent?.id);
  }

  async function performPlanAction(planId, action, button) {
    const agentId = state.agent?.id;
    const plan = currentPlanForAgent(agentId);
    if (!agentId || !plan?.id || plan.id !== planId || !action || state.planActionBusy?.[planId]) return;
    state.planActionBusy = { ...(state.planActionBusy || {}), [planId]: true };
    // Replanning carries the reviewer's notes so the next plan revision can
    // address them instead of guessing why the previous one was rejected.
    const feedback = action === "replan" ? String(state.planFeedbackDrafts?.[planId] || "").trim() : "";
    renderPlanCards();
    try {
      const result = await request(`/api/agents/${encodeURIComponent(agentId)}/plans/${encodeURIComponent(planId)}/${encodeURIComponent(action)}`, {
        method: "POST",
        body: JSON.stringify(feedback ? { revision: plan.revision, comment: feedback } : { revision: plan.revision }),
      });
      if (action === "replan" && state.planFeedbackDrafts?.[planId] !== undefined) {
        const drafts = { ...state.planFeedbackDrafts };
        delete drafts[planId];
        state.planFeedbackDrafts = drafts;
      }
      if (state.agent?.id !== agentId) return;
      const next = normalizeAgentPlan(result?.activePlan ?? result?.pendingPlanApproval ?? result?.pendingPlan ?? result?.plan ?? result, agentId) || {
        ...plan,
        status: { approve: "approved", execute: "executing", cancel: "cancelled", replan: "draft" }[action] || plan.status,
      };
      const pending = result?.pendingPlanApproval ?? result?.pendingPlan ?? (compactPlanStatus(next.status) === "pending_approval" ? next : null);
      replacePlanState(next, pending, agentId);
      showToast(cr(`plan.toast.${action}`), action === "cancel" ? "warn" : "success");
      scheduleMessageRefresh(80, agentId);
    } catch (error) {
      showError(error);
    } finally {
      const busy = { ...(state.planActionBusy || {}) };
      delete busy[planId];
      state.planActionBusy = busy;
      if (state.agent?.id === agentId) renderPlanCards();
    }
  }

  function bindPlanButtons(root) {
    root.querySelectorAll("[data-plan-action]").forEach((button) => {
      button.addEventListener("click", () => performPlanAction(button.dataset.planId || "", button.dataset.planAction || "", button));
    });
    // The card is re-rendered on every plan event, which would wipe whatever
    // the reviewer has typed. The draft therefore lives in state, keyed by
    // plan id, and is written back into the textarea on each render.
    root.querySelectorAll("[data-plan-feedback]").forEach((input) => {
      input.addEventListener("input", () => {
        const planId = input.dataset.planFeedback || "";
        if (!planId) return;
        state.planFeedbackDrafts = { ...(state.planFeedbackDrafts || {}), [planId]: input.value };
      });
    });
  }

  return {
    applyPlanEvent,
    bindPlanButtons,
    clearPlanState,
    performPlanAction,
    planForMessage,
    renderPlanCardHTML,
    renderPlanCards,
    renderPlanCardsHTML,
    renderPlanSystemNoticeHTML,
    replacePlanState,
  };
}
