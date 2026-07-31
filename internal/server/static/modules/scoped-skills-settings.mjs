import { escapeAttr, escapeHtml } from "./dom.mjs";
import { t } from "./messages-skills.mjs";
import { normalizeSkillContext, skillContextAvailability } from "./skills-bootstrap.mjs";

const MAX_SKILL_FILE_BYTES = 128 * 1024;

function contextLabel(context) {
  if (context.scope === "project") return t("skillsWorkbench.scope.project", { suffix: context.projectId ? ` · ${context.projectId}` : "" });
  if (context.scope === "workspace") return t("skillsWorkbench.scope.workspace", { suffix: context.worklineId ? ` · ${context.worklineId}` : "" });
  return t("skillsWorkbench.scope.global");
}

function verdictLabel(verdict) {
  return {
    safe: t("skillsWorkbench.skills.verdictSafe"),
    review: t("skillsWorkbench.skills.verdictReview"),
    blocked: t("skillsWorkbench.skills.verdictBlocked"),
  }[String(verdict || "").toLowerCase()] || String(verdict || "");
}

function verdictTone(verdict) {
  return verdict === "safe" ? "ok" : verdict === "review" ? "warn" : "muted";
}

function scopeBadge(scope) {
  const normalized = normalizeSkillContext({ scope }).scope;
  const label = normalized === "project" ? t("skillsWorkbench.scope.projectBadge") : normalized === "workspace" ? t("skillsWorkbench.scope.workspaceBadge") : t("skillsWorkbench.scope.globalBadge");
  return `<span class="skill-scope-badge settings-badge skill-scope-${escapeAttr(normalized)}">${escapeHtml(label)}</span>`;
}

function findingsMarkup(skill) {
  const findings = Array.isArray(skill?.scanFindings) ? skill.scanFindings : [];
  if (findings.length) {
    return `<ul class="skill-findings">${findings.map((finding) => `<li><strong>${escapeHtml(finding?.code || "scan")}</strong>：${escapeHtml(finding?.message || t("skillsWorkbench.skills.findingDefault"))}</li>`).join("")}</ul>`;
  }
  return "";
}

function mutationBusy(bucket, key) {
  return bucket?.mutations?.[key]?.status === "saving";
}

function ensureDrafts(bucket) {
  if (!bucket.drafts || typeof bucket.drafts !== "object") bucket.drafts = {};
  if (!bucket.drafts.create || typeof bucket.drafts.create !== "object") bucket.drafts.create = {};
  if (!bucket.drafts.edits || typeof bucket.drafts.edits !== "object") bucket.drafts.edits = {};
  if (typeof bucket.drafts.importContent !== "string") bucket.drafts.importContent = "";
  return bucket.drafts;
}

function editDraft(bucket, skill) {
  const drafts = ensureDrafts(bucket);
  if (!drafts.edits[skill.id]) {
    drafts.edits[skill.id] = {
      name: String(skill.name || ""),
      command: String(skill.command || ""),
      description: String(skill.description || ""),
      prompt: String(skill.prompt || ""),
    };
  }
  return drafts.edits[skill.id];
}

