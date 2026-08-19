# Autoto Architecture Guide

This guide is a contributor-facing map of how a request flows through the local Autoto MVP. The Go module is `autoto`. There are two entrypoints: `cmd/autoto` is the canonical CLI/server entrypoint (it starts the local service and opens the browser UI via `app.Run`), and `cmd/autoto-desktop` is the optional desktop entrypoint (a Wails WebView shell behind the `desktop` build tag). There is no separate legacy shim binary. For operational security boundaries, see `SECURITY.md`.

## High-level shape

Autoto is a single local Go service with an embedded browser UI, SQLite persistence, provider adapters, and tool execution inside a bounded project workspace.

```text
Browser UI
  | HTTP /api/* + WebSocket /ws/*
  v
internal/server
  | validates local token, Origin/Sec-Fetch-Site, route params, and path boundaries
  v
internal/agent Runner + EventHub
  | persists messages/tool calls and streams events
  +--> internal/providers Provider.Generate
  |      OpenAI official, Anthropic official, OpenAI-compatible, Gemini, Kiro, CLIProxyAPI preset
  |
  +--> internal/tools Tool.Execute
         Read, Write, Edit, Bash, Glob, Grep, WebSearch, WebFetch, MCPListTools, MCPCallTool,
         AgentSnapshot, AgentSendMessage
  v
internal/db SQLite store
```

## Request and event flow

### 1. Browser boot

1. `internal/server/ui.go` serves `/` and the embedded static assets.
2. The page receives the local API token as a JS bootstrap value and as a local cookie. The token is not per-process: it is persisted to `secrets/local-api.token` under the Autoto home directory (or supplied via `AUTOTO_LOCAL_TOKEN`) and reused across restarts, so open tabs survive a server restart.
3. `internal/server/static/app.js` attaches the canonical `X-Autoto-Token` header to API calls. `X-Autoto-Token` is the only accepted token header; WebSocket upgrades additionally accept the `autoto_local_token` cookie. The server does not accept `?token=` query credentials.

### 2. Local request guard

1. `internal/server/server.go` builds the chi router and wraps browser-originated API routes with `localRequestGuard`.
2. `internal/server/security.go` rejects cross-site browser requests using `Origin`, rejects `Sec-Fetch-Site: cross-site` even when `Origin` is absent, and requires the local token for browser-originated API requests.
3. `internal/server/ws.go` and `internal/server/terminal.go` apply the WebSocket-specific same-origin and token checks before accepting upgrades.

The guard is intended to prevent a random web page from driving the local agent through `http://localhost:16888` while the user is browsing. It is not a replacement for real multi-user authentication: any local process or user that can read the served UI can also read the bootstrap token. Before exposing Autoto beyond a trusted local loopback workflow, add login sessions, scoped authorization, audit trails, and stronger secret storage.

### 3. Chat message submission

1. `POST /api/agents/{id}/messages` is handled in `internal/server/agent.go`.
2. The handler validates the agent and request payload, stores the user message, then starts or resumes the agent runner.
3. The UI listens on `/ws/agent` for `message.created`, `tool.call.*`, `run.*`, and error events.

### 4. Agent loop

The loop logic that used to live in a single `internal/agent/loop.go` is now split across `internal/agent/continuation.go`, `continuation_background.go`, `continuation_limits.go`, `runner_context.go`, `runner_model.go`, and `runner_tools.go` (plus focused helpers such as `context_management.go` and `tool_output_pipeline.go`).

1. `continuation.go` (`Runner.run` / `runContinuous`) drives the run in segments and loads the agent and message history from `internal/db`. Background-task park/wake and startup recovery live in `continuation_background.go`; per-run budgets live in `continuation_limits.go`.
2. `runner_context.go` compacts older context when needed and assembles the provider message set; `runner_model.go` builds the `providers.GenerateRequest` containing system prompt, messages, and tool schemas.
3. The selected provider streams `providers.Event` values back to the runner (`runner_model.go`).
4. Assistant text and tool requests are persisted as messages/tool calls — tool execution and approval routing live in `runner_tools.go` — then published through the event hub. After a tool batch settles and before the next `Generate`, the runner may claim one queued follow-up onto the same Run (`runner_steer.go`). The same inject runs at the start of a resumed segment after a background-task wake, so a follow-up queued while the parent was parked is visible on that next `Generate`. Pending approval, danger confirmation, a queued plan/execute mode that does not match the live Run, and permission/policy generation changes fail closed and leave the queue parked. A persist failure restores the queue and continues the Run instead of killing it. Interrupt still cancels the tree; leftover queue drains into a new Run only after the agent is idle.

