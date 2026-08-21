# Features

Exhaustive capability list moved out of the root README. Architecture is in [ARCHITECTURE.md](ARCHITECTURE.md).

The per-item list below is deliberately exhaustive and reads as a specification rather than a tour; the sections above cover what most people want to know.

### Agent and model

- Local HTTP server with embedded HTML/CSS/JS UI, no-build ES module seam for frontend bootstrap/runtime helpers, and extracted Settings preference panels
- SQLite persistence for projects, worklines, agents, messages, tool calls, backend registry entries, and stdio MCP server registry entries
- Provider abstraction with a minimal `Tools` / `Streaming` / `ImageInput` capability contract for:
  - OpenAI official Responses API with SDK streaming text deltas and usage capture
  - Anthropic official Messages API with SDK streaming text deltas, tool-use deltas, usage capture, and automatic 5m prompt-cache breakpoints for sufficiently large requests
  - OpenAI-compatible Chat Completions APIs
  - Gemini Interactions API with SSE streaming, images, native function calls, reasoning effort, and internal thought-signature replay
  - Kiro (Amazon Q) native subscription provider with Event Stream API, OAuth token refresh, and `ksk_*` API key authentication
  - CLIProxyAPI local OpenAI-compatible preset
- Core tools:
  - Read
  - Write
  - Edit
  - MultiEdit
  - Bash
  - Glob
  - Grep
  - WebFetch
  - WebSearch
  - MCPListTools
  - MCPCallTool
  - AgentSnapshot
  - AgentSendMessage
- Local cross-conversation collaboration: `AgentSnapshot` lists the other primary conversations on the same instance and reads their recent transcripts; `AgentSendMessage` (exec risk, approval-gated) sends one conversation a message that runs as its own turn under the narrower of its own permission mode and the sender's cap, then reports the reply back into the sending run through the same resume mechanism subagents use. Direct A-to-B-to-A message cycles are rejected, and subagents can neither send nor be targeted. A transcript read out of another conversation — or off a paired instance through `PeerSnapshot` — is fenced as untrusted, read-only background information, so a forged instruction sitting in someone else's transcript gains no authority from arriving through a tool result
- Tool-output spilling: a result larger than `agent.toolOutputSpillBytes` (default 50000 bytes) is written to disk under the Autoto home and replaced with a head/tail preview plus the file path, which the model pages through with `Read` or searches with `Grep` instead of carrying the whole thing in context. `Read` and `Grep` are exempt so a retrieval is never answered with an instruction to retrieve again, any write failure keeps the result inline rather than turning a successful call into an error, and spilled files are pruned after seven days
- Repeated-tool-call detection: consecutive identical calls are counted per run, and hitting one of `agent.repeatToolCallThresholds` (default `3, 5, 8`) adds an escalating reminder to the next request. Arguments are compared after canonicalization, denied calls count as attempts, and the detector never vetoes a call
- File-edit anchors: `Read` / `Write` / `Edit` / `MultiEdit` report a whole-file SHA-256 prefix (`hash=`). A truncated `Read` without paging includes line numbers and a cheap outline of declarations and headings. `Edit` / `MultiEdit` accept optional `expected_hash` and refuse to write when the live file no longer matches
- Project instructions load `AGENTS.md`, `CLAUDE.md`, `.cursorrules`, `.cursor/rules/*.mdc`, `.github/copilot-instructions.md`, and `GEMINI.md` as untrusted project context. They never enter the system prompt. `.cursor/mcp.json`, hooks, and `.env` are not read
- Child-agent public results include a bounded `summary` / `files` / `result` parsed from the last assistant message; parse failure falls back to truncated prose. That JSON does not grant permissions or path access
- Same-run steering: a queued follow-up is claimed onto the current Run after a settled tool batch, and again when a parked parent wakes from a child. Pending approval, a queued plan/execute mode that does not match the live Run, and permission/policy generation changes leave it parked. Interrupt still cancels the tree; leftover queue drains into a new Run only after the agent is idle
- Sensitive-path hard blocking for the file path tools: `Read`, `Write`, and `Edit` reject protected files, while `Glob` and `Grep` omit them. The blocked set includes `.env*`, credential/secret files, common private-key material, and `.git` contents
- Plan mode: a plan-mode turn can only inspect read-only tools and must emit a structured plan. The UI shows a plan card instead of the raw JSON, keeps executed/cancelled cards after reopen, and compactly labels the synthetic execute/replan prompts. Isolated review uses a dedicated model when one is set; if that model cannot be resolved or is not configured, it uses the active conversation model. Isolated review may automatically replan once when the draft admits it missed the goal, unless plan reflection is turned off for that conversation (default on; on/off under plan mode in the permission menu); a human still has to approve, then separately execute. Reviewer pass is not approval.
- Danger reflection for Bash tool calls: a configurable LLM safety gate (off / loose / medium / strict) that uses the active conversation model to review high-risk commands before execution and blocks or allows them based on a structured verdict
- Continuation budget settings panel: per-workspace limits on continuations, total turns, total tokens, and wall-clock run duration; defaults to no limit, and negative values explicitly opt out of any ceiling
- Project drag-to-reorder in the sidebar, with server-persisted order

