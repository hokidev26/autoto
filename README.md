# Autoto

Autoto is a local-first coding-agent server that turns a task into a background run with approval-gated tools, a run summary, diff review, and an explicit-path local commit.

**Task → background run → approval → run summary → diff → explicit-path commit**

![Autoto local agent workflow demo](docs/demo.svg)

Autoto is an experimental local-development MVP, not an untrusted multi-user or production service. Its current remote control surface is deliberately narrow: Telegram uses Bot API long polling for private-chat pairing, minimal status, one-time tool approval, and denial. It is not a general IM assistant: there is no `/task`, free-form chat, Telegram webhook receiver, Slack, or Discord channel.

## Quick start

Requires Go 1.26 or newer:

```bash
go run ./cmd/autoto
```

Then open:

```text
http://localhost:16888
```

Default local state:

```text
Config:   ~/.autoto/config.json
Database: ~/.autoto/autoto.db
Projects: ~/projects
```

## Features

- Local HTTP server with embedded HTML/CSS/JS UI, using a no-build ES module seam for frontend bootstrap/runtime helpers and extracted Settings local-preference panels
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
  - Bash
  - Glob
  - Grep
  - WebFetch
  - WebSearch
  - MCPListTools
  - MCPCallTool
- Sensitive-path hard blocking for the file path tools: `Read`, `Write`, and `Edit` reject protected files, while `Glob` and `Grep` omit them. The blocked set includes `.env*`, credential/secret files, common private-key material, and `.git` contents
- Danger reflection for Bash tool calls: a configurable LLM safety gate (off / loose / medium / strict) that intercepts high-risk commands before execution and blocks or allows them based on a structured verdict
- Continuation budget settings panel: per-workspace limits on continuations, total turns, total tokens, and wall-clock run duration; defaults to no limit, and negative values explicitly opt out of any ceiling
- Project drag-to-reorder in the sidebar, with server-persisted order
- Git workspace APIs and UI for status, diff, log, and explicit-path commits without automatic push, amend, reset, clean, force, or `git add -A`
- Agent WebSocket protocol 2 on `/ws/agent`, with per-process monotonic sequence, bounded in-memory replay, and authoritative live-snapshot resync; it is not a durable or cross-process event log
- SQLite migrations V19–V22 and server APIs for schedules, durable notification deliveries, integration connections, channel pairings/events/cursors, and device-action requests
- Schedule worker with cron/`@every` expressions and IANA time zones. Schedule permission is limited to `readOnly` or `acceptEdits`, persists as a run permission cap, and skips a busy Agent without interrupting or replacing its manual run. Unattended runs do not reuse session approvals granted interactively, and a schedule prompt containing a command that stops or restarts Autoto itself is rejected at create and update
- Durable Webhook/Telegram notification delivery history with deduplication, leases, exponential backoff, bounded attempts, delivered/dead states, aggregate metrics, and explicit retry
- Telegram Bot API long polling with private-chat `/pair`, `/status`, `/approve <toolCallId>` (always one-time `allow_once`), and `/deny <toolCallId> [reason]`; unauthenticated commands and failed pairing attempts are silent, and processed updates are protected by persisted event IDs and cursors
- Home Assistant integration restricted to local/private endpoints: read-only state/entity summaries, a fixed action allowlist, short-lived action requests, two local UI confirmations, and direct-loopback approval. Unknown/critical actions such as door unlock and camera snapshot are hard-blocked, and IM cannot control devices
- Local monitoring aggregation for active runs, pending approvals, schedules, delivery states, channels, device actions, and automation-worker health
- Runtime Supervisor lifecycle for preview, Telegram channel services, automation workers, and HTTP serving
- Workline and container settings backed by project Workline/Agent APIs, with backend workline-fork support that creates Git worktrees, merge-check preflight, and clean-worktree merge APIs
- Interactive PTY terminal WebSocket: `/ws/terminal`, with terminal-management controls and browser-local retention/focus preferences
- Filesystem browse/preview/mkdir APIs
- Agent Server backend registry with sidebar and Agent Admin management UI for compatible OpenHands Agent Server endpoints
- Settings modal search/filter with keyboard focus shortcut for quickly locating growing product configuration panels
- Chat message copy actions for exporting individual messages and the current conversation as Markdown
- Versioned private chat drafts per logged-in user and Agent, with browser-local drafts retained only as an unauthenticated compatibility fallback
- Unicode/case-insensitive local account handles, `@handle` suggestions, and immutable user-message corrections with retained/new attachments
- Clipboard image/file attachments, Unicode-safe localized draft limits, and browser-native text undo/redo
- Browser-local prompt history for the chat composer, with empty-input ↑/↓ recall and migration through local preference backups
- Chat-composer slash command palette backed by enabled local Skills command templates
- Browser-local Settings → Profile preferences for display identity, avatar initials, workspace label, and Git identity helpers
- Browser-local Settings → Network Search policy preferences for provider presets, result limits, confirmation, and domain rules, plus `WebSearch` and `WebFetch` core tools for public web/documentation lookup
- Settings → P2–P3 automation control backed by server APIs for schedules, notification history/retry, Telegram and Home Assistant connection metadata, pairing/revocation, monitoring, device state, local device-action confirmation, and audit events. A detected legacy browser-local IM draft is shown only as a disabled migration hint and never starts a channel
- Server-backed Skills with global/project/workspace CRUD, effective-skill resolution, revision history/restore, and snapshot-stable cursor pagination. The Settings scoped panel can browse by scope, inspect details, paginate, and view/restore revisions; create, SKILL.md import, enable/disable, edit, and delete UI actions still operate only on global scope. MCP registry actions remain available with explicit exec-risk approval
- Server-backed global/project/agent lifecycle hooks for run/tool boundaries, with snapshot-stable dispatch, CAS updates, execution history, and isolated test executions that do not create ordinary Agent runs. Shell and HTTP actions reuse the existing tool approval/audit gateway; `env:` secrets resolve only after approval, Shell stays workspace-bound with a sanitized environment, and HTTP uses the existing SSRF-resistant direct network policy. LLM gates remain isolated, tool-free provider requests
- Browser-local Settings → Notifications preferences for toast categories, display duration, and UI terminal notices, plus server-backed durable Webhook/Telegram delivery history and retry
- Browser-local Settings → Appearance preferences for theme, density, terminal default visibility, and Agent event-log display
- Runtime summary endpoint and Settings → Servers/System + Runtime panels for process, Go runtime, paths, and Agent limits
- Settings → Users read-only auth status panel backed by `/api/auth/status`
- Local storage summary endpoint and Settings → Storage panel for config, database, home, and project-directory footprint
- Local usage summary endpoint and Settings → Usage panel for projects, messages, tool calls, model requests, estimated token cost, and backends
- Settings → About dependency-license panel backed by the development-time `/api/licenses` endpoint
- Settings → About browser-local preferences backup/import for migrating profile, skills, chat drafts, prompt history, search, IM, notification, appearance, terminal, recent directory, model, and relay-protocol settings