Plan-mode runs freeze `execution_mode=plan` on the Run. The assistant's final text must be the six-field plan JSON; `persistAndReviewPlan` stores it, runs the isolated reviewer, and either publishes `plan.approval_required` or defers that event for one automatic plan-mode replan when the verdict is `needs_human` and the conversation's `plan_reflection` flag is on (default). The retry is started from `executeRegisteredRun` after `unregisterRun` so it cannot cancel the finishing draft run. Live snapshots include `plans[]` with `sourceRunId` (executed and cancelled included) so the UI can attach a card to the originating assistant message instead of dumping JSON. Human approval and execute remain separate HTTP actions; reviewer pass never creates `CreateRunForPlan`. A blank `reviewModel` inherits the active conversation model; resolve and not-configured failures also fall back to that conversation model, while timeout and malformed reviewer output stay unavailable.

### 5. Provider adapters

Provider implementations live in `internal/providers` and all satisfy the same interface:

```go
type Provider interface {
    Name() string
    ListModels(context.Context) ([]string, error)
    Generate(context.Context, GenerateRequest) (<-chan Event, error)
}
```

Current adapters include:

- Anthropic official Messages API with SDK streaming and automatic 5m prompt-cache breakpoints for sufficiently large requests.
- OpenAI official Responses API with SDK streaming.
- OpenAI-compatible Chat Completions APIs, including the CLIProxyAPI preset.
- Gemini Interactions API in stateless mode, with provider-native steps and thought signatures persisted in the internal-only `provider_state_json` message field.
- Native Gemini Cloud Code / Antigravity OAuth (`internal/providers/gemini_provider.go`) against `cloudcode-pa.googleapis.com`. Settings quota comes from production `POST /v1internal:fetchAvailableModels`. Generate follows Antigravity-Manager: Electron `User-Agent` plus `x-client-name`/`x-client-version`, `requestId` `agent/{ms}/{hex}`, `enabledCreditTypes: ["GOOGLE_ONE_AI"]` on agent requests, and host fallback sandbox → daily → prod because production generate can 429 while the quota card still shows remaining. Live `fetchAvailableModels` is the model catalog; static ids such as `gemini-3.7-flash` are not advertised unless Google returned them.
- Kiro (Amazon Q) Event Stream API with OAuth token refresh, startup token warmup, and `ksk_*` API key authentication as an alternative to the OAuth browser flow.
- Codex ChatGPT OAuth (`internal/providers/codex.go`) against `chatgpt.com/backend-api/codex`. Account quota is reconstructed from ChatGPT's subscription windows rather than the Platform Billing API: live `x-codex-*` rate-limit headers on `/responses`, `GET /backend-api/wham/usage` on explicit sync, and optional `/wham/rate-limit-reset-credits` for official reset tokens. Local 5h/7d request stats are aligned to the current reset window when `reset_at` is known; dollar amounts stay local estimates, not an OpenAI invoice.

Each provider may set its own outbound User-Agent and an optional `clientIdentity` (`""` / Autoto default, `claude-code`, or `codex`). The identity is a short first-party CLI sentence prepended to that provider's system prompt or instructions only; it does not replace Autoto's runtime prompt, permissions, or safety boundary, and it is not a global switch. Anthropic subscription OAuth still injects Claude Code identity as the first system block because the official API requires it. Gateway calls keep the caller's prompt and do not apply this overlay.

Provider code is responsible for translating Autoto's normalized message/tool representation into each upstream API shape and translating upstream deltas back into normalized events.

### 6. Tool execution and approval

Tools live in `internal/tools` and implement:

```go
type Tool interface {
    Name() string
    Description() string
    Schema() any
    Risk(json.RawMessage) Risk
    Execute(context.Context, Call, Env) (Result, error)
}
```

The runner checks each tool risk against the agent permission mode:

- Safe read-only tools can run in less restrictive modes.
- Riskier tools such as `Write`, `Edit`, `Bash`, and stdio MCP tools may pause for approval.
- Approval decisions are posted to `POST /api/agents/{id}/tool-calls/{toolUseId}/approval` and are sent back to the model as tool results.

`Read`, `Write`, `Edit`, and `MultiEdit` report a whole-file SHA-256 prefix (`hash=`) in both tool output and `Meta.contentHash`. `Edit`/`MultiEdit` accept optional `expected_hash` and refuse to write when the live file no longer matches. Truncated `Read` windows use 1-based line prefixes and, when no `offset` was given, a cheap bounded outline of `func`/`class`/`def`/`type`/Markdown headings. Binary and image reads stay unchanged.

Project instruction files (`AGENTS.md`, `CLAUDE.md`, `.cursorrules`, `.cursor/rules/*.mdc`, `.github/copilot-instructions.md`, `GEMINI.md`) are loaded as untrusted `project` user context and must not enter the system prompt. `.cursor/mcp.json`, hooks, and `.env` are not read.

Child-agent public results may include bounded `summary` / `files` / `result` parsed from the last assistant message. Whole-message JSON is accepted as-is; otherwise only a trailing fenced or trailing JSON object with both `summary` and `result` counts, so an example object in the prose is not treated as the report. Parse failure falls back to truncated prose. Acceptance criteria stay off that public object.

Tool path handling should stay bounded to the agent working directory or explicitly configured project boundary. Network tools must keep local/private host protections by default.

The stdio MCP client lives in `internal/mcp`. `MCPListTools` and `MCPCallTool` accept a persisted `serverId` from the MCP registry (freeform command/cwd/env from the model are rejected). `MCPCallTool.arguments` is a JSON object matching the MCP tool `inputSchema`; a JSON-encoded object string is unwrapped before `tools/call` so nested fields such as `navigate_page.url` are not advertised or sent as a string. Execute runs inject an immutable `host_runtime` system layer listing enabled registry names and `serverId` values only; command, args, cwd, and env stay omitted. Consecutive calls from the same agent and working directory reuse one warm stdio process so browser pages and other server-side state persist. The process is pinned to the launch fingerprint (command, args, cwd, env hash); a mismatch, call error, disable/delete, idle timeout (5 minutes), or 62 served calls recycles it. Concurrent calls to the same slot do not queue: the overflow call runs on its own short-lived process. Settings → Skills → MCP `tools/list` discovery still starts a fresh process and closes it, so UI discovery does not share the agent's session. MCP tools remain `exec` risk (except the documented managed-automation read subset) and stay approval-gated.

### 7. MCP server registry

MCP registry handlers live in `internal/server/mcp_servers.go`:

- `GET /api/mcp/servers` lists persisted stdio MCP server entries with environment variable names only.
- `POST /api/mcp/servers`, `PATCH /api/mcp/servers/{id}`, and `DELETE /api/mcp/servers/{id}` manage local stdio server launch configuration.
- `GET /api/mcp/servers/{id}/tools` starts the registered server long enough to run `initialize` + `tools/list`, then closes it.

Registry entries are stored in SQLite `mcp_servers`. Environment variable values are local launch secrets: they are stored for process execution but are not returned by API responses. Settings → Skills → MCP can create, enable/disable, delete, and run `tools/list` discovery for registered servers. Agent `MCPListTools` / `MCPCallTool` calls reuse a warm stdio process per server, agent, and working directory. Registry disable, launch-config edits, and delete invalidate that pool. UI discovery remains one-shot.

### 8. Git workflow

Git handlers live in `internal/server/git.go`:

- `GET /api/agents/{id}/git/status`
- `GET /api/agents/{id}/git/diff`
- `GET /api/agents/{id}/git/log` returns newest-first commits with parent hashes and ref decorations (`HEAD`, branch, remote, tag) so the Git panel can draw a commit timeline. The API remains read-only: it does not checkout, reset, or push.
- `POST /api/agents/{id}/git/commit`

Important invariants:

- Repository roots must resolve under the project Git path or the configured default project directory.
- Commits require an explicit `paths` list.
- The API must not silently push, amend, reset, clean, force, or stage the whole worktree.
- Unborn repositories without `HEAD` should degrade gracefully for diff/status flows.