### Security boundaries

- Sensitive-path hard blocking: `Read`, `Write`, `Edit` reject protected files; `Glob` and `Grep` skip them. Scope covers `.env*`, credential/secret files, common private-key material, and `.git` contents
- Plan review is isolated and tool-free. A `needs_human` verdict can start one plan-mode retry when plan reflection is on for that conversation (the default); turning it off waits for a human instead. `pass` still cannot approve or execute. If the dedicated review model cannot be resolved or is not configured, review uses the active conversation model. Timeouts and malformed reviewer output stay unavailable and fail closed to the human.
- Bash danger reflection: tunable LLM safety gate (off / loose / medium / strict) that uses the active conversation model to review high-risk commands before execution
- Recursive deletes, raw disk writes, permission weakening, and piping a download straight into a shell are classified as irreversible and execute-never, regardless of permission mode, allow rule, or approval
- Git workspace APIs are limited to status, diff, log, and explicit-path commits. Autoto does not automatically push, amend, reset, clean, force, or `git add -A`. The Git panel shows a commit timeline (lane graph plus ref pills) for the current branch; that view is read-only.

### Schedules and automation

- Schedule worker with cron / `@every` expressions and IANA time zones. Schedule permission is limited to `readOnly` or `acceptEdits`, persists as a run permission cap, and skips a busy Agent without interrupting or replacing its manual run. Unattended runs do not reuse session approvals granted interactively, and a schedule prompt containing a command that stops or restarts Autoto itself is rejected at create and update
- Durable Webhook / Telegram notification delivery history with deduplication, leases, exponential backoff, bounded attempts, delivered / dead states, aggregate metrics, and explicit retry
- Telegram Bot API long polling with private-chat `/pair`, `/status`, `/approve <toolCallId>` (always one-time `allow_once`), and `/deny <toolCallId> [reason]`; unauthenticated commands and failed pairing attempts are silent, and processed updates are protected by persisted event IDs and cursors
- Home Assistant integration restricted to local/private endpoints: read-only state/entity summaries, a fixed action allowlist, short-lived action requests, two local UI confirmations, and direct-loopback approval. Unknown/critical actions such as door unlock and camera snapshot are hard-blocked, and IM cannot control devices
- SQLite migrations V19–V22 and server APIs for schedules, durable notification deliveries, integration connections, channel pairings/events/cursors, and device-action requests
- Local monitoring aggregation for active runs, pending approvals, schedules, delivery states, channels, device actions, and automation-worker health

### Backends and workspaces

- Agent Server backend registry with sidebar and Agent Admin management UI for compatible OpenHands Agent Server endpoints
- Workline and container settings backed by project Workline/Agent APIs, with backend workline-fork support that creates Git worktrees, a merge-check preflight, and clean-worktree merge APIs
- Interactive PTY terminal WebSocket: `/ws/terminal`, with terminal-management controls and browser-local retention/focus preferences
- Filesystem browse / preview / mkdir APIs
- Server-backed Skills with global/project/workspace CRUD, effective-skill resolution, revision history/restore, and snapshot-stable cursor pagination; the Settings scoped panel can browse by scope, inspect details, paginate, and view/restore revisions. Create, SKILL.md import, enable/disable, edit, and delete UI actions still operate only on global scope. MCP registry actions remain available with explicit exec-risk approval
- Server-backed global/project/agent lifecycle hooks for run/tool boundaries, with snapshot-stable dispatch, CAS updates, execution history, and isolated test executions that do not create ordinary Agent runs. Shell and HTTP actions reuse the existing tool approval/audit gateway; `env:` secrets resolve only after approval, Shell stays workspace-bound with a sanitized environment, and HTTP uses the existing SSRF-resistant direct network policy. LLM gates remain isolated, tool-free provider requests
- Local plugin registry that installs stdio MCP plugins from local directories as dynamically discovered agent tools (`plugin__<slug>__<tool>`): always installed disabled, enabling requires an explicit local-code-execution confirmation, processes run with a clean environment and `env:` secret references, the manifest supports a per-plugin timeout, and update/health-check endpoints cover manifest changes; see [PLUGINS.md](PLUGINS.md)