## Requirements

- Go 1.26 or newer, as declared in `go.mod`
- SQLite is provided through the pure-Go `modernc.org/sqlite` driver
- Node.js is optional and only used for `node --check` and `node --test` on embedded frontend scripts during validation

## Installation details

Tagged releases publish Autoto release assets for macOS, Linux, and Windows, named like `autoto_<version>_<os>_<arch>`. Download the matching asset from GitHub Releases, unpack it, then run the `autoto` binary.

From source:

```bash
go run ./cmd/autoto
```

Then open:

```text
http://localhost:16888
```

Default paths:

```text
Config:   ~/.autoto/config.json
Database: ~/.autoto/autoto.db
Projects: ~/projects
```

You can pass a custom config path:

```bash
go run ./cmd/autoto --config /path/to/config.json
```

## Dogfood demo (historical evidence)

The following tracked-file smoke was run before the rename, against temporary **Autoto** servers and temporary Git repositories. It is retained as a historical record; the same current workflow uses the canonical Agent APIs shown below.

```text
Write: Wrote 197 bytes to demo/notes.md inside the temporary project worktree
Read:  confirmed the new tracked diff review line
Grep:  notes.md:4:- Updated through Autoto Write tool for tracked diff review.
Status before commit: demo/notes.md was tracked and modified (worktree=M)
Diff:  demo/notes.md added=2 deleted=0
Patch excerpt:
  diff --git a/demo/notes.md b/demo/notes.md
  +- Updated through Autoto Write tool for tracked diff review.
Commit: 96cd79e Dogfood tracked diff workflow
Paths:  demo/notes.md
After commit: clean=true, remainingFiles=[]
```

An earlier untracked-file smoke also created and committed `demo/notes.md` with commit `2484ab7 Dogfood Autoto API workflow`.

`docs/demo.svg` is a lightweight tracked workflow preview. To replace it with a real product recording, capture a 15–20 second browser flow (create/open project → send task → approve tool call → review diff → commit selected path), compress it to a small asset, and update the README reference.

To reproduce manually, start Autoto with a temporary config, create or open a local Git repository as the project worktree, use the UI or tool-call API to write a small file, verify Git status, inspect the diff in the Git panel, select the file checkbox, enter a commit message, and submit the commit. The commit API stages only the selected paths and does not push, amend, reset, clean, force, or auto-stage the full worktree.

## Usage cost estimates