Workline workflow handlers live in `internal/server/workline_workflow.go`:

- `POST /api/worklines/{id}/fork` creates a sibling Git worktree, a child workline, and a primary agent whose `cwd` points at that worktree.
- `GET /api/worklines/{id}/merge-check` creates a temporary detached worktree for the target head and runs a non-committing merge preflight to report conflicts without touching the real target worktree.
- `POST /api/worklines/{id}/merge` requires clean source and target worktrees, runs a no-ff merge in the target worktree, aborts and returns conflicts on merge failure, and persists merge metadata on success.

These handlers reuse the Git boundary model: repositories must stay within the project path, configured default project directory, or an Autoto-created workline worktree under `.autoto-worktrees`. Future AI conflict-resolution code should keep the same invariant.

## Temporary remote tunnel

`internal/server/temporary_tunnel.go` owns the Quick Tunnel state machine (`unavailable`, `installing`, `idle`, `starting`, `running`, `stopping`, `error`). Binary discovery always prefers an existing `cloudflared` on `PATH`, then checks Autoto's managed copy at `<home>/bin/cloudflared[.exe]`.

A host-local `POST /api/security/remote-access/tunnel/install` request delegates to `internal/server/temporary_tunnel_install.go`. The installer selects a fixed OS/architecture asset from Cloudflare's official GitHub release, uses the public-direct network policy with an explicit GitHub host allowlist, bounds metadata and asset sizes, verifies the GitHub-provided SHA-256 digest, restricts macOS archive extraction to one regular `cloudflared` entry, and commits through a temporary file in the managed directory. It does not use an operating-system package manager, request administrator privileges, or modify `PATH`. Successful installation returns the manager to `idle`; starting the tunnel remains a separate user action.

`internal/server/static/modules/remote-access-settings.mjs` renders one install action only while the binary is missing and the current platform is supported. Once installation succeeds, the install action disappears and the existing start action becomes available without restarting Autoto.

## Peer collaboration and the remote execution transport

`internal/peercontrol` owns the device identity, invitation envelope, pairing claim, session challenge, and bearer session used between two Autoto instances. `internal/server/remote_collaboration.go` exposes the owner-facing side under `/api/remote-collaboration` behind the sensitive local token guard, and `internal/server/remote_peer_api.go` exposes the inbound protocol under `/api/peer/v1`. `internal/server/static/modules/peer-collaboration-settings.mjs` is the settings page for invitations, pairings, per-agent grants, and revocation. Revoked or expired pairing rows can be deleted from that list after they have already been invalidated; active pairings cannot. On the controller, `GET /api/remote-collaboration/peers/{id}/snapshot` also feeds the conversation sidebar (`peer-collaboration-workspace.mjs`): shared agents are listed as a separate remote section and opened in a dedicated transcript that posts tasks and approvals through the existing controller proxies. Remote agent ids never enter local `/api/agents` routes or FK-constrained columns. Every inbound peer request re-reads the pairing and compares its `credential_revision` and `grant_revision` against the values snapshotted when the bearer was issued, so revoking a pairing or re-authorizing its grants invalidates tokens already in the wild.

`internal/server/execution_transport.go` adds the device-facing half of remote execution on the same channel: `POST /api/peer/v1/execution/{heartbeat,claim,report}`, all requiring the `execute_tools` scope. A remote execution device is not a separate credential. `execution_devices.identity_fingerprint` is recorded at registration and compared inside `internal/db/execution_transport.go`, so only the pairing whose peer identity matches may act as that device; holding a valid bearer for a different pairing is not enough. A claim is restricted to the agents that pairing was granted, and the allow list is part of the claim query rather than a check on the delivered task, because a delivered payload cannot be recalled and the ledger has no leased-to-queued transition.

Task state lives in `remote_execution_tasks` and moves only by compare-and-swap on `revision`, which the device echoes from the task it received. `no_fallback` stays set: an abandoned lease is expired rather than retried on the host. Liveness is separate from authority — a heartbeat may report `ready`, `online`, `degraded`, or `offline`, but a disabled device cannot heartbeat its way back into service, and `MarkStaleExecutionDevicesOffline` drops a device that stops reporting so a stale `ready` cannot keep authorizing new bindings. Claims and reports are audited under the `peer` category; heartbeats are not, because auditing a timer would bury the rows that carry security meaning.

