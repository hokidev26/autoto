export function createAppMainStreamWiring({
  $,
  am,
  t,
  state,
  normalizeWorkStateSnapshot,
  contextManagement,
  backgroundTasks,
  executionNotifications,
  renderConversationHeaderIdentity,
  syncNavigationConversationFromAgent,
  navigationRefresh,
  clearLiveAssistantText,
  clearLiveImageGenerations,
  clearRunSummary,
  replacePlanState,
  replacePendingApprovals,
  replacePendingUserQuestions,
  applyMessageSnapshot,
  normalizeStoredPermissionMode,
  enforcePermissionSelectCap,
  renderModelOptions,
  refreshReasoningEffortControl,
  refreshFastModeControl,
  refreshMessageModeControl,
  updateWorkspaceMetaPills,
  renderWorkbenchShell,
  syncMessageComposerBusy,
  refreshComposerActivityStatus,
  loadRunSummary,
  loadLatestRunSummary,
  notifyTerminal,
  shouldLogAgentEvents,
  appendTerminal,
  applyPlanEvent,
  rememberAssistantToolOwner,
  showToast,
  clearAgentRunError,
  clearBlockedToolNotices,
  beginLiveAssistantGeneration,
  appendLiveReasoning,
  closeLiveReasoningStep,
  appendLiveAssistantText,
  updateLiveAssistantPerformance,
  rememberImageGenerationStatus,
  rememberToolStarted,
  appendToolOutput,
  appendToolInputDelta,
  rememberToolApproval,
  rememberUserQuestion,
  clearToolApproval,
  clearUserQuestion,
  finishToolOutput,
  clearCurrentAgentApprovals,
  rememberAgentRunError,
  scheduleMessageRefresh,
  clearLiveToolOutputs,
} = {}) {
async function applyAgentLiveSnapshot(snapshot, detail = {}) {
  const agentId = snapshot?.agent?.id || "";
  if (!agentId || state.agent?.id !== agentId) return;
  let nextAgent = snapshot.agent;
  const pendingModel = (state.agentSavePending || state.agentSaving)
    && state.agentSaveSnapshot?.agentId === agentId
    ? String(state.agentSaveSnapshot?.model || "").trim()
    : "";
  const currentGeneration = Number(state.agent?.entityGeneration);
  const nextGeneration = Number(nextAgent?.entityGeneration);
  const snapshotIsOlder = Number.isSafeInteger(currentGeneration)
    && Number.isSafeInteger(nextGeneration)
    && nextGeneration < currentGeneration;
  // A model PATCH bumps entityGeneration. An interrupt resync that left before
  // that write must not put the previous model back onto the agent, or the
  // next send/continue still runs on the old provider.
  if (pendingModel) {
    nextAgent = { ...nextAgent, model: pendingModel };
  } else if (snapshotIsOlder) {
    nextAgent = {
      ...nextAgent,
      model: state.agent.model,
      reasoningEffort: state.agent.reasoningEffort,
      fastMode: state.agent.fastMode,
      permissionMode: state.agent.permissionMode,
      planMode: state.agent.planMode,
      entityGeneration: currentGeneration,
    };
  }
  const nextWorkState = normalizeWorkStateSnapshot(snapshot);
  state.agent = nextAgent;
  // A snapshot is the authoritative server state, and neither of these is part of
  // it: both are local guesses driven purely by live events. Keeping them across a
  // resync is what let a dropped compaction_finished or retry-cleared event pin the
  // composer to a status the server no longer reports.
  state.contextCompacting = false;
  state.providerRetry = null;
  contextManagement.applyStatus(snapshot.context || {}, { agentId });
  state.workState = nextWorkState;
  backgroundTasks.applySnapshot(snapshot, { agentId });
  if (detail.source === "initial") await executionNotifications.initial(snapshot, { agentId });
  else await executionNotifications.snapshot(snapshot, { agentId });
  renderConversationHeaderIdentity();
  syncNavigationConversationFromAgent(state.agent, { reason: "agent-snapshot" });
  navigationRefresh.request("agent-snapshot");
  // applyMessageSnapshot at the end of this function rebuilds the whole
  // transcript -- live cards, run summary, plans and approvals included -- so
  // every clear here is painting an intermediate state that is about to be
  // overwritten. Doing it unguarded is what makes opening or switching a
  // conversation flash: the view empties, then fills.
  clearLiveAssistantText({ preserveView: true });
  clearLiveImageGenerations({ agentId, preserveView: true });
  const recoveredToolOutputs = Object.fromEntries(Object.entries(state.liveToolOutputs || {}).filter(([, value]) => value?.agentId && value.agentId !== agentId));
  for (const call of Array.isArray(snapshot.toolActivity) ? snapshot.toolActivity : []) {
    const toolUseId = String(call?.toolUseId || call?.tool_use_id || "").trim();
    if (toolUseId) recoveredToolOutputs[toolUseId] = { ...call, agentId, toolUseId };
  }
  state.liveToolOutputs = recoveredToolOutputs;
  clearRunSummary({ preserveView: true });
  replacePlanState(snapshot.activePlan, snapshot.pendingPlanApproval ?? snapshot.pendingPlan, agentId, snapshot.plans);
  replacePendingApprovals(snapshot.pendingApprovals, agentId);
  replacePendingUserQuestions(snapshot.pendingUserQuestions, agentId);
  applyMessageSnapshot(snapshot.messages, agentId, {
    hasMoreBefore: snapshot.messageHasMoreBefore,
    nextBefore: snapshot.messageNextBefore,
  });
  // Re-render the heading now that the messages exist: an untitled conversation
  // takes its title from the first user message, which was not yet loaded when
  // the header rendered above.
  renderConversationHeaderIdentity();
  const permissionMode = $("permissionMode");
  if (permissionMode) permissionMode.value = normalizeStoredPermissionMode(state.agent.permissionMode) || "acceptEdits";
  enforcePermissionSelectCap();
  renderModelOptions();
  refreshReasoningEffortControl();
  refreshFastModeControl();
  refreshMessageModeControl();
  updateWorkspaceMetaPills();
  renderWorkbenchShell();
  syncMessageComposerBusy();
  refreshComposerActivityStatus();
  const latestRun = snapshot.latestRun;
  if (latestRun?.id && ["completed", "error", "failed", "interrupted", "superseded"].includes(latestRun.status)) {
    loadRunSummary(latestRun.id, { agentId }).catch((error) => notifyTerminal(`[warn] ${am("runSummaryRestoreFailed", { message: error?.message || error })}\n`));
  }
}

async function handleAgentStreamEvent(event) {
  const agentId = state.agent?.id || "";
  if (!agentId || (event.agentId && event.agentId !== agentId)) return;
  backgroundTasks.handleEvent(event);
  await executionNotifications.live(event, { agentId });
  if (shouldLogAgentEvents()) appendTerminal(`[event] ${event.type}${event.text ? `: ${event.text}` : ""}\n`);
  applyPlanEvent(event);
  if (event.type === "context.updated") {
    const contextUpdate = event.data?.context || event.data?.status || event.data || {};
    // A compacted context.updated is the server saying the work landed, and it is
    // the only completion signal the manual endpoint sends on the path that
    // rewrites the summary. Treating it as terminal means a lost or filtered
    // compaction_finished cannot leave the notice spinning after the fact.
    if (contextUpdate.compacted || event.data?.compacted) state.contextCompacting = false;
    if (Number.isInteger(Number(contextUpdate.entityGeneration))) {
      state.agent = {
        ...state.agent,
        entityGeneration: Number(contextUpdate.entityGeneration),
        prunedPercent: Number(contextUpdate.prunedPercent) || state.agent?.prunedPercent || 0,
        pruneEnabled: contextUpdate.pruneEnabled ?? state.agent?.pruneEnabled,
      };
    }
    contextManagement.applyStatus(contextUpdate, { agentId, partial: true });
  }
  if (event.type === "agent.title_updated") {
    const generatedTitle = String(event.data?.title || "").trim();
    const generation = Number(event.data?.entityGeneration);
    // An open title editor is the user's in-flight intent, so the server's
    // generated name must not overwrite what they are currently typing.
    if (generatedTitle && !state.titleEditing && !state.titleSaving) {
      state.agent = { ...state.agent, title: generatedTitle, entityGeneration: Number.isInteger(generation) ? generation : state.agent?.entityGeneration };
      state.worklineAgents = (state.worklineAgents || []).map((agent) => agent.id === agentId ? { ...agent, title: generatedTitle } : agent);
      syncNavigationConversationFromAgent(state.agent, { reason: "agent-title" });
      renderConversationHeaderIdentity();
      renderWorkbenchShell();
    }
  }
  const runId = event.data?.runId || "";
  const requestId = event.data?.requestId || "";
  // The assistant turn is persisted and announced before its tools run, so this
  // is the last message id seen when tool.started arrives -- i.e. the owner of
  // whatever activity follows.
  if (event.type === "message.created") rememberAssistantToolOwner(event.messageId || event.data?.messageId);
  const completedMessageEvents = ["message.created", "message.completed"];
  const terminalAgentEvents = ["agent.done", "agent.error", "agent.interrupted"];
  const navigationRefreshEvents = ["agent.started", ...completedMessageEvents, ...terminalAgentEvents];
  // An upstream fault is now retried automatically, so say so. Without this the
  // run looks frozen for the length of the backoff and the user cannot tell a
  // retry from a hang.
  // A long-running segment reports itself so a wedged run is distinguishable
  // from a finished one. Report-only: the run is not cancelled.
  if (event.type === "agent.stalled") {
    const elapsed = Number(event.data?.elapsedSeconds || 0);
    notifyTerminal(`[warn] ${am("agentStalled", { minutes: Math.max(1, Math.round(elapsed / 60)) })}\n`);
  }
  if (event.type === "agent.provider_error_retry") {
    const attempt = Number(event.data?.attempt || 0);
    const maxAttempts = Number(event.data?.maxAttempts || 0);
    const detail = String(event.data?.error || "").trim();
    // Drives the composer status so the wait between attempts reads as "retrying"
    // instead of an idle conversation. Cleared when the next attempt produces
    // anything or the run reaches a terminal state.
    //
    // maxAttempts stays 0 when the retry ceiling is unlimited. Forcing it to 1
    // there produced "retrying 5/1", so the absent total is preserved and the
    // status line drops the fraction instead of inventing a wrong one.
    state.providerRetry = { attempt: attempt || 1, maxAttempts: maxAttempts > 0 ? maxAttempts : 0, at: Date.now() };
    refreshComposerActivityStatus();
    notifyTerminal(`[warn] ${maxAttempts > 0
      ? am("providerErrorRetry", {
        attempt: attempt || 1,
        maxAttempts,
        message: detail || am("providerErrorRetryUnknown"),
      })
      : am("providerErrorRetryUnlimited", {
        attempt: attempt || 1,
        message: detail || am("providerErrorRetryUnknown"),
      })}\n`);
  }
  if (event.type === "context.compaction_started") {
    // Scoped to the agent, so switching conversations cannot leave an unrelated
    // composer stuck on "compacting". A manual compaction has no run behind it,
    // so no terminal agent event arrives later to clear a stray flag.
    state.contextCompacting = event.agentId || agentId || true;
    refreshComposerActivityStatus();
  }
  if (event.type === "context.compaction_finished") {
    state.contextCompacting = false;
    // modelSummary === false means the summary model call failed and this
    // compaction shipped the low-fidelity local fallback; true or a missing
    // field is the normal path and stays silent.
    if (event.data?.modelSummary === false) showToast(t("context.compactionDegraded"), "warn");
    refreshComposerActivityStatus();
  }
  if (event.type === "agent.started") {
    state.agent = { ...state.agent, status: "running" };
    syncNavigationConversationFromAgent(state.agent, { status: "running", reason: "agent-started" });
    // A new turn supersedes the previous failure, so the old reason must not sit
    // above a run that is already under way.
    clearAgentRunError(agentId);
    clearBlockedToolNotices(agentId);
    clearRunSummary();
    clearLiveAssistantText();
    syncMessageComposerBusy();
    refreshComposerActivityStatus();
  }
  if (event.type === "agent.waiting") {
    // The run parked on a subagent boundary: the conversation is not over, it
    // is waiting for the child's report. The sidebar and composer both say so
    // instead of spinning as if the model were still generating.
    state.agent = { ...state.agent, status: "waiting" };
    syncNavigationConversationFromAgent(state.agent, { status: "waiting", reason: "agent-waiting" });
    syncMessageComposerBusy();
    refreshComposerActivityStatus();
  }
  if (event.type === "model.started") {
    // The retry produced a live model call, so the retry wait is over.
    state.providerRetry = null;
    beginLiveAssistantGeneration({
      requestId,
      runId,
      provider: event.data?.provider,
      model: event.data?.model,
      startedAt: event.data?.startedAt,
    });
    refreshComposerActivityStatus();
  }
  if (event.type === "agent.reasoning") {
    appendLiveReasoning({ ...event, data: { ...(event.data || {}), runId } });
    refreshComposerActivityStatus();
  }
  if (event.type === "agent.text") {
    // The model has stopped planning and started answering, so whatever it
    // was reasoning about is a finished step.
    closeLiveReasoningStep();
    appendLiveAssistantText(event.text || event.data?.text || "", { requestId, runId });
    refreshComposerActivityStatus();
  }
  if (event.type === "model.streaming") {
    updateLiveAssistantPerformance(event.data?.pendingThroughput, { requestId, runId });
  }
  if (event.type === "model.completed") {
    const throughput = event.data?.throughput && typeof event.data.throughput === "object" ? { ...event.data.throughput } : {};
    if (throughput.ttftMs == null && event.data?.ttftMs != null) throughput.ttftMs = event.data.ttftMs;
    updateLiveAssistantPerformance(throughput, { requestId, runId, replace: true });
  }
  if (event.type === "image_generation.status") {
    rememberImageGenerationStatus(event);
    refreshComposerActivityStatus();
  }
  if (event.type === "tool.started") {
    rememberToolStarted(event);
    refreshComposerActivityStatus();
  }
  if (event.type === "tool.output") appendToolOutput(event);
  // Argument text streamed while the model composes a Write/Edit call; the
  // card exists before tool.started so the content can be followed live.
  if (event.type === "tool.input_delta") appendToolInputDelta(event);
  if (event.type === "tool.approval_required") {
    rememberToolApproval(event);
    showToast(event.data?.risk === "danger" ? t("workspace.chat.dangerousToolBlocked") : t("workspace.chat.toolApproval"), event.data?.risk === "danger" ? "error" : "warn");
    refreshComposerActivityStatus();
  }
  if (event.type === "user.question_required") {
    rememberUserQuestion(event);
    showToast(t("workspace.chat.userQuestionRequired"), "warn");
    refreshComposerActivityStatus();
  }
  if (event.type === "tool.finished") {
    clearToolApproval(event.data?.toolUseId);
    clearUserQuestion(event.data?.toolUseId);
    finishToolOutput(event);
    refreshComposerActivityStatus();
  }
  if (event.type === "agent.interrupted") {
    clearCurrentAgentApprovals();
    // Stopping is the reader's own decision, so it needs no explanation. Refusals
    // the agent already routed around are not why this run ended.
    clearBlockedToolNotices(event.agentId || state.agent?.id);
  }
  // A run that finished its answer routed around every refusal it hit, so those
  // are steps, not the outcome -- surfaced under a successful reply they read
  // as "something went wrong at the end". Only a failed stop keeps them: there
  // the refusal may be exactly why the run ended, and agent.error is the one
  // terminal event that does not clear.
  if (event.type === "agent.done") clearBlockedToolNotices(event.agentId || state.agent?.id);
  // Keep the reason the moment it arrives. Everything downstream of here depends
  // on a fetch that may return nothing, so this is the only copy guaranteed to
  // exist for a run that failed before it was ever recorded.
  if (event.type === "agent.error") rememberAgentRunError({ ...event, agentId });
  if (terminalAgentEvents.includes(event.type)) {
    // Belt and braces: a run that ends during a retry or a compaction must not
    // leave the composer claiming either is still happening.
    state.providerRetry = null;
    state.contextCompacting = false;
    const status = event.type === "agent.error" ? "error" : "idle";
    state.agent = { ...state.agent, status };
    syncNavigationConversationFromAgent(state.agent, { status, reason: event.type });
  }
  if ([...completedMessageEvents, ...terminalAgentEvents].includes(event.type)) {
    // preserveView because the persisted message has not arrived yet. Rendering
    // the cleared state now tears the streamed answer out of the transcript,
    // the page collapses by its height, and 80ms later loadMessages puts an
    // equivalent block back -- a visible jolt at the end of every single turn.
    // The state is cleared either way; only the repaint waits for the refresh
    // that is about to replace this content anyway.
    clearLiveAssistantText({ preserveView: true });
    clearLiveImageGenerations({ agentId, preserveView: true });
    // Keep live tool and reasoning records visible until the persisted run
    // summary successfully takes ownership of them. The composer separately
    // treats terminal live records as display-only, so retaining them here no
    // longer leaves the send button stuck in Stop mode.
    syncMessageComposerBusy();
    refreshComposerActivityStatus();
    // The context readout and its panel are fed by context.updated, and every
    // place that publishes it does so only when the context was compacted or
    // explicitly managed -- never for an ordinary turn. So the numbers stayed
    // frozen at whatever they were when the conversation was opened, and only a
    // reload appeared to fix them. A finished turn is exactly when the token
    // count has changed, so re-read it here. Guarded on the agent still being
    // the open one so a background conversation cannot overwrite the panel.
    if (state.agent?.id === agentId) contextManagement.load().catch(() => {});
  }
  if ([...completedMessageEvents, ...terminalAgentEvents].includes(event.type)) scheduleMessageRefresh(80, agentId);
  // Another client rolled back, deleted, or forked: refresh this transcript
  // (and navigation, where a fork shows up as a new conversation).
  if (["message.rollback", "message.deleted"].includes(event.type)) scheduleMessageRefresh(80, agentId);
  if (event.type === "message.forked") navigationRefresh.request(event.type);
  if (navigationRefreshEvents.includes(event.type)) navigationRefresh.request(event.type);
  if (["agent.error", "agent.interrupted"].includes(event.type)) {
    const restore = runId ? loadRunSummary(runId, { agentId }) : loadLatestRunSummary(agentId);
    void restore.then((summary) => {
      if (!summary || state.agent?.id !== agentId) return;
      // The persisted outcome now owns these records; remove the live copy so
      // the same tool calls are not shown twice.
      clearLiveToolOutputs({ agentId });
      // Records can arrive between the completion event and this callback, and
      // clearLiveToolOutputs does not touch the composer, so the button state
      // has to be recomputed against what is left.
      syncMessageComposerBusy();
    }).catch((error) => notifyTerminal(`[warn] ${am("runSummaryRestoreFailed", { message: error?.message || error })}\n`));
  }
  // Successful runs: also eagerly persist tool history so it survives before
  // the next user message triggers pruneLiveToolOutputs and wipes the live view.
  if (event.type === "agent.done" && runId) {
    const restore = loadRunSummary(runId, { agentId });
    void restore.then((summary) => {
      if (!summary || state.agent?.id !== agentId) return;
      clearLiveToolOutputs({ agentId });
      syncMessageComposerBusy();
    }).catch(() => {});
  }
}

  return { applyAgentLiveSnapshot, handleAgentStreamEvent };
}
