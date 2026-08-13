# Autoto

English | [繁體中文](README.zh-TW.md) | [簡體中文](README.zh-CN.md)

Autoto is a coding agent that runs on your own machine. You give it a task, it works in the background, and it asks before it does anything you would want to be asked about.

**Task → background run → approval → run summary → diff → explicit-path commit**

![Autoto local agent workflow demo](docs/demo.svg)

## What you can do with it

**Give it work and walk away.** Tasks run in the background, so you can queue several and let them proceed. Each run ends with a summary, a diff to review, and a commit that stages only the paths you picked. It never pushes, amends, resets, or runs `git add -A` on your behalf.

**Keep an eye on it from your phone.** The UI is built for small screens, not merely shrunk to fit: pull-to-refresh, swipe-to-dismiss notifications, and a composer that stays reachable. It installs to a home screen as a standalone app, so it opens without browser chrome and behaves like an app.

**Reach your machine from outside your network.** Open a temporary Cloudflare tunnel from the settings panel and get a URL and a QR code to scan. Remote sessions are password-gated and have two modes: restricted for following along and approving, or full control when you explicitly allow it. Sessions expire, and tightening the policy revokes the ones already open.

**Approve from your phone, or from Telegram.** When a run needs permission for something risky, you can approve or deny it remotely. Telegram pairing is private-chat only and deliberately narrow: status, one-time approve, deny. It is not a chat assistant and there is no `/task`.

**Share your models as an API.** Autoto can expose the providers you have configured as an OpenAI-compatible `/v1` endpoint for your other tools and devices. Each key gets its own model whitelist and usage accounting, so you can hand one out without handing over everything.

**Work in an isolated copy.** Fork a workline into its own Git worktree, let the agent work there, then merge back after a preflight check that the merge is clean.

## What it will not do

Autoto is an experimental local-development MVP, not a hardened multi-user or production service.

Some operations are refused outright rather than merely gated. Recursive deletes, raw disk writes, permission weakening, and piping a download straight into a shell are classified as irreversible, and no permission mode, allow rule, or approval can execute them. The gate is deliberately not something you can turn off for convenience.

Files that usually hold secrets are hard-blocked from the file tools: `.env*`, credential and key material, and `.git` contents. `Read`, `Write`, and `Edit` refuse them; `Glob` and `Grep` leave them out of results entirely.

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

The full per-item list follows. It is deliberately exhaustive and reads as a specification rather than a tour; the sections above cover what most people want to know.

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
- Danger reflection for Bash tool calls: a configurable LLM safety gate (off / loose / medium / strict) that uses the active conversation model to review high-risk commands before execution and blocks or allows them based on a structured verdict
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

Tagged releases publish two kinds of binary. Both serve the same product; they differ only in how you reach the UI.

**CLI** — `autoto_<version>_<os>_<arch>`, for macOS, Linux, and Windows on both amd64 and arm64. It runs a local server and you open the UI in your browser. Cross-compiled, so every platform is built from one job.

**Desktop** — `autoto-desktop_<version>_<os>_<arch>.tar.gz`, for macOS (arm64 and amd64) and Linux amd64. Same server with a native window instead of a browser tab. Each one is built on its own runner because the shell links that platform's system WebView, which cannot be cross-compiled.

Download the matching asset from GitHub Releases, unpack it, then run the binary. Checksums are published alongside: `checksums.txt` for the CLI archives, and a `.sha256` file next to each desktop archive.

Two limits worth knowing before you download the desktop build. It is not code-signed or notarized, so macOS Gatekeeper will refuse it on first launch until you allow it explicitly in System Settings → Privacy & Security, and Windows may warn similarly. And Linux desktop is amd64 only; on arm64 Linux, use the CLI.

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

### Building binaries

CLI (cross-compiles to any supported platform):

```bash
go build -o autoto ./cmd/autoto
```

Desktop requires the `desktop` build tag — without it the build fails with "build constraints exclude all Go files" — plus the platform's native WebView toolchain, so it cannot be cross-compiled. Windows desktop is not published in releases, but it builds from source fine:

```bash
go build -tags desktop -o autoto-desktop ./cmd/autoto-desktop
```

On Windows, add `-ldflags "-H windowsgui"` so the desktop shell opens without a console window.

For smaller release-style binaries, strip debug info and local paths (roughly 25% smaller; panic stack traces keep function names but lose file paths, and tools like `pprof` lose symbol detail):

```bash
go build -trimpath -ldflags "-s -w" -o autoto ./cmd/autoto
go build -tags "desktop,production" -trimpath -ldflags "-s -w -H windowsgui" -o autoto-desktop ./cmd/autoto-desktop
```

The `production` tag additionally disables the Wails devtools. See `docs/BUILD.md` and the `Makefile` for the full build reference.

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

Start CLIProxyAPI, then use **Settings → Providers → Codex 憑證 + 中轉站** inside Autoto. Codex uses credential import: paste a Codex auth JSON, refresh token list, or token/account rows and import them directly into CLIProxyAPI; Autoto refreshes CLIProxyAPI auth files and `/v1/models` afterwards. The same page also includes an embedded relay/provider form for API Key, Base URL, protocol selection, and default model. Saving the form updates Autoto's runtime provider registry immediately and persists non-secret provider settings to `config.json`; API keys are intentionally not written to disk. You can pick a preferred model before creating a project, and Autoto will use it for the new Agent. To make new projects use the preset by default, start Autoto with `AUTOTO_DEFAULT_MODEL=cliproxyapi:gpt-5.5`. If your CLIProxyAPI config enables client `api-keys`, export `CLIPROXYAPI_API_KEY` before starting Autoto. You can override the local endpoint or fallback model with `CLIPROXYAPI_BASE_URL` and `CLIPROXYAPI_MODEL`. Autoto uses `CLIPROXYAPI_MANAGEMENT_KEY` for local management API calls.

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