function renderEditor(bucket, skill) {
  if (bucket.editingSkillId !== skill.id) return "";
  const draft = editDraft(bucket, skill);
  const busy = mutationBusy(bucket, `skill:${skill.id}`);
  return `<form class="skill-command-form" data-scoped-skill-edit-form="${escapeAttr(skill.id)}">
    <div class="settings-provider-form-grid settings-form-grid">
      <label>${escapeHtml(t("skillsWorkbench.scoped.displayName"))}<input class="settings-field" data-scoped-edit-field="name" value="${escapeAttr(draft.name)}" /></label>
      <label>${escapeHtml(t("skillsWorkbench.scoped.command"))}<input class="settings-field" data-scoped-edit-field="command" value="${escapeAttr(draft.command)}" /></label>
      <label class="settings-form-span-2">${escapeHtml(t("skillsWorkbench.commands.descriptionLabel"))}<input class="settings-field" data-scoped-edit-field="description" value="${escapeAttr(draft.description)}" /></label>
      <label class="settings-form-span-2">${escapeHtml(t("skillsWorkbench.commands.promptLabel"))}<textarea class="settings-field settings-textarea" rows="6" data-scoped-edit-field="prompt">${escapeHtml(draft.prompt)}</textarea></label>
    </div>
    <div class="settings-action-row settings-form-actions settings-inline-actions">
      <button class="settings-action-btn subtle" type="button" data-scoped-skill-edit-cancel="${escapeAttr(skill.id)}">${escapeHtml(t("skillsWorkbench.scoped.cancel"))}</button>
      <button class="settings-action-btn primary" type="submit" ${busy ? "disabled" : ""}>${escapeHtml(busy ? t("skillsWorkbench.scoped.saving") : t("skillsWorkbench.scoped.saveChanges"))}</button>
    </div>
  </form>`;
}

function renderSkillCard(bucket, skill) {
  const verdict = String(skill.scanVerdict || "safe").toLowerCase();
  const detailLoaded = Boolean(skill.detailLoaded && String(skill.prompt || "").trim());
  const busy = mutationBusy(bucket, `skill:${skill.id}`);
  const canEnable = verdict !== "blocked";
  return `<article class="skill-command-card settings-card settings-data-list-row skills-v2-card ${skill.enabled ? "" : "disabled"}">
    <div>
      <div class="skill-command-title settings-card-title">${escapeHtml(skill.command || skill.name || t("skillsWorkbench.skills.unnamedCommand"))} ${scopeBadge(skill.scope)} <span class="settings-status-pill settings-badge ${skill.enabled ? "ok" : "muted"}">${escapeHtml(skill.enabled ? t("skillsWorkbench.skills.enabled") : t("skillsWorkbench.skills.disabled"))}</span> <span class="settings-status-pill settings-badge ${verdictTone(verdict)}">${escapeHtml(verdictLabel(verdict))}</span></div>
      ${findingsMarkup(skill)}
      ${detailLoaded ? `<pre class="skill-command-prompt">${escapeHtml(skill.prompt)}</pre>` : skill.detailError ? `<div class="settings-inline-alert settings-alert" role="alert">${escapeHtml(skill.detailError)}</div>` : ""}
      ${renderEditor(bucket, skill)}
    </div>
    <div class="settings-action-row settings-inline-actions">
      <button class="settings-action-btn subtle" type="button" data-scoped-skill-detail="${escapeAttr(skill.id)}">${escapeHtml(t(detailLoaded ? "skillsWorkbench.skills.refreshDetail" : "skillsWorkbench.skills.loadDetail"))}</button>
      <button class="settings-action-btn subtle" type="button" data-scoped-skill-edit="${escapeAttr(skill.id)}" ${busy ? "disabled" : ""}>${escapeHtml(t("skillsWorkbench.scoped.edit"))}</button>
      <button class="settings-action-btn subtle" type="button" data-scoped-skill-toggle="${escapeAttr(skill.id)}" ${busy || (!skill.enabled && !canEnable) ? "disabled" : ""}>${escapeHtml(skill.enabled ? t("skillsWorkbench.skills.disabled") : canEnable ? t("skillsWorkbench.skills.enabled") : t("skillsWorkbench.skills.cannotEnable"))}</button>
      <button class="settings-action-btn subtle" type="button" data-scoped-skill-revisions="${escapeAttr(skill.id)}">${escapeHtml(t("skillsWorkbench.revisions.title"))}</button>
      <button class="settings-action-btn danger destructive" type="button" data-scoped-skill-delete="${escapeAttr(skill.id)}" ${busy ? "disabled" : ""}>${escapeHtml(t("skillsWorkbench.skills.delete"))}</button>
    </div>
  </article>`;
}

