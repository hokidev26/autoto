// Compatibility facade: model-provider-settings.mjs used to hold the entire model
// provider settings implementation (~4,300 lines). It has been split by domain into
// dedicated modules; this file only re-exports their public surface so existing
// `import ... from "./model-provider-settings.mjs"` call sites keep working unchanged.
//
//   provider-settings-normalization.mjs  - validation / normalize / request builders
//   provider-account-rendering.mjs       - Codex + Anthropic account table & quota rendering
//   provider-codex-auth.mjs              - Codex import/export, browser login, batch ops
//   provider-anthropic-accounts.mjs      - Anthropic account management
//   model-routing-settings.mjs           - default/agent-role models, aggregates, reasoning effort
//   provider-console.mjs                 - provider console controller (draft/focus/events/CRUD)
export {
  anthropicAccountActionRequest,
  anthropicAccountCreateRequest,
  anthropicAccountsListRequest,
  anthropicProfileLoginCommand,
  automaticProviderNameUpdate,
  codexAccountActionRequest,
  codexAccountBatchRequest,
  codexAccountExportFilename,
  codexBrowserLoginRequest,
  codexDeleteResultWarning,
  codexImportBatchRequest,
  codexMutationRefreshWarning,
  consumeAnthropicAccountCreateRequest,
  isProviderConsoleInteractiveTarget,
  markProviderModelsStale,
  normalizeCodexAccountList,
  normalizeCodexBatchResult,
  normalizeCodexBrowserLoginStatus,
  normalizeCodexImportBatchResult,
  normalizeCodexSelectedIds,
  normalizeAnthropicAccountList,
  providerConnectionFingerprint,
  providerConsoleDraftFromForm,
  providerNameFromBaseURL,
  providerConsoleFocusableElements,
  providerModelDiscovery,
  providerPreflightResult,
  providerSensitiveDraftAccessAllowed,
  redactedProviderProxyURL,
  restoreProviderConsoleFocus,
  selectProviderConsoleFieldOnFocus,
  shouldOpenProviderCardFromKeyboard,
  syncProviderConsoleDraft,
  trapProviderConsoleFocus,
  trustedCodexBrowserAuthURL,
  validateCodexImportJSON,
  validateProviderNameValue,
} from "./provider-settings-normalization.mjs";

export {
  agentModelRoles,
  agentModelSettingsPayload,
  defaultReasoningEffortValues,
  isAgentModelReference,
  modelAggregateActionRequest,
  modelAggregateMembers,
  normalizeAgentModelSettings,
  normalizeDefaultReasoningEffort,
  normalizeModelAggregateList,
  runtimeReasoningSettingsRequest,
} from "./model-routing-settings.mjs";

export {
  anthropicAccountOverview,
  anthropicAccountStatus,
  codexAccountOverview,
  codexAccountStatus,
  codexAccountUsageWindows,
  renderAnthropicAccountManagementTable,
  renderCodexAccountManagementTable,
} from "./provider-account-rendering.mjs";

export { createModelProviderSettingsController } from "./provider-console.mjs?v=native-image-generation-1-provider-auto-name-1";
