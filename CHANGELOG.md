# Autoto Changelog

All notable changes to Autoto are tracked here. The project is still an experimental local-first MVP, so entries focus on user-visible behavior, security boundaries, and contributor-facing workflow changes.

## Unreleased

### Branding and compatibility

- Renamed the current product to **Autoto**. The canonical Go module and CLI are `autoto` and `cmd/autoto` / `autoto`; `cmd/autoto` remains a legacy compatibility shim.
- Moved default runtime state to `~/.autoto/config.json` and `~/.autoto/autoto.db`. When the canonical config is absent, a legacy `~/.autoto/config.json` is copied forward for compatibility.
- Made `AUTOTO_*` environment variables and `X-Autoto-*` headers canonical. Corresponding `AUTOTO_*`, `X-Autoto-*`, and legacy remote-access cookie names remain accepted only for migration compatibility.
- Legacy compatibility actually used at runtime now warns once per process/compatibility key: the `autoto` CLI shim, effective `AUTOTO_*` or legacy-config fallback, successful legacy token/access header or cookie use, and the successful CLIProxyAPI legacy management-credential fallback report their canonical replacement. Canonical values suppress fallback warnings, invalid credentials do not warn, and logs never include token, password, cookie, API-key, or credential values.
- Defined the legacy compatibility lifecycle in `PROJECT_PLAN.md`: canonical names win, legacy aliases are compatibility reads/forwards only, removal is no earlier than v0.4.0, and every runtime surface requires at least two tagged releases of migration runway plus the documented deletion gates. The explicit response-write exception is `window.AUTOTO_LOCAL_TOKEN`: the server still injects it with the same value as canonical `window.AUTOTO_LOCAL_TOKEN`, and `runtime.mjs` reads it only as a fallback until first-party runtime no longer depends on it and the old-UI migration window is complete.
- Historical entries below intentionally retain pre-rename Autoto terminology, endpoint names, commit messages, and other recorded facts; they are legacy history rather than current naming guidance.

### Added

- Added a native Kiro (Amazon Q) subscription provider using the Event Stream API, with OAuth token refresh, startup token warmup, and `ksk_*` API key authentication as an alternative to the OAuth browser flow.
- Added a Bash tool danger reflection safety gate with four configurable strictness levels (off / loose / medium / strict): the gate intercepts high-risk commands, queries a separate LLM for a structured verdict, and blocks execution when the risk is not justified. `--version` and `--help` queries are whitelisted and bypass reflection.
- Added a continuation budget settings panel per workspace: independently configurable limits on continuations, total turns, total tokens, and wall-clock run duration. Defaults to no limit; negative values explicitly opt out of any ceiling. Currently running sessions retain their existing durable budgets on change.
- Added project drag-to-reorder in the sidebar, with server-persisted order.
- Added a native `gemini-interactions` provider with SSE streaming, image input, function calling, reasoning effort, internal thought-signature replay, schema sanitization, and `x-goog-api-key` redaction.
- Added local account sessions, Unicode/case-folded handles, per-user Agent drafts, handle suggestions, and immutable message corrections with retained or newly uploaded attachments.
- Added a provider/model-driven setup wizard, live assistant streaming cards, clipboard attachments, localized Unicode-safe draft limits, and Agent reasoning controls.
- Added persistent Run/ToolCall lifecycle timestamps, lightweight tool-call previews, and an active Run summary endpoint.
- Added a frontend Run Summary card that loads completed/error/interrupted run summaries, shows tool/message/token/cost metrics, and links the review flow to the existing Git changes modal.
- Added streaming Bash tool output over Agent WebSocket events, with a live output card in the chat UI while commands run.
- Added SQLite migrations V19–V22 for schedules and run-source metadata, durable notification deliveries, channel pairings/events/cursors, and device-action requests.
- Added schedule CRUD and trigger APIs plus the automation worker. Schedules accept only `readOnly` or `acceptEdits`, persist a run permission cap, use leases to avoid duplicate claims, record busy executions as skipped, and do not cancel or replace an existing manual run.
- Added persisted Webhook and Telegram delivery history with deduplication, leases, bounded attempts, exponential backoff, delivered/dead states, aggregate statistics, and an explicit retry API. Delivery failures remain outside the Agent loop.
- Added server-backed integration connections whose Telegram bot token and Home Assistant access token must be `env:VARIABLE_NAME` references. Public API responses expose only configured-field booleans, not the reference target or resolved secret.
- Added a Telegram control plane using Bot API long polling only. It persists update cursors and channel events, supports private-chat `/pair`, `/status`, `/approve <toolCallId>` with one-time `allow_once` semantics, and `/deny <toolCallId> [reason]`, rate-limits paired chats, and silently ignores unauthenticated commands and failed pairing attempts. There is no `/task`, free-form chat, Telegram webhook receiver, Slack, or Discord adapter.
- Added bot-credential revision binding for Telegram pairings: changing the bot token revokes stale pairings, and the local API/UI can explicitly revoke a pairing.
- Added a Home Assistant adapter restricted to loopback, `.local`, link-local, or private-network endpoints. State/entity listing is read-only and attribute-filtered; state changes use a fixed action allowlist, a short-lived persisted request, two local UI confirmations, and a direct-loopback approval. Critical or unknown actions—including door unlock, camera snapshot, scripts, automations, and shell commands—are hard-blocked, and IM cannot initiate or approve device actions.
- Added a local monitoring snapshot that aggregates active runs, pending approvals, schedules, delivery states, channel pairings/events, device-action states, and automation-worker status.
- Added hard blocking for sensitive workspace paths in the file path tools: `Read`, `Write`, `Edit`, `Glob`, and `Grep` reject or omit environment files, credential/secret files, private-key material, and `.git` contents.
- Added Agent stream protocol 2 with per-process stream sessions and monotonic sequences, bounded in-memory replay, explicit resync reasons, and authoritative live-snapshot recovery. Durable Agent-event persistence and cross-process/restart replay remain unimplemented.
- Added server-backed scoped and revisioned Skills with global/project/workspace CRUD, effective-skill resolution, revision history/detail, optimistic-lock restore, and snapshot-stable cursor pagination. The Settings scoped panel supports scope browsing, detail, pagination, revision history, and restore; create, SKILL.md import, enable/disable, edit, and delete UI actions remain global-only.