function renderRevisionPanel(bucket, context) {
  const drawer = bucket.drawer;
  if (!drawer?.skillId) return "";
  const revisions = bucket.revisions?.[drawer.skillId] || { items: [], status: "idle", error: "" };
  const items = Array.isArray(revisions.items) ? revisions.items : [];
  const selected = drawer.revisionDetail;
  return `<aside class="skill-revision-drawer settings-sheet settings-card" role="region" aria-label="${escapeAttr(t("skillsWorkbench.revisions.ariaLabel"))}">
    <div class="skill-revision-drawer-head settings-card-header"><div><strong>${escapeHtml(t("skillsWorkbench.revisions.title"))}</strong><small>${escapeHtml(contextLabel(context))}</small></div><button class="settings-action-btn subtle" type="button" data-scoped-revisions-close>${escapeHtml(t("skillsWorkbench.revisions.close"))}</button></div>
    ${revisions.error ? `<div class="settings-inline-alert settings-alert" role="alert">${escapeHtml(revisions.error)}</div>` : ""}
    <div class="skill-revision-list settings-data-list">${revisions.status === "loading" && !items.length ? `<div class="settings-empty-card settings-empty-state compact">${escapeHtml(t("skillsWorkbench.revisions.loading"))}</div>` : items.length ? items.map((revision) => {
      const revisionNo = String(revision.revisionNo ?? revision.revision ?? "");
      return `<div class="skill-revision-card settings-card settings-data-list-row"><div><strong>${escapeHtml(t("skillsWorkbench.revisions.label", { revision: revisionNo }))}</strong><small>${escapeHtml(revision.createdAt || revision.updatedAt || "")}</small></div><div class="settings-action-row settings-inline-actions"><button class="settings-action-btn subtle" type="button" data-scoped-revision-detail="${escapeAttr(revisionNo)}">${escapeHtml(t("skillsWorkbench.revisions.view"))}</button><button class="settings-action-btn danger destructive" type="button" data-scoped-revision-restore="${escapeAttr(revisionNo)}">${escapeHtml(t("skillsWorkbench.revisions.restore"))}</button></div></div>`;
    }).join("") : `<div class="settings-empty-card settings-empty-state compact">${escapeHtml(t("skillsWorkbench.revisions.empty"))}</div>`}</div>
    ${revisions.nextCursor ? `<button class="settings-action-btn subtle" type="button" data-scoped-revisions-more>${escapeHtml(t("skillsWorkbench.commands.loadMore"))}</button>` : ""}
    ${selected ? `<pre class="skill-command-prompt skill-revision-detail">${escapeHtml(selected.prompt || selected.content || JSON.stringify(selected, null, 2))}</pre>` : drawer.revisionDetailError ? `<div class="settings-inline-alert settings-alert" role="alert">${escapeHtml(drawer.revisionDetailError)}</div>` : ""}
  </aside>`;
}

function renderImport(bucket) {
  const previewState = bucket.importPreview || {};
  const preview = previewState.value;
  const busy = mutationBusy(bucket, "import") || previewState.status === "loading";
  return `<section class="settings-provider-section settings-card settings-page-section">
    <div class="settings-provider-title settings-card-title">${escapeHtml(t("skillsWorkbench.commands.importTitle"))}</div>
    <div class="settings-provider-meta settings-card-description" data-settings-help-copy>${escapeHtml(t("skillsWorkbench.commands.importDescription"))}</div>
    <input type="file" accept=".md,text/markdown,text/plain" data-scoped-skill-import-file />
    ${previewState.error ? `<div class="settings-inline-alert settings-alert" role="alert">${escapeHtml(previewState.error)}</div>` : ""}
    ${preview ? `<div class="skill-command-card settings-card settings-data-list-row"><div><strong>${escapeHtml(preview.command || preview.name || t("skillsWorkbench.skills.unnamedCommand"))}</strong> <span class="settings-status-pill settings-badge ${verdictTone(String(preview.scanVerdict || "safe").toLowerCase())}">${escapeHtml(verdictLabel(preview.scanVerdict))}</span>${findingsMarkup(preview)}</div><button class="settings-action-btn primary" type="button" data-scoped-skill-import-confirm ${busy ? "disabled" : ""}>${escapeHtml(t("skillsWorkbench.skills.importConfirm"))}</button></div>` : ""}
  </section>`;
}