The runner still refuses to start a run for an agent bound to a remote device (`Runner.EnsureLocalExecution`, `ErrRemoteExecutionUnavailable`), and the HTTP guards in `internal/server/execution_guard.go` still fail closed for such agents. The transport is the delivery channel and the ledger boundary; dispatching a run or a tool call into it is a separate step.

Local agents can drive a paired peer through three core tools — `PeerSnapshot` (read), `PeerSendTask` and `PeerResolveApproval` (both exec-risk, so they go through the normal approval policy). Approval decisions are `allow_once`, `allow_session`, or `deny`; `allow_session` is gated behind the dedicated `approve_session` scope on both the pairing and the per-agent grant (approve_once stays strictly one-shot), and hosts degrade it to `allow_once` — echoed via `appliedDecision` and the audit trail — when the pending tool call cannot carry a session grant. The tools live in `internal/tools/peer_collaboration_tools.go` and reach the server through `tools.PeerCollaborationService` (`internal/tools/peer_collaboration.go`), an interface implemented by `internal/server/peer_agent_bridge.go` and injected via `Runner.SetPeerCollaborationService` — the same cycle-free pattern as `BackgroundTaskService`. The bridge reuses the controller-side pairing validation and `peercontrol.Client` cache, applies the same message bounds as the HTTP proxies, and records the same required `peer` audits (`task.forward`, `approval.forward`) with `actor=agent` and the local agent id, failing closed when the audit cannot be persisted. Remote agent ids never enter FK-constrained columns: audits keep them in `details_json`, and `agent_messages.created_by` stores NULL for non-user actors (`api`, `peer:*`) whose attribution lives in the audit trail.

## Local cross-conversation collaboration

`AgentSnapshot` and `AgentSendMessage` (`internal/tools/agent_snapshot.go`, `internal/tools/agent_send_message.go`) are the single-instance siblings of the peer tools: they let one primary conversation list the other primary conversations, read their recent user/assistant transcripts through `Store.ListNavigationConversations` / `Store.ListMessagesPage`, and send one of them a message. Both tools reject subagents and the calling conversation itself; the snapshot is read risk while sending is exec risk and goes through the normal approval policy.