### Changed

- Danger reflection now uses the active conversation's model for its safety judgment instead of the summary/title-compaction model or a global safety-model override. Reflection verdict caches are also bound to the active model.
- Gateway requests carrying standard sampling parameters now succeed and disclose what was dropped, instead of failing. `providers.GenerateRequest` has no field for `temperature`, `top_p`, `seed`, or their neighbours, so no backend can receive them; rejecting the request made the endpoint unusable, because the standard OpenAI clients set `temperature` on nearly every call. The call now goes through and the response carries `X-Autoto-Ignored-Parameters` naming each parameter that could not be forwarded, on both `/v1/chat/completions` and `/v1/messages`, for buffered and streaming responses alike. A request whose parameters were all honoured carries no such header. `/v1/responses` is unaffected: it never parsed the sampling fields.
- Truncated Bash results now keep the first 40% and the last 60% of the output instead of the first 20 KB. A command's exit summary, final stack frame, and failing assertion are written last, so the head-only cut discarded exactly the part that explains the failure. The retained halves are joined by a `...[N bytes truncated]...` marker, and both join points are trimmed back to a UTF-8 rune boundary so a multi-byte character is never split; genuinely binary output still passes through untouched.
- Hardened conversation switching with cancellable, generation-bound message requests so stale A→B→A responses cannot overwrite the active session. Subagent task updates now patch stable `runId`/`toolUseId` cards instead of rebuilding the full message history, and recent-conversation state synchronizes across browser tabs through the existing local preference key.
- Model discovery now reports whether models were remotely discovered or fallback-only, and the setup wizard accepts any registered provider with usable models.
- Run summaries no longer load complete tool inputs/outputs; full details remain available from the tool-call detail APIs.
- Browser-local drafts remain an unauthenticated compatibility fallback, while logged-in users use versioned private server drafts.
- Updated the July 9/12 planning notes and project roadmap to reflect completed provider reliability, database migration, project instruction loading, run tracking, schedules, durable delivery history, limited Telegram pairing/approval controls, monitoring aggregation, and the constrained Home Assistant adapter.
- Added the Provider capability contract and exposed it through provider/model metadata; Agent execution now uses declared capabilities instead of provider-name branching.
- Moved preview, Telegram channels, automation workers, and HTTP serving under the runtime Supervisor. Services start in registration order and close in reverse order, so HTTP stops before automation/channels during shutdown.
- Clarified product Phase naming: the implemented IM scope is a narrow Telegram status/one-time-approval control plane, not a general inbound assistant. Skills closeout is limited to backend scope/effective/revision/pagination semantics and the current scoped browse/restore UI; future work is defect-only and does not imply that all scoped write operations have UI coverage.

### Security

- Required a full remote session to change instance-wide account preferences. Before any local account is registered these are one shared record, not one per person, and a restricted remote session could rewrite it — including `gitName`/`gitEmail`, the identity commits are attributed to, and an avatar data URL rendered by every local browser. Reading is still allowed in either mode, since the remote UI cannot render without it, and per-user preferences are unaffected because that path is governed by the user's own login.
- Restored the `/v1/responses` rejection of an explicit `store`. It sits alongside `previous_response_id`, `background`, and `conversation` as a stateful-hosted field the gateway cannot honour, but it had dropped out of the rejection list while its request field stayed parsed and unread, so a client asking for server-side storage was dispatched as if the request had been accepted on those terms.
- Scoped tool session grants to attended Runs. A grant is a human's answer to one interactive approval prompt and it outranks the serious-but-recoverable review gate, so a schedule dispatch or internal submission reusing one meant an unattended Run could execute a command nobody was present to approve. Schedule and internal Runs — and any Run source that is not `manual` or `conversation` — now neither spend nor create session grants, and unknown sources fail closed as unattended.
- Rejected schedule prompts containing a command that stops or restarts Autoto itself, at both create and update. Such a schedule otherwise produces a restart loop that survives every restart: the server dies mid-run, autostart or an OS supervisor revives it, the interrupted Run resumes, and the resumed turn reissues the command — with the UI that would disable the schedule hosted by the process being killed. Matching anchors on command shapes so prompts that merely discuss restarts are unaffected.
- Bound Agent and terminal WebSockets to the authenticated local account session that opened them. Local logout or session expiry now terminates established sockets immediately, while other independent login sessions remain connected.
- Hardened tool and filesystem path resolution against symlink escapes, bounded file/DOCX/Git fingerprint work, and restricted remote root-directory browsing.
- Hardened attachment filenames and MIME detection, explicitly cleans multipart temporary files, defaults downloads to attachment, and only inlines validated safe images with `nosniff`.