function renderCreate(bucket) {
  const draft = ensureDrafts(bucket).create;
  const busy = mutationBusy(bucket, "create");
  return `<section class="settings-provider-section settings-card settings-page-section">
    <div class="settings-provider-title settings-card-title">${escapeHtml(t("skillsWorkbench.commands.createTitle"))}</div>
    <form class="skill-command-form" data-scoped-skill-create-form>
      <div class="settings-provider-form-grid settings-form-grid">
        <label>${escapeHtml(t("skillsWorkbench.commands.nameLabel"))}<input class="settings-field" data-scoped-create-field="command" value="${escapeAttr(draft.command || "")}" placeholder="${escapeAttr(t("skillsWorkbench.commands.namePlaceholder"))}" /></label>
        <label>${escapeHtml(t("skillsWorkbench.commands.descriptionLabel"))}<input class="settings-field" data-scoped-create-field="description" value="${escapeAttr(draft.description || "")}" /></label>
        <label class="settings-form-span-2">${escapeHtml(t("skillsWorkbench.commands.promptLabel"))}<textarea class="settings-field settings-textarea" rows="6" data-scoped-create-field="prompt">${escapeHtml(draft.prompt || "")}</textarea></label>
      </div>
      <button class="settings-action-btn primary" type="submit" ${busy ? "disabled" : ""}>${escapeHtml(busy ? t("skillsWorkbench.scoped.saving") : t("skillsWorkbench.commands.saveDisabled"))}</button>
    </form>
  </section>`;
}

export function effectiveOwnerRows(response) {
  const items = Array.isArray(response) ? response : Array.isArray(response?.items) ? response.items : [];
  return items.map((item) => item?.owner || item?.effectiveOwner || item?.skill || item).filter(Boolean).map((owner) => ({
    id: String(owner.id || ""),
    command: String(owner.command || owner.name || ""),
    description: String(owner.description || ""),
    scope: normalizeSkillContext({ scope: owner.scope }).scope,
    enabled: Boolean(owner.enabled),
    scanVerdict: String(owner.scanVerdict || ""),
    readOnly: true,
  }));
}

export function renderEffectiveCommandsView(response) {
  const rows = effectiveOwnerRows(response);
  return `<div class="skill-command-list settings-data-list" data-effective-commands-readonly>${rows.length ? rows.map((row) => `<div class="skill-command-card settings-card settings-data-list-row ${row.enabled ? "" : "disabled"}"><div><strong>${escapeHtml(row.command)}</strong> ${scopeBadge(row.scope)}</div><span class="settings-status-pill settings-badge muted">${escapeHtml(t("skillsWorkbench.scoped.readOnly"))}</span></div>`).join("") : `<div class="settings-empty-card settings-empty-state compact">${escapeHtml(t("skillsWorkbench.commands.scopeEmpty"))}</div>`}</div>`;
}

