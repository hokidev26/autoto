#!/usr/bin/env bash
set -euo pipefail

repo_root="$(git rev-parse --show-toplevel)"
cd "$repo_root"

readonly default_deadcode_version="v0.48.0"
readonly deadcode_version="${DEADCODE_VERSION:-$default_deadcode_version}"
readonly output_file="$(mktemp)"
readonly allowed_file="$(mktemp)"
readonly actual_file="$(mktemp)"
readonly allowed_sorted_file="$(mktemp)"
readonly actual_sorted_file="$(mktemp)"
trap 'rm -f "$output_file" "$allowed_file" "$actual_file" "$allowed_sorted_file" "$actual_sorted_file"' EXIT

# Exact, reviewed exceptions. Keys deliberately include the source file so
# identically named package entry points cannot hide a newly unreachable symbol.
readonly -a allowed_findings=(
  # The six entries below were already unreachable but stayed hidden while comm
  # ran under a non-C locale and bailed out before comparing (see the LC_ALL note
  # further down). They are the same kind of exception as the rest of this list:
  # documented convenience wrappers, public API accessors, and a test helper.
  "internal/agent/danger_reflection.go::Runner.dangerReflectionPreferred"
  "internal/kiroauth/kiro.go::RefreshLead"
  "internal/peercontrol/invitation.go::EncodeInvitation"
  "internal/peercontrol/scope.go::AllScopes"
  "internal/providers/kiro_provider.go::newKiroProviderForTest"
  "internal/providers/provider_runtime.go::normalizeReasoningEffort"
  "internal/automation/manager.go::New"
  "internal/channels/types.go::WithAPIBase"
  "internal/channels/types.go::WithHTTPClient"
  "internal/channels/types.go::WithRefreshInterval"
  "internal/channels/types.go::WithLongPollTimeout"
  "internal/channels/types.go::WithRequestTimeout"
  "internal/channels/types.go::WithRetryDelay"
  "internal/channels/types.go::WithRateLimit"
  "internal/channels/types.go::WithClock"
  "internal/channels/types.go::WithAudit"
  "internal/channels/types.go::WithErrorHandler"
  "internal/codexauth/store.go::publicCredentialID"
  "internal/app/runtime.go::Runtime.GatewayAddr"
  "internal/app/runtime.go::Runtime.GatewayStatus"
  "internal/db/background_tasks.go::parseBackgroundTaskTime"
  "internal/db/store.go::WrapNotFound"
  "internal/db/learned_features.go::sortedKeys"
  "internal/devices/action.go::Catalog"
  "internal/devices/client.go::NewHomeAssistantAdapter"
  "internal/gitsnapshot/gitsnapshot.go::FingerprintBudget.TotalBytes"
  "internal/gitsnapshot/gitsnapshot.go::FingerprintReader"
  "internal/plugins/service.go::WithTimeout"
  "internal/providers/anthropic_provider.go::emitAnthropicToolCall"
  "internal/providers/codex_models.go::parseCodexModels"
  "internal/review/review.go::ParseVerdict"
  "internal/secrets/secrets.go::Parse"
  "internal/server/agent.go::boundedActivityInput"
  "internal/server/agent.go::activityInputFieldBytes"
  "internal/server/agent.go::boundedActivityInputValue"
  "internal/server/agent.go::addBoundedActivityInputField"
  "internal/server/execution_control.go::Server.setProjectDeviceGrant"
  "internal/server/http_test_helpers_test.go::markLoopbackProxyHTTPS"
  "internal/server/model_runtime.go::Server.upsertModelAggregate"
  "internal/server/model_runtime.go::Server.patchRuntimeModelSettings"
  "internal/server/model_runtime.go::Server.updateAgentReasoning"
  "internal/server/model_runtime.go::Server.getClientIdentity"
  "internal/server/model_runtime.go::Server.rotateInstallationID"
  "internal/server/provider_auth.go::Server.listCLIProxyAPIAuthFiles"
  "internal/server/provider_auth.go::Server.importCLIProxyAPIAuthFile"
  "internal/server/provider_auth.go::Server.cliProxyAPIManagementRequest"
  "internal/server/provider_auth.go::Server.cliProxyAPIManagementBaseURL"
  "internal/server/provider_auth.go::Server.cliProxyAPIProviderSummary"
  "internal/server/provider_auth.go::cliProxyAPIManagementKey"
  "internal/server/provider_config.go::distinctModelCount"
  "internal/server/remote_access.go::Server.filesystemPathAllowedForRequest"
  "internal/server/security.go::Server.remoteHardeningActive"
  "internal/server/skills_v2.go::marshalSkillJSON"
  "internal/themes/manifest.go::LoadManifest"
  "internal/themes/store.go::New"
  "internal/update/manifest.go::Validate"
  "internal/update/version.go::NormalizeVersion"
)

run_deadcode() {
  if go tool -n deadcode >/dev/null 2>&1; then
    go tool deadcode -test ./...
    return
  fi
  go run "golang.org/x/tools/cmd/deadcode@${deadcode_version}" -test ./...
}

printf '==> Running deadcode %s (-test ./...)\n' "$deadcode_version"
if ! run_deadcode >"$output_file" 2>&1; then
  while IFS= read -r line; do
    printf '%s\n' "$line"
  done <"$output_file"
  exit 1
fi

printf '%s\n' "${allowed_findings[@]}" >"$allowed_file"
while IFS= read -r line; do
  printf '%s\n' "$line"
  if [[ "$line" != *": unreachable func: "* ]]; then
    continue
  fi
  location="${line%%: unreachable func: *}"
  path="${location%%:*}"
  # deadcode prints native separators, so on Windows every key would arrive as
  # internal\pkg\file.go and match none of the forward-slash exceptions below,
  # reporting the whole allowlist as both unexpected and stale.
  path="${path//\\//}"
  symbol="${line##*: unreachable func: }"
  printf '%s\n' "${path}::${symbol}" >>"$actual_file"
done <"$output_file"
LC_ALL=C sort -u "$allowed_file" >"$allowed_sorted_file"
LC_ALL=C sort -u "$actual_file" >"$actual_sorted_file"

# comm has to use the same collation the sorts above used. Without LC_ALL=C it
# applies the caller's locale, decides the C-sorted input is unsorted, and prints
# "file 1 is not in sorted order" instead of comparing. That made this script
# fail on any machine whose locale is not C (for example zh_TW.UTF-8).
unexpected="$(LC_ALL=C comm -13 "$allowed_sorted_file" "$actual_sorted_file")"
stale="$(LC_ALL=C comm -23 "$allowed_sorted_file" "$actual_sorted_file")"
if [[ -n "$unexpected" ]]; then
  printf 'Unexpected dead code:\n%s\n' "$unexpected" >&2
fi
if [[ -n "$stale" ]]; then
  printf 'Stale deadcode exceptions (remove or justify):\n%s\n' "$stale" >&2
fi
if [[ -n "$unexpected" || -n "$stale" ]]; then
  exit 1
fi
printf '==> Deadcode findings match the reviewed exception set\n'