A send is not a new subsystem. `AgentSendMessage` submits the existing `agent` background-task kind with a `targetAgentId` payload field, and `internal/background/message_task.go` branches the agent executor: instead of creating a child agent it validates the target (primary, not archived, not the owner, no active reverse message task), clamps the run's permission cap to the narrower of the target's own mode and the sender's frozen cap, submits the prompt through `Runner.SubmitInternal` (bounded busy-retry, then `target_agent_busy`), and attaches the target as the task's child. Everything downstream is therefore shared with spawned subagents unchanged: the wait loop with idle detection, cancellation (interrupting the dispatched run only while it is still the target's active run), the durable task result (bounded reply text), and the `resumeParent` wake-up whose report prefers the dispatched run's answer over whatever the target conversation said later (`Runner.latestVisibleAssistantText` with the task's child run id).

A transcript belonging to someone else is untrusted input. Its text is whatever the other side accumulated — user pastes, fetched pages, tool output — so a forged instruction inside it must not gain authority merely by arriving through a read-only tool result. `untrustedSnapshotPreamble` (`internal/tools/peer_collaboration_tools.go`) fences the JSON body for both the local `AgentSnapshot` detail read and the remote `PeerSnapshot`, naming the source and directing the model to treat the content as background information only. The conversation list carries its own narrower warning because titles are model- or user-generated too. The preamble is prepended, never substituted for the body, and tests assert its presence so it cannot be dropped silently.

## Tool output shaping

`Runner.processToolResultForModel` (`internal/agent/tool_output_pipeline.go`) is the single point between a tool finishing and its result being persisted and sent to the model, and it is where every result converges — successes, tool-reported errors, policy denials, plan-mode blocks, approval denials, and wrapped Go errors alike. Two policies hang off it.

Spilling keeps an oversized result out of the request while leaving all of it reachable. Above `AgentConfig.ToolOutputSpillBytes` the full output is written by `internal/spill` under `{HomeDir}/data/tool_output_spill/{conversationId}/` and the result is replaced with a head/tail preview plus the path; the model retrieves the rest with the ordinary `Read` and `Grep` tools, so the store needs no retrieval tool of its own and stays write-only. The notice's byte cost is reserved inside the cap rather than appended to a full-budget preview, so a replacement cannot exceed the result it replaced, and invalid UTF-8 is repaired before the cut so raw bytes from `Bash` do not collapse the rune-safe truncation to nothing. `Read`, `Grep`, and the pipeline controls are exempt: spilling a retrieval answers it with an instruction to retrieve again, and `EndPipeline` already honoured a `max_chars` the model chose. Every failure keeps the inline result, because a storage problem must not turn a successful tool call into an error or hide output that was about to be delivered. Files are created exclusively under random names at `0600` inside `0700` directories since tool output routinely carries secrets, and the startup asset sweep prunes them by age (`spill.Retention`) — Autoto never deletes a conversation, so there is no owner lifecycle to hang deletion off, and the window outlives the point where compaction has dropped the referencing result from every live request. An active tool-output pipeline and spilling never both act: the pipeline has already replaced the output with a short preview far below the threshold.

Repeat detection counts consecutive identical calls per run (`internal/agent/repeat_tool_calls.go`), keyed by tool name plus arguments canonicalized through `encoding/json` so property order cannot disguise a repeat. Landing on one of `AgentConfig.RepeatToolCallThresholds` parks a reminder that the next request picks up through the existing `turnSystemControls` sidecar, which is why counting can happen while a result settles and no request is being built. It counts at the convergence point specifically so denials count: a model hammering a call that keeps being refused is the loop most worth breaking, and a denial never reaches an executor. The detector never vetoes — a call that already ran cannot be recalled, and the goal is to shape the next request — reports one streak length once, and clears an Agent's streaks on a new user message, because repetition either side of a human interjection is not a loop.

## Persistence model

`internal/db` owns schema creation and store methods. Main entities are:

- `projects`: local workspaces.
- `worklines`: worklines, including root worklines plus fork/worktree/merge metadata.
- `agents`: agent persona/runtime configuration for a workline.
- `agent_messages`: user, assistant, and tool-result transcript entries; provider-native continuity state is stored separately and excluded from public JSON.
- `agent_tool_calls`: pending/completed/denied tool execution records with persisted start/completion/update timestamps.
- `users` / `auth_sessions`: local account handles and revocable opaque browser sessions.
- `message_drafts`: versioned per-user, per-Agent private drafts.
- `api_requests`: provider usage, latency, and estimated cost source data.
- `agent_backends`: Agent Server integration registry entries.
- `mcp_servers`: stdio MCP server registry entries used by APIs and MCP core tools.

Schema changes should include migrations or backward-compatible normalization, plus tests that cover existing config/database state where practical.

## Frontend layout

The current UI is served from `internal/server/static/index.html` and `internal/server/ui.go`. `GET /ui/styles.css` is concatenated at serve time from the cascade-ordered `@import` list in `internal/server/static/styles.css`, so a client pays one stylesheet round trip. UI assets use a content ETag with `private, max-age=0, stale-while-revalidate` (Cloudflare is told `no-store`). `internal/server/static/app.js` is a tiny compatibility bootstrap that dynamically loads ES modules without a build step. The legacy UI logic lives in `internal/server/static/modules/app-main.mjs`; Agent Server backend registry/modal/Agent Admin behavior lives in `internal/server/static/modules/backend-registry.mjs`; chat sending/drafts/history/attachments/slash commands live in `internal/server/static/modules/chat-composer.mjs`; chat message rendering/approval/Markdown behavior lives in `internal/server/static/modules/chat-rendering.mjs`; directory chooser/browser/recent-directory/path formatting behavior lives in `internal/server/static/modules/directory-browser.mjs`; shared number/size/money/time formatters live in `internal/server/static/modules/formatters.mjs`; Git status/diff/log/commit modal behavior lives in `internal/server/static/modules/git-workflow.mjs`; terminal preferences/settings/WebSocket behavior lives in `internal/server/static/modules/terminal.mjs`; shared API/token/WebSocket helpers live in `internal/server/static/modules/runtime.mjs`; MCP registry form parsing helpers live in `internal/server/static/modules/mcp-registry.mjs`; backend MCP registry UI/actions live in `internal/server/static/modules/mcp-registry-ui.mjs`; Settings Models/Providers UI and model-selection helpers live in `internal/server/static/modules/model-provider-settings.mjs`; Settings local preference panels (Profile/Network Search/IM Gateway/Notifications/Appearance) rendering/actions live in `internal/server/static/modules/local-preferences-settings.mjs`; Settings system/storage/usage/users/about panels live in `internal/server/static/modules/system-settings.mjs`; Settings usage-history analytics live in `internal/server/static/modules/usage-history.mjs` (`GET /api/usage/history` returns the filtered trend plus a bounded stacked/distribution of the top providers); Settings Skills workbench rendering/actions live in `internal/server/static/modules/skills-workbench.mjs`; global shortcut/sidebar/mobile shell/project-search behavior lives in `internal/server/static/modules/ui-shell.mjs`; browser-local settings preference normalization, backup, and import behavior lives in `internal/server/static/modules/settings-preferences.mjs`; basic DOM/query/escaping/button helpers live in `internal/server/static/modules/dom.mjs`; static Settings/Skills navigation data lives in `internal/server/static/modules/settings-data.mjs`; and localStorage keys/default preference data live in `internal/server/static/modules/preferences-data.mjs`.

When adding frontend features, keep extracting stable seams out of `app-main.mjs` before adding more monolithic state:

- settings panels
- chat/rendering
- Git panel
- terminal panel
- API/WebSocket client helpers

The roadmap target remains either incremental ES modules without a build step or a full React/Vite migration.

## Consistency and concurrency rules

Cross-cutting implementations should follow the contributor invariants in `CONTRIBUTING.md`:

- Derive hashes, scanner verdicts, acknowledgements, and other security conclusions on the trusted server side.
- Encode state transitions as compare-and-swap writes with expected states or versions and checked `RowsAffected` values.
- Use only the transaction handle while a transaction is active, and publish success only after commit.
- Give asynchronous UI loads a monotonically increasing request sequence and model multi-stage loading with explicit states such as `idle/loading/ready/stale/error`.
- Keep provider-specific behavior behind provider adapters or minimal, evidence-driven capabilities rather than provider-name checks in business logic.

Caches must declare their source, capacity, expiry, version invalidation, permission/content invalidation, failure behavior, and secret-handling boundary before they are introduced. Security metadata that cannot be trusted must be recomputed or disabled fail-closed.

The Agent WebSocket speaks protocol 2 (`internal/agent/hub.go`). Events carry a stream session identifier and a monotonic sequence, and a reconnecting client may replay from an `after` cursor within the same process and stream session. Replay is bounded by named limits (ring size and bytes, per-event bytes, replay limit, subscriber buffer, stream count, and idle timeout) rather than by an unbounded buffer.

When replay cannot be served correctly the hub does not backfill partially. It reports one of five enumerated resync reasons (`session_mismatch`, `cursor_expired`, `replay_limit`, `subscriber_overrun`, `stream_evicted`) and the client re-reads an authoritative live snapshot (`internal/server/stream_recovery.go`, `internal/db/live_snapshot.go`) before resuming from that snapshot's watermark.

The sequence is per process and in memory, not a durable event log. There is no Agent event table, so events cannot be replayed after a restart or across processes, and stream sessions and sequences are not shared between instances. Durable persistence with a retention policy stays deferred until an external task/approval channel such as the IM Gateway makes cross-process replay a correctness requirement; the in-memory ring must not be described as a durable queue.

## Validation checklist

Before submitting changes, run the unified local check:

```bash
make check
```

If `make` is unavailable, run `./scripts/check.sh` directly. The script verifies Go formatting without rewriting files, runs Go tests/vet/build, checks embedded JavaScript syntax, and runs embedded JavaScript tests. Use `make fmt` to apply Go formatting.

CI runs the same check script and additionally runs `golangci-lint`. The server package includes an end-to-end smoke for HTTP message submission, agent WebSocket events, approval routing, Bash execution, provider feedback, and persistence. Release tags matching `v*` trigger GoReleaser to build macOS, Linux, and Windows archives.