export function renderScopedSkillsManager({ controller, context } = {}) {
  const normalized = normalizeSkillContext(context);
  const availability = skillContextAvailability(normalized);
  if (!availability.available) {
    const key = availability.reason === "workspace" ? "skillsWorkbench.scoped.noWorkspace" : "skillsWorkbench.scoped.noProject";
    return `<section class="settings-provider-section settings-card settings-page-section" data-scoped-skills-empty="${escapeAttr(availability.reason)}"><div class="settings-empty-card settings-empty-state">${escapeHtml(t(key))}</div></section>`;
  }
  const bucket = controller.ensureContext(normalized);
  const loading = bucket.status === "loading";
  return `<section class="settings-provider-section settings-card settings-page-section highlighted" data-scoped-skills-manager data-skill-scope="${escapeAttr(normalized.scope)}">
    <div class="settings-provider-section-head settings-card-header"><div><div class="settings-provider-title settings-card-title">${escapeHtml(t("skillsWorkbench.scoped.title", { scope: contextLabel(normalized) }))}</div><div class="settings-provider-meta settings-card-description">${escapeHtml(t("skillsWorkbench.commands.snapshot", { sequence: String(bucket.snapshotSequence ?? "—") }))}</div></div><button class="settings-action-btn subtle" type="button" data-scoped-skills-refresh ${loading ? "disabled" : ""}>${escapeHtml(loading ? t("skillsWorkbench.commands.statusLoading") : t("skillsWorkbench.commands.refresh"))}</button></div>
    ${bucket.error ? `<div class="settings-inline-alert settings-alert" role="alert">${escapeHtml(bucket.error)}</div>` : ""}
    <div class="skill-command-list settings-data-list">${loading && !bucket.items.length ? `<div class="settings-empty-card settings-empty-state compact">${escapeHtml(t("skillsWorkbench.commands.loadingSkills"))}</div>` : bucket.items.length ? bucket.items.map((skill) => renderSkillCard(bucket, skill)).join("") : `<div class="settings-empty-card settings-empty-state compact">${escapeHtml(t("skillsWorkbench.commands.scopeEmpty"))}</div>`}</div>
    ${bucket.nextCursor ? `<button class="settings-action-btn subtle" type="button" data-scoped-skills-more>${escapeHtml(t("skillsWorkbench.commands.loadMore"))}</button>` : ""}
    ${renderRevisionPanel(bucket, normalized)}
    ${renderImport(bucket)}
    ${renderCreate(bucket)}
  </section>`;
}

function restoreReviewPrompt(error) {
  const body = error?.body;
  if (error?.status !== 409 || body?.code !== "skill_restore_review_required" || !body?.contentHash) return null;
  const findings = Array.isArray(body.scanFindings) && body.scanFindings.length
    ? body.scanFindings.map((finding) => `- ${finding?.code || "scan"}: ${finding?.message || t("skillsWorkbench.skills.findingDefault")}`).join("\n")
    : t("skillsWorkbench.restoreReview.noFindings");
  return {
    hash: String(body.contentHash),
    message: t("skillsWorkbench.restoreReview.prompt", { scannerVersion: body.scannerVersion || "—", hash: String(body.contentHash).slice(0, 16), findings }),
  };
}