## v0.1.0 - 2026-07-07

### Added

- Added SDK streaming for the official Anthropic Messages and OpenAI Responses providers, including text deltas and usage capture.
- Added automatic Anthropic 5m prompt-cache breakpoints for sufficiently large system/tool/message requests.
- Added Git workspace status, diff, log, and explicit-path commit APIs with a Git diff UI.
- Added backend workline fork APIs that create Git worktrees, child worklines, and primary agents, plus merge-check preflight and clean-worktree merge APIs that reject conflicts safely.
- Added local dogfood evidence for API-driven project creation, tool execution, Git diff review, and selected-path commit flows.
- Added model usage cost estimates backed by a small public USD-per-million-token table.
- Added `WebFetch` as a core read-only tool for public HTTP(S) documentation lookup.
- Added `WebSearch` as a core read-only tool for lightweight public web search result lookup.
- Added a stdio MCP client and `MCPListTools` / `MCPCallTool` core tools, guarded as exec-risk operations.
- Added a persisted stdio MCP server registry with CRUD APIs, `tools/list` discovery, and registered-server lookup from MCP core tools.
- Added `config.json` schema `version: 1` normalization for legacy configs.

### Security

- Added browser-originated API and WebSocket protection with a per-process local token, same-origin checks, and `Sec-Fetch-Site` handling.
- Removed the terminal WebSocket `InsecureSkipVerify` bypass.
- Restricted Git API repository resolution to the project path or configured default project directory.
- `WebFetch` rejects local, loopback, link-local, private, and unspecified hosts by default.
- MCP registry responses expose environment variable names only, while stored values remain local and are used only to launch approved stdio MCP processes.

### Changed

- Removed the unused `background_tasks` metric from the usage summary and UI until real background task execution exists, and stopped creating the unused table for new databases.
- Added edit/update/cancel support to the MCP registry UI without echoing stored environment values.
- Documented the current local security model, Git workflow boundaries, and dogfood reproduction path.
- Added CI lint/release scaffolding for `golangci-lint` and GoReleaser binary releases.
- Added an explicit `.golangci.yml` and front-end `node --test` module coverage for formatter and MCP registry parsing helpers.
- Added an end-to-end server smoke covering HTTP message submission, agent WebSocket events, tool approval, Bash execution, tool-result feedback, and persistence.
- Continued frontend ES module extraction: `app.js` is now a small bootstrap, with main UI logic, Agent Server backend registry/modal/Admin behavior, chat sending/drafts/history/attachments/slash command behavior, chat message rendering/approval/Markdown behavior, directory chooser/browser/recent-directory behavior, shared formatters, Git workflow modal behavior, terminal preferences/settings/WebSocket behavior, API/WebSocket runtime helpers, MCP registry parsing helpers, backend MCP registry UI/actions, Settings Models/Providers UI/model helpers, Settings local preference panels rendering/actions, Settings system/storage/usage/users/about panels, Settings AI Agents/Worklines workspace panels, Settings Skills workbench rendering/actions, global shortcut/sidebar/mobile shell behavior, browser-local settings preference/backup/import behavior, DOM helpers, Settings/Skills static data, and local preference defaults split under `static/modules/`.

### Known gaps

- Slack and Discord channels are not implemented. Telegram is the only inbound channel and uses long polling; there is no inbound webhook receiver.
- Telegram has no `/task` command and no free-form assistant chat. Its current command surface is pairing, minimal status, one-time tool approval, and denial only.
- Home Assistant is the only device integration. There is no generic IoT layer, camera action support, door-unlock action, or cloud monitoring service; the monitoring snapshot is local aggregate state only.
- Durable notification deliveries do not make Agent WebSocket events durable: Agent stream replay is still process-local and in-memory.
- The Settings skills workbench can create, enable/disable, delete, and discover tools for persisted MCP registry servers; full update/edit forms and long-lived MCP sessions remain future work.
- The embedded frontend has an ES module seam, but a large remaining portion of UI feature code still lives in `app-main.mjs` and should continue being split or migrated to a framework.
- README includes a lightweight tracked GIF workflow preview; a recorded product GIF can replace it later.
