# Autoto Architecture Guide

This guide is a contributor-facing map of how a request flows through the local Autoto MVP. The Go module is `autoto`; `cmd/autoto` is the canonical application entrypoint, while `cmd/autoto` is a legacy compatibility shim. For roadmap detail, see `PROJECT_PLAN.md`; for operational security boundaries, see `SECURITY.md`.

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
         Read, Write, Edit, Bash, Glob, Grep, WebSearch, WebFetch, MCPListTools, MCPCallTool
  v
internal/db SQLite store
```

## Request and event flow

### 1. Browser boot

1. `internal/server/ui.go` serves `/` and the embedded static assets.
2. The page receives a per-process local token as a JS bootstrap value and as a local cookie.
3. `internal/server/static/app.js` attaches the canonical `X-Autoto-Token` header to API calls and includes the same token on WebSocket URLs. `X-Autoto-Token` remains accepted only for legacy-client compatibility.

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

1. `internal/agent/loop.go` loads agent, project, workline, and message history from `internal/db`.
2. It compacts older context when needed and builds a `providers.GenerateRequest` containing system prompt, messages, and tool schemas.
3. The selected provider streams `providers.Event` values back to the runner.
4. Assistant text and tool requests are persisted as messages/tool calls, then published through the event hub.

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
- Kiro (Amazon Q) Event Stream API with OAuth token refresh, startup token warmup, and `ksk_*` API key authentication as an alternative to the OAuth browser flow.

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

Tool path handling should stay bounded to the agent working directory or explicitly configured project boundary. Network tools must keep local/private host protections by default.

The stdio MCP client lives in `internal/mcp`. `MCPListTools` starts a configured stdio server, performs `initialize` + `tools/list`, and returns discovered tool metadata. `MCPCallTool` starts a configured stdio server, performs `initialize` + `tools/call`, and formats text content results. Both tools accept direct stdio config or a persisted `serverId` from the MCP registry. They remain `exec` risk because they launch local processes and should stay approval-gated until a finer-grained MCP policy layer exists.

### 7. MCP server registry

MCP registry handlers live in `internal/server/mcp_servers.go`:

- `GET /api/mcp/servers` lists persisted stdio MCP server entries with environment variable names only.
- `POST /api/mcp/servers`, `PATCH /api/mcp/servers/{id}`, and `DELETE /api/mcp/servers/{id}` manage local stdio server launch configuration.
- `GET /api/mcp/servers/{id}/tools` starts the registered server long enough to run `initialize` + `tools/list`, then closes it.

Registry entries are stored in SQLite `mcp_servers`. Environment variable values are local launch secrets: they are stored for process execution but are not returned by API responses. Settings → Skills → MCP can create, enable/disable, delete, and run `tools/list` discovery for registered servers. The current implementation starts a fresh stdio process per discovery or tool call; long-lived pooled MCP sessions are future work.

### 8. Git workflow

Git handlers live in `internal/server/git.go`:

- `GET /api/agents/{id}/git/status`
- `GET /api/agents/{id}/git/diff`
- `GET /api/agents/{id}/git/log`
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

`internal/peercontrol` owns the device identity, invitation envelope, pairing claim, session challenge, and bearer session used between two Autoto instances. `internal/server/remote_collaboration.go` exposes the owner-facing side under `/api/remote-collaboration` behind the sensitive local token guard, and `internal/server/remote_peer_api.go` exposes the inbound protocol under `/api/peer/v1`. `internal/server/static/modules/peer-collaboration-settings.mjs` is the settings page for invitations, pairings, per-agent grants, and revocation. Every inbound peer request re-reads the pairing and compares its `credential_revision` and `grant_revision` against the values snapshotted when the bearer was issued, so revoking a pairing or re-authorizing its grants invalidates tokens already in the wild.

`internal/server/execution_transport.go` adds the device-facing half of remote execution on the same channel: `POST /api/peer/v1/execution/{heartbeat,claim,report}`, all requiring the `execute_tools` scope. A remote execution device is not a separate credential. `execution_devices.identity_fingerprint` is recorded at registration and compared inside `internal/db/execution_transport.go`, so only the pairing whose peer identity matches may act as that device; holding a valid bearer for a different pairing is not enough. A claim is restricted to the agents that pairing was granted, and the allow list is part of the claim query rather than a check on the delivered task, because a delivered payload cannot be recalled and the ledger has no leased-to-queued transition.

Task state lives in `remote_execution_tasks` and moves only by compare-and-swap on `revision`, which the device echoes from the task it received. `no_fallback` stays set: an abandoned lease is expired rather than retried on the host. Liveness is separate from authority — a heartbeat may report `ready`, `online`, `degraded`, or `offline`, but a disabled device cannot heartbeat its way back into service, and `MarkStaleExecutionDevicesOffline` drops a device that stops reporting so a stale `ready` cannot keep authorizing new bindings. Claims and reports are audited under the `peer` category; heartbeats are not, because auditing a timer would bury the rows that carry security meaning.

The runner still refuses to start a run for an agent bound to a remote device (`Runner.EnsureLocalExecution`, `ErrRemoteExecutionUnavailable`), and the HTTP guards in `internal/server/execution_guard.go` still fail closed for such agents. The transport is the delivery channel and the ledger boundary; dispatching a run or a tool call into it is a separate step.

Local agents can drive a paired peer through three core tools — `PeerSnapshot` (read), `PeerSendTask` and `PeerResolveApproval` (both exec-risk, so they go through the normal approval policy). Approval decisions are `allow_once`, `allow_session`, or `deny`; `allow_session` is gated behind the dedicated `approve_session` scope on both the pairing and the per-agent grant (approve_once stays strictly one-shot), and hosts degrade it to `allow_once` — echoed via `appliedDecision` and the audit trail — when the pending tool call cannot carry a session grant. The tools live in `internal/tools/peer_collaboration_tools.go` and reach the server through `tools.PeerCollaborationService` (`internal/tools/peer_collaboration.go`), an interface implemented by `internal/server/peer_agent_bridge.go` and injected via `Runner.SetPeerCollaborationService` — the same cycle-free pattern as `BackgroundTaskService`. The bridge reuses the controller-side pairing validation and `peercontrol.Client` cache, applies the same message bounds as the HTTP proxies, and records the same required `peer` audits (`task.forward`, `approval.forward`) with `actor=agent` and the local agent id, failing closed when the audit cannot be persisted. Remote agent ids never enter FK-constrained columns: audits keep them in `details_json`, and `agent_messages.created_by` stores NULL for non-user actors (`api`, `peer:*`) whose attribution lives in the audit trail.

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

The current UI is served from `internal/server/static/index.html` and `internal/server/ui.go`. `internal/server/static/app.js` is now a tiny compatibility bootstrap that dynamically loads ES modules without a build step. The legacy UI logic lives in `internal/server/static/modules/app-main.mjs`; Agent Server backend registry/modal/Agent Admin behavior lives in `internal/server/static/modules/backend-registry.mjs`; chat sending/drafts/history/attachments/slash commands live in `internal/server/static/modules/chat-composer.mjs`; chat message rendering/approval/Markdown behavior lives in `internal/server/static/modules/chat-rendering.mjs`; directory chooser/browser/recent-directory/path formatting behavior lives in `internal/server/static/modules/directory-browser.mjs`; shared number/size/money/time formatters live in `internal/server/static/modules/formatters.mjs`; Git status/diff/log/commit modal behavior lives in `internal/server/static/modules/git-workflow.mjs`; terminal preferences/settings/WebSocket behavior lives in `internal/server/static/modules/terminal.mjs`; shared API/token/WebSocket helpers live in `internal/server/static/modules/runtime.mjs`; MCP registry form parsing helpers live in `internal/server/static/modules/mcp-registry.mjs`; backend MCP registry UI/actions live in `internal/server/static/modules/mcp-registry-ui.mjs`; Settings Models/Providers UI and model-selection helpers live in `internal/server/static/modules/model-provider-settings.mjs`; Settings local preference panels (Profile/Network Search/IM Gateway/Notifications/Appearance) rendering/actions live in `internal/server/static/modules/local-preferences-settings.mjs`; Settings system/storage/usage/users/about panels live in `internal/server/static/modules/system-settings.mjs`; Settings Skills workbench rendering/actions live in `internal/server/static/modules/skills-workbench.mjs`; global shortcut/sidebar/mobile shell/project-search behavior lives in `internal/server/static/modules/ui-shell.mjs`; browser-local settings preference normalization, backup, and import behavior lives in `internal/server/static/modules/settings-preferences.mjs`; basic DOM/query/escaping/button helpers live in `internal/server/static/modules/dom.mjs`; static Settings/Skills navigation data lives in `internal/server/static/modules/settings-data.mjs`; and localStorage keys/default preference data live in `internal/server/static/modules/preferences-data.mjs`.

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