Autoto records provider usage in `api_requests` and shows aggregate estimated cost in Settings → Usage. Cost is calculated from a small built-in USD-per-million-token table in `internal/agent/loop.go`. The table was last reviewed on 2026-07-07 against public pricing pages: [OpenAI API pricing](https://developers.openai.com/api/docs/pricing), [OpenAI GPT-4.1 pricing announcement](https://openai.com/index/gpt-4-1/), and [Anthropic Claude pricing](https://docs.anthropic.com/en/docs/about-claude/pricing). Unknown models intentionally estimate to `0`, and OpenAI-compatible relay or local models may bill differently from their public model-name match.

## Configuration

On first run, Autoto creates a local config file if it does not exist. Runtime secrets can be supplied through environment variables. `config.json` includes a schema `version` field; legacy configs without it are loaded as version `1` and normalized in memory.

Agent-model environment variables:

```text
AUTOTO_DEFAULT_MODEL
AUTOTO_SUMMARY_MODEL
AUTOTO_CONTEXT_TOKEN_LIMIT
```

Provider environment variables:

```text
OPENAI_API_KEY
OPENAI_MODEL
ANTHROPIC_API_KEY
ANTHROPIC_MODEL
GEMINI_API_KEY
GEMINI_MODEL
GEMINI_BASE_URL
OPENAI_BASE_URL
OPENAI_COMPATIBLE_BASE_URL
OPENAI_COMPATIBLE_API_KEY
OPENAI_COMPATIBLE_MODEL
CLIPROXYAPI_BASE_URL
CLIPROXYAPI_API_KEY
CLIPROXYAPI_MODEL
CLIPROXYAPI_MANAGEMENT_KEY
CLIPROXYAPI_BIN
CLIPROXYAPI_CONFIG
```

### Automation integrations and secret references

Telegram and Home Assistant connections are stored as non-secret metadata plus logical secret references. The current reference format is only `env:VARIABLE_NAME`; plaintext bot/access tokens are rejected by the connection APIs and UI, and public responses expose only whether each required secret is configured.

For example, set the secret values in the Autoto process environment:

```sh
export AUTOTO_TELEGRAM_BOT_TOKEN='replace-with-current-bot-token'
export AUTOTO_HOME_ASSISTANT_TOKEN='replace-with-current-ha-token'
```

Then configure the connection with references such as:

```json
{
  "kind": "telegram",
  "name": "Personal Telegram",
  "secretRefs": { "botToken": "env:AUTOTO_TELEGRAM_BOT_TOKEN" }
}
```

```json
{
  "kind": "home-assistant",
  "name": "Home Assistant",
  "endpoint": "http://homeassistant.local:8123",
  "secretRefs": { "accessToken": "env:AUTOTO_HOME_ASSISTANT_TOKEN" }
}
```

Telegram is fixed to the official API endpoint and receives updates only through long polling. Generate a short-lived pairing code in the local UI/API, then send `/pair <code>` from a private chat. The accepted command set is `/status`, `/approve <toolCallId>` (one-time approval only), and `/deny <toolCallId> [reason]`. There is no `/task` or free-form assistant chat. Rotate the bot token if it may have leaked; the token revision changes and stale pairings are revoked, after which you must pair again. You can also revoke a pairing explicitly from the local UI/API.

Home Assistant endpoints must use loopback, `.local`, link-local, or private-network hosts. Rotate the Home Assistant token in the referenced environment variable if it may have leaked. Home Assistant has no channel pairing to revoke; disable/delete the connection and restart/retest it after rotation as appropriate.

### CLIProxyAPI preset

Autoto includes a built-in `cliproxyapi` provider profile for local [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) instances:

```text
Provider: cliproxyapi
Type:     openai-compatible
Base URL: http://127.0.0.1:8317/v1
Model:    gpt-5.5
```

Start CLIProxyAPI, then use **Settings → Providers → Codex 凭证 + 中转站** inside Autoto. Codex uses credential import: paste a Codex auth JSON, refresh token list, or token/account rows and import them directly into CLIProxyAPI; Autoto refreshes CLIProxyAPI auth files and `/v1/models` afterwards. The same page also includes an embedded relay/provider form for API Key, Base URL, protocol selection, and default model. Saving the form updates Autoto's runtime provider registry immediately and persists non-secret provider settings to `config.json`; API keys are intentionally not written to disk. You can pick a preferred model before creating a project, and Autoto will use it for the new Agent. To make new projects use the preset by default, start Autoto with `AUTOTO_DEFAULT_MODEL=cliproxyapi:gpt-5.5`. If your CLIProxyAPI config enables client `api-keys`, export `CLIPROXYAPI_API_KEY` before starting Autoto. You can override the local endpoint or fallback model with `CLIPROXYAPI_BASE_URL` and `CLIPROXYAPI_MODEL`. Autoto uses `CLIPROXYAPI_MANAGEMENT_KEY` for local management API calls.

Agent Server backend seed variables:

```text
AUTOTO_AGENT_BACKEND_URL
AUTOTO_AGENT_BACKEND_NAME
AUTOTO_AGENT_BACKEND_KIND
AUTOTO_AGENT_BACKEND_API_KEY
OPENHANDS_AGENT_SERVER_URL
OPENHANDS_SESSION_API_KEY
AGENT_SERVER_URL
AGENT_SERVER_API_KEY
```

If a backend URL is configured, Autoto seeds the backend registry on first startup. Local backends use `X-Session-API-Key`; cloud backends use `Authorization: Bearer ...`.

### Naming compatibility

Autoto accepts legacy configuration paths and route aliases for backward compatibility. Canonical values always take precedence. The compatibility lifecycle and removal gates are defined in `PROJECT_PLAN.md`; no legacy surface may be removed before v0.4.0 or without at least two tagged releases of migration runway.