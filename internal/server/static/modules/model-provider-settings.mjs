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
  normalizeSubscriptionAccountList,
  normalizeSubscriptionLoginStatus,
  normalizeSubscriptionProvider,
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
  subscriptionAccountActionRequest,
  subscriptionAccountsListRequest,
  subscriptionOAuthLoginRequest,
  syncProviderConsoleDraft,
  trapProviderConsoleFocus,
  trustedCodexBrowserAuthURL,
  trustedSubscriptionAuthURL,
  validateCodexImportJSON,
  validateProviderNameValue,
} from "./provider-settings-normalization.mjs?v=provider-hidden-models-1";

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
  renderSubscriptionAccountManagementTable,
  providerAccountQuotaSummary,
  subscriptionAccountModelQuotas,
  subscriptionAccountOverview,
  subscriptionAccountQuotaBudgets,
  subscriptionAccountStatus,
} from "./provider-account-rendering.mjs?v=provider-quota-overview-1-codex-quota-exhausted-1";

export {
  isSubscriptionAccountProvider,
  subscriptionProviderKind,
  subscriptionProviderKinds,
  subscriptionProviderSpec,
  subscriptionProviderSpecs,
} from "./model-provider-components.mjs?v=provider-hidden-models-1-provider-quota-overview-1";

export { createSubscriptionAccountsController } from "./provider-subscription-accounts.mjs?v=provider-quota-overview-1-codex-quota-exhausted-1";

export { createModelProviderSettingsController } from "./provider-console.mjs?v=native-image-generation-1-provider-auto-name-1-provider-hidden-models-1-provider-quota-overview-1-codex-quota-exhausted-1";