### Interface and experience

- UI built for small screens, not merely shrunk to fit: pull-to-refresh, swipe-to-dismiss notifications, a single top bar with the conversation title and workspace tools rather than the product name, and a composer that stays reachable. Installs to a home screen as a standalone PWA, opens without browser chrome, and behaves like an app
- Settings modal search/filter with keyboard focus shortcut for quickly locating growing product configuration panels
- Chat message copy actions for exporting individual messages and the current conversation as Markdown
- Versioned private chat drafts per logged-in user and Agent, with browser-local drafts retained only as an unauthenticated compatibility fallback
- Unicode/case-insensitive local account handles, `@handle` suggestions, and immutable user-message corrections with retained/new attachments
- Clipboard image/file attachments, Unicode-safe localized draft limits, and browser-native text undo/redo
- Browser-local prompt history for the chat composer, with empty-input ↑/↓ recall and migration through local preference backups
- Chat-composer slash command palette backed by enabled local Skills command templates
- Browser-local Settings → Profile preferences for display identity, avatar initials, workspace label, and Git identity helpers
- Browser-local Settings → Network Search preferences for provider presets, result limits, confirmation, and domain rules; `WebSearch` and `WebFetch` core tools provide public web/documentation lookup
- Browser-local Settings → Notifications preferences for toast categories, run-event sounds (done / approval / error, with preset or a local custom clip, volume, and a playback cap), OS notifications with an explicit permission request, display duration, and UI terminal notices; server-backed durable Webhook / Telegram delivery history and retry
- Browser-local Settings → Appearance preferences for theme, density, terminal default visibility, and Agent event-log display
- Settings → Servers/System + Runtime panels for process, Go runtime, paths, and Agent limits
- Settings → Users: administrators can create operators (password sign-in, host settings except user management, can create projects), collaborators (password sign-in, granted projects only, including the running-task panel, Git panel, and a per-conversation summary model for those agents), and conversation-only guests (access keys, watch-only membership). Guest and collaborator limits are enforced by the server.
- Settings → Storage panel for config, database, home, and project-directory footprint
- Settings → Usage panel for request analytics (date presets, chart/records tabs, stacked provider trend, CSV of the currently loaded rows) plus projects, messages, tool calls, estimated token cost, and backends
- Settings → About dependency-license panel backed by the development-time `/api/licenses` endpoint
- Settings → About browser-local preferences backup/import for migrating profile, skills, chat drafts, prompt history, search, IM, notification, appearance, terminal, recent directory, model, and relay-protocol settings
- Settings → IM gateway automation control backed by server APIs for schedules, notification history/retry, Telegram and Home Assistant connection metadata, pairing/revocation, monitoring, device state, local device-action confirmation, and audit events. A detected legacy browser-local IM draft is shown only as a disabled migration hint and never starts a channel

### Agent WebSocket protocol

- Protocol v2 on `/ws/agent`, with per-process monotonic sequence, bounded in-memory replay, and authoritative live-snapshot resync. It is **not** a durable or cross-process event log

## Usage cost estimates

Autoto records provider usage in `api_requests` and shows aggregate estimated cost in **Settings → Usage**. The history page can export the currently loaded rows as CSV; it does not include credentials. Cost is calculated from a small built-in USD-per-million-token table in `internal/pricing/pricing.go`. The table was last reviewed on 2026-07-07 against public pricing pages: [OpenAI API pricing](https://developers.openai.com/api/docs/pricing), [OpenAI GPT-4.1 pricing announcement](https://openai.com/index/gpt-4-1/), and [Anthropic Claude pricing](https://docs.anthropic.com/en/docs/about-claude/pricing). Unknown models intentionally estimate to `0`, and OpenAI-compatible relay or local models may bill differently from their public model-name match.

## Naming compatibility

Autoto accepts legacy configuration paths and route aliases for backward compatibility. Canonical values always take precedence. The compatibility lifecycle and removal gates are defined internally: no legacy surface may be removed before v1.0.0 or without at least two tagged releases of migration runway.