export function bindScopedSkillsManager(root, { controller, context, confirmAction = (message) => globalThis.confirm?.(message), onError, onChange } = {}) {
  const normalized = normalizeSkillContext(context);
  if (!skillContextAvailability(normalized).available || !root) return () => {};
  const bucket = controller.ensureContext(normalized);
  const cleanups = [];
  const listen = (node, type, handler) => {
    if (!node?.addEventListener) return;
    node.addEventListener(type, handler);
    cleanups.push(() => node.removeEventListener?.(type, handler));
  };
  const act = (operation) => Promise.resolve().then(operation).catch((error) => onError?.(error));
  const one = (selector) => root.querySelector?.(selector);
  const all = (selector) => [...(root.querySelectorAll?.(selector) || [])];
  const changed = () => onChange?.();

  listen(one("[data-scoped-skills-refresh]"), "click", () => act(() => controller.load(normalized)));
  listen(one("[data-scoped-skills-more]"), "click", () => act(() => controller.loadMore(normalized)));
  all("[data-scoped-skill-detail]").forEach((node) => listen(node, "click", () => act(() => controller.loadDetail(node.dataset.scopedSkillDetail, normalized))));
  all("[data-scoped-skill-edit]").forEach((node) => listen(node, "click", () => {
    const skill = bucket.items.find((item) => item.id === node.dataset.scopedSkillEdit);
    if (!skill) return;
    editDraft(bucket, skill);
    bucket.editingSkillId = skill.id;
    changed();
  }));
  all("[data-scoped-skill-edit-cancel]").forEach((node) => listen(node, "click", () => {
    const id = node.dataset.scopedSkillEditCancel;
    delete ensureDrafts(bucket).edits[id];
    if (bucket.editingSkillId === id) bucket.editingSkillId = "";
    changed();
  }));
  all("[data-scoped-skill-edit-form]").forEach((form) => {
    const id = form.dataset.scopedSkillEditForm;
    [...(form.querySelectorAll?.("[data-scoped-edit-field]") || [])].forEach((field) => listen(field, "input", () => {
      const skill = bucket.items.find((item) => item.id === id);
      if (skill) editDraft(bucket, skill)[field.dataset.scopedEditField] = field.value;
    }));
    listen(form, "submit", (event) => {
      event.preventDefault?.();
      act(async () => {
        const skill = bucket.items.find((item) => item.id === id);
        const draft = ensureDrafts(bucket).edits[id];
        if (!skill || !draft) return;
        if (!String(draft.command || "").trim() || !String(draft.prompt || "").trim()) throw new Error(t("skillsWorkbench.errors.commandAndPromptRequired"));
        const result = await controller.updateSkill(id, { ...draft, expectedUpdatedAt: skill.updatedAt }, normalized);
        if (result) {
          delete bucket.drafts.edits[id];
          bucket.editingSkillId = "";
          changed();
        }
      });
    });
  });
  all("[data-scoped-skill-toggle]").forEach((node) => listen(node, "click", () => act(async () => {
    const skill = bucket.items.find((item) => item.id === node.dataset.scopedSkillToggle);
    if (!skill) return;
    let acknowledgeRisk = false;
    if (!skill.enabled && skill.scanVerdict === "review") {
      acknowledgeRisk = Boolean(await confirmAction(t("skillsWorkbench.confirmation.enableReview")));
      if (!acknowledgeRisk) return;
    }
    await controller.toggleSkill(skill.id, !skill.enabled, normalized, { acknowledgeRisk, expectedUpdatedAt: skill.updatedAt });
  })));
  all("[data-scoped-skill-delete]").forEach((node) => listen(node, "click", () => act(async () => {
    const skill = bucket.items.find((item) => item.id === node.dataset.scopedSkillDelete);
    if (!skill || !await confirmAction(t("skillsWorkbench.confirmation.deleteSkill", { command: skill.command }))) return;
    await controller.deleteSkill(skill.id, normalized, { expectedUpdatedAt: skill.updatedAt });
  })));
  all("[data-scoped-skill-revisions]").forEach((node) => listen(node, "click", () => act(async () => {
    bucket.drawer = { skillId: node.dataset.scopedSkillRevisions, selectedRevision: "", revisionDetail: null, revisionDetailError: "" };
    changed();
    await controller.loadRevisions(bucket.drawer.skillId, normalized);
  })));
  listen(one("[data-scoped-revisions-close]"), "click", () => { bucket.drawer = null; changed(); });
  listen(one("[data-scoped-revisions-more]"), "click", () => act(() => controller.loadRevisions(bucket.drawer?.skillId, normalized, { append: true })));
  all("[data-scoped-revision-detail]").forEach((node) => listen(node, "click", () => act(async () => {
    if (!bucket.drawer) return;
    const drawer = bucket.drawer;
    const requestSequence = drawer.detailRequestSequence = Number(drawer.detailRequestSequence || 0) + 1;
    const detail = await controller.loadRevisionDetail(drawer.skillId, node.dataset.scopedRevisionDetail, normalized);
    if (bucket.drawer !== drawer || requestSequence !== drawer.detailRequestSequence) return;
    drawer.revisionDetail = detail;
    drawer.selectedRevision = node.dataset.scopedRevisionDetail;
    changed();
  })));
  all("[data-scoped-revision-restore]").forEach((node) => listen(node, "click", () => act(async () => {
    const skill = bucket.items.find((item) => item.id === bucket.drawer?.skillId);
    const revision = bucket.revisions?.[skill?.id]?.items?.find((item) => String(item.revisionNo ?? item.revision) === node.dataset.scopedRevisionRestore);
    if (!skill || !revision || !await confirmAction(t("skillsWorkbench.confirmation.restoreRevision"))) return;
    try {
      await controller.restoreRevision(skill.id, revision, normalized, { expectedUpdatedAt: skill.updatedAt });
    } catch (error) {
      const review = restoreReviewPrompt(error);
      if (!review || !await confirmAction(review.message)) throw error;
      await controller.restoreRevision(skill.id, revision, normalized, { expectedUpdatedAt: skill.updatedAt, acknowledgeRisk: true, acknowledgedContentHash: review.hash });
    }
  })));

  const createForm = one("[data-scoped-skill-create-form]");
  all("[data-scoped-create-field]").forEach((field) => listen(field, "input", () => { ensureDrafts(bucket).create[field.dataset.scopedCreateField] = field.value; }));
  listen(createForm, "submit", (event) => {
    event.preventDefault?.();
    act(async () => {
      const draft = ensureDrafts(bucket).create;
      if (!String(draft.command || "").trim() || !String(draft.prompt || "").trim()) throw new Error(t("skillsWorkbench.errors.commandAndPromptRequired"));
      const result = await controller.createSkill({ ...draft, name: draft.name || draft.command, source: "manual", enabled: false }, normalized);
      if (result) { bucket.drafts.create = {}; changed(); }
    });
  });
  listen(one("[data-scoped-skill-import-file]"), "change", (event) => act(async () => {
    const input = event?.target;
    const file = input?.files?.[0];
    if (input) input.value = "";
    if (!file) return;
    if (file.size > MAX_SKILL_FILE_BYTES) throw new Error(t("skillsWorkbench.errors.fileTooLarge"));
    const requestSequence = bucket.importFileReadSequence = Number(bucket.importFileReadSequence || 0) + 1;
    const content = await file.text();
    if (requestSequence !== bucket.importFileReadSequence) return;
    ensureDrafts(bucket).importContent = content;
    await controller.previewImport(content, normalized);
  }));
  listen(one("[data-scoped-skill-import-confirm]"), "click", () => act(async () => {
    const content = ensureDrafts(bucket).importContent;
    const preview = bucket.importPreview?.value;
    if (!content || !preview) throw new Error(t("skillsWorkbench.errors.selectAndPreview"));
    if (!await confirmAction(t("skillsWorkbench.confirmation.importSkill", { command: preview.command || preview.name || "", verdict: preview.scanVerdict || "safe" }))) return;
    const result = await controller.importSkill(content, normalized, { enabled: false });
    if (result) { bucket.drafts.importContent = ""; changed(); }
  }));

  return () => cleanups.splice(0).forEach((cleanup) => cleanup());
}

export function createScopedSkillsSettings({ controller, getContext, context, confirmAction, onError, onChange } = {}) {
  const currentContext = () => normalizeSkillContext(getContext?.() || context || { scope: "global" });
  return {
    render: () => renderScopedSkillsManager({ controller, context: currentContext() }),
    bind: (root) => bindScopedSkillsManager(root, { controller, context: currentContext(), confirmAction, onError, onChange }),
    load: () => skillContextAvailability(currentContext()).available ? controller.load(currentContext()) : Promise.resolve(null),
  };
}
