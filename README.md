# Autoto

English | [繁體中文](README.zh-TW.md) | [简体中文](README.zh-CN.md)

[![Release](https://img.shields.io/github/v/release/hokidev26/autoto)](https://github.com/hokidev26/autoto/releases)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![Go 1.26+](https://img.shields.io/badge/Go-1.26%2B-00ADD8?logo=go)](go.mod)
[![Platforms](https://img.shields.io/badge/Platforms-macOS%20%7C%20Linux%20%7C%20Windows-lightgrey)](#installation)

> **Autoto** is a coding agent that runs on your own machine.
> You give it a task, it works in the background, and it asks before it does anything you would want to be asked about.
> **Task → background run → approval → run summary → diff → explicit-path commit**

---

## Why Autoto

| Pain point | What Autoto does |
|---|---|
| AI takes too long, you're stuck watching | Tasks run in the background; queue them and walk away |
| AI edits a pile of files | You review a diff; commit only stages the paths you pick |
| Can't see progress from your phone | Phone UI is built for small screens, installable as a PWA |
| Can't approve risky operations remotely | Phone UI or Telegram private chat, one-time approvals only |
| Can't reach your machine from outside | Open a temporary Cloudflare tunnel from Settings; password-gated, expires |
| Want to run several tasks at once | Queue them; background agents pick them up |
| Want to try new features without dirtying main | Fork into an isolated Git worktree, merge after a clean preflight |
| AI keeps repeating the same call | Consecutive repeats trigger an escalating reminder (never a veto) |
| Tool output blows up the context | Spills to disk past a threshold; the model pages with Read/Grep |
| Want to share models with other tools | Expose providers as an OpenAI-compatible `/v1` endpoint, per-key quotas |

**Autoto will not** push, amend, reset, force, clean, or `git add -A` on your behalf. No permission mode, allow rule, or approval can execute an irreversible operation such as recursive deletes, raw disk writes, or piping a download straight into a shell. `.env*`, credential files, private keys, and `.git` contents are hard-blocked: Read/Write/Edit refuse them, Glob/Grep skip them.

---

## Quick start (CLI)

Download `autoto_<version>_<OS>_<arch>` from the [GitHub Releases](https://github.com/hokidev26/autoto/releases) page:

| OS | File | Arch |
|---|---|---|
| **Windows** | `autoto_<version>_windows_amd64.zip` | x64 |
| **Windows** | `autoto_<version>_windows_arm64.zip` | ARM |
| **macOS** | `autoto_<version>_darwin_arm64.tar.gz` | Apple Silicon |
| **macOS** | `autoto_<version>_darwin_amd64.tar.gz` | Intel |
| **Linux** | `autoto_<version>_linux_amd64.tar.gz` | x64 |
| **Linux** | `autoto_<version>_linux_arm64.tar.gz` | ARM |

Unpack and run:

```bash
# macOS / Linux
./autoto

# Windows
autoto.exe
```

Open http://localhost:16888

> **Default state paths**
> ```
> Config:   ~/.autoto/config.json
> Database: ~/.autoto/autoto.db
> Projects: ~/projects
> ```

> **No provider yet?** Open `Settings → Providers` and add an API key for OpenAI / Anthropic / Gemini / an OpenAI-compatible relay, or use the built-in `cliproxyapi` preset.

### Native desktop shell (optional)

If you prefer a native window instead of a browser tab, download `autoto-desktop_<version>_<OS>_<arch>.tar.gz`:

> The desktop shell needs each platform's native WebView toolchain, so it cannot be cross-compiled. GitHub Releases only publish **macOS (arm64 / amd64)** and **Linux amd64**.
> Windows desktop builds from source fine but is not packaged in releases, see [Building from source](#building-from-source) below.

---

## Building from source

Requires **Go 1.26+** (declared in `go.mod`).

### CLI (cross-platform)

```bash
git clone https://github.com/hokidev26/autoto
cd autoto
go run ./cmd/autoto
```

### Desktop (requires native WebView)

```bash
# macOS / Linux
go build -tags desktop -o autoto-desktop ./cmd/autoto-desktop

# Windows (extra flag avoids a console window)
go build -tags desktop -ldflags "-H windowsgui" -o autoto-desktop.exe ./cmd/autoto-desktop
```

### Lean release-style binary

Add `-trimpath -ldflags "-s -w"` (about 25% smaller; panic stack traces still keep function names but lose file paths, and `pprof`-style tooling loses symbol detail):

```bash
go build -trimpath -ldflags "-s -w" -o autoto ./cmd/autoto
go build -tags "desktop,production" -trimpath -ldflags "-s -w -H windowsgui" -o autoto-desktop ./cmd/autoto-desktop
```

The `production` tag additionally disables the Wails devtools. See `docs/BUILD.md` and `Makefile` for the full reference.

---

## Configuration

Autoto creates `~/.autoto/config.json` on first run if it does not exist. The schema includes a `version` field; legacy configs without it are loaded as version `1` and normalized in memory.

### Agent model

```text
AUTOTO_DEFAULT_MODEL        # default agent model
AUTOTO_SUMMARY_MODEL        # smaller model for summaries
AUTOTO_CONTEXT_TOKEN_LIMIT  # context token ceiling
```

### Providers (environment variables)

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

### Automation integrations (env var over hardcoded secrets)

Telegram and Home Assistant connections are stored as non-secret metadata plus a logical secret reference. Only the `env:VAR_NAME` format is accepted; the UI and connection APIs reject plaintext tokens, and public responses only expose whether each required secret is configured.

```sh
export AUTOTO_TELEGRAM_BOT_TOKEN='replace-with-current-bot-token'
export AUTOTO_HOME_ASSISTANT_TOKEN='replace-with-current-ha-token'
```

Then reference them in the connection config:

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

Telegram is fixed to the official API endpoint and only receives updates through long polling. Generate a short-lived pairing code in the UI, then send `/pair <code>` from a private chat. The accepted command set is `/status`, `/approve <toolCallId>` (one-time only), and `/deny <toolCallId> [reason]`. There is no `/task` and no free-form chat. Rotating the bot token rotates its revision and revokes all existing pairings automatically.

Home Assistant endpoints must be loopback, `.local`, link-local, or private-network hosts.

### CLIProxyAPI preset

Autoto ships a built-in `cliproxyapi` provider profile for local [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) instances:

```text
Provider: cliproxyapi
Type:     openai-compatible
Base URL: http://127.0.0.1:8317/v1
Model:    gpt-5.5
```

Start CLIProxyAPI, then open **Settings → Providers → Codex credentials + relay** inside Autoto. Codex credentials are imported by pasting a Codex auth JSON, refresh token list, or token/account rows directly into CLIProxyAPI; Autoto refreshes CLIProxyAPI auth files and `/v1/models` afterwards.

To make new projects default to this preset:

```sh
AUTOTO_DEFAULT_MODEL=cliproxyapi:gpt-5.5 ./autoto
```

### Agent Server backend

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

Local backends use `X-Session-API-Key`; cloud backends use `Authorization: Bearer ...`.

---

## Features

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
- Sensitive-path hard blocking for the file path tools: `Read`, `Write`, and `Edit` reject protected files, while `Glob` and `Grep` omit them. The blocked set includes `.env*`, credential/secret files, common private-key material, and `.git` contents
- Plan mode: a plan-mode turn can only inspect read-only tools and must emit a structured plan. The UI shows a plan card instead of the raw JSON, keeps executed/cancelled cards after reopen, and compactly labels the synthetic execute/replan prompts. Isolated review may automatically replan once when the draft admits it missed the goal; a human still has to approve, then separately execute. Reviewer pass is not approval.
- Danger reflection for Bash tool calls: a configurable LLM safety gate (off / loose / medium / strict) that uses the active conversation model to review high-risk commands before execution and blocks or allows them based on a structured verdict
- Continuation budget settings panel: per-workspace limits on continuations, total turns, total tokens, and wall-clock run duration; defaults to no limit, and negative values explicitly opt out of any ceiling
- Project drag-to-reorder in the sidebar, with server-persisted order

### Security boundaries

- Sensitive-path hard blocking: `Read`, `Write`, `Edit` reject protected files; `Glob` and `Grep` skip them. Scope covers `.env*`, credential/secret files, common private-key material, and `.git` contents
- Plan review is isolated and tool-free. A `needs_human` verdict can start one plan-mode retry; `pass` still cannot approve or execute. Unavailable review fails closed to the human.
- Bash danger reflection: tunable LLM safety gate (off / loose / medium / strict) that uses the active conversation model to review high-risk commands before execution
- Recursive deletes, raw disk writes, permission weakening, and piping a download straight into a shell are classified as irreversible and execute-never, regardless of permission mode, allow rule, or approval
- Git workspace APIs are limited to status, diff, log, and explicit-path commits. Autoto does not automatically push, amend, reset, clean, force, or `git add -A`

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
- Local plugin registry that installs stdio MCP plugins from local directories as dynamically discovered agent tools (`plugin__<slug>__<tool>`): always installed disabled, enabling requires an explicit local-code-execution confirmation, processes run with a clean environment and `env:` secret references, the manifest supports a per-plugin timeout, and update/health-check endpoints cover manifest changes; see [docs/PLUGINS.md](docs/PLUGINS.md)

### Interface and experience

- UI built for small screens, not merely shrunk to fit: pull-to-refresh, swipe-to-dismiss notifications, and a composer that stays reachable. Installs to a home screen as a standalone PWA, opens without browser chrome, and behaves like an app
- Settings modal search/filter with keyboard focus shortcut for quickly locating growing product configuration panels
- Chat message copy actions for exporting individual messages and the current conversation as Markdown
- Versioned private chat drafts per logged-in user and Agent, with browser-local drafts retained only as an unauthenticated compatibility fallback
- Unicode/case-insensitive local account handles, `@handle` suggestions, and immutable user-message corrections with retained/new attachments
- Clipboard image/file attachments, Unicode-safe localized draft limits, and browser-native text undo/redo
- Browser-local prompt history for the chat composer, with empty-input ↑/↓ recall and migration through local preference backups
- Chat-composer slash command palette backed by enabled local Skills command templates
- Browser-local Settings → Profile preferences for display identity, avatar initials, workspace label, and Git identity helpers
- Browser-local Settings → Network Search preferences for provider presets, result limits, confirmation, and domain rules; `WebSearch` and `WebFetch` core tools provide public web/documentation lookup
- Browser-local Settings → Notifications preferences for toast categories, display duration, and UI terminal notices; server-backed durable Webhook / Telegram delivery history and retry
- Browser-local Settings → Appearance preferences for theme, density, terminal default visibility, and Agent event-log display
- Settings → Servers/System + Runtime panels for process, Go runtime, paths, and Agent limits
- Settings → Users: administrators can create collaborators (password sign-in, working project membership) and conversation-only guests (access keys, watch-only membership). Guest limits are enforced by the server.
- Settings → Storage panel for config, database, home, and project-directory footprint
- Settings → Usage panel for projects, messages, tool calls, model requests, estimated token cost, and backends
- Settings → About dependency-license panel backed by the development-time `/api/licenses` endpoint
- Settings → About browser-local preferences backup/import for migrating profile, skills, chat drafts, prompt history, search, IM, notification, appearance, terminal, recent directory, model, and relay-protocol settings
- Settings → IM gateway automation control backed by server APIs for schedules, notification history/retry, Telegram and Home Assistant connection metadata, pairing/revocation, monitoring, device state, local device-action confirmation, and audit events. A detected legacy browser-local IM draft is shown only as a disabled migration hint and never starts a channel

### Agent WebSocket protocol

- Protocol v2 on `/ws/agent`, with per-process monotonic sequence, bounded in-memory replay, and authoritative live-snapshot resync. It is **not** a durable or cross-process event log

---

## Platform support

| Component | Windows | macOS | Linux |
|---|---|---|---|
| CLI | ✅ amd64 / arm64 | ✅ arm64 / amd64 | ✅ amd64 / arm64 |
| Desktop native window | ⚠️ Build from source (not in Releases) | ✅ arm64 / amd64 | ✅ amd64 |

The desktop shell links each platform's native WebView and cannot be cross-compiled.

---

## Troubleshooting

### Windows: "Windows protected your PC"

Autoto is not Authenticode-signed. To run it:

1. Right-click `autoto.exe` → **Properties**
2. Check **Unblock** → **Apply**
3. Or in the SmartScreen dialog, click **More info** → **Run anyway**

Code signing is on the roadmap; it requires buying and maintaining a certificate.

### macOS: "Cannot be opened because it is from an unidentified developer"

Gatekeeper rejects the first run of `autoto` in Finder:

1. Open **System Settings → Privacy & Security**
2. Scroll to the bottom; you will see "autoto was blocked"
3. Click **Open Anyway**

Notarization is on the roadmap; it requires an Apple Developer account (annual fee USD 99).

### Port already in use

The default port is 16888. To change it, edit `~/.autoto/config.json`:

```json
{
  "server": { "host": "localhost", "port": 17888 }
}
```

Or change it through the Web UI under **Settings → Servers/System**.

### SQLite lock left behind

If the process is forcibly killed, the database may leave a stale lock. Delete `~/.autoto/autoto.db-shm` and `~/.autoto/autoto.db-wal`, then restart.

### Reaching your machine from outside

Do **not** expose Autoto directly to the public internet. Open **Settings → Remote Access** and start a temporary Cloudflare tunnel; you get a URL and a QR code to scan, password-gated with automatic expiry.

---

## Usage cost estimates

Autoto records provider usage in `api_requests` and shows aggregate estimated cost in **Settings → Usage**. Cost is calculated from a small built-in USD-per-million-token table in `internal/pricing/pricing.go`. The table was last reviewed on 2026-07-07 against public pricing pages: [OpenAI API pricing](https://developers.openai.com/api/docs/pricing), [OpenAI GPT-4.1 pricing announcement](https://openai.com/index/gpt-4-1/), and [Anthropic Claude pricing](https://docs.anthropic.com/en/docs/about-claude/pricing). Unknown models intentionally estimate to `0`, and OpenAI-compatible relay or local models may bill differently from their public model-name match.

---

## Requirements

- Go 1.26 or newer, as declared in `go.mod`
- SQLite is provided through the pure-Go `modernc.org/sqlite` driver; no system sqlite3 needed
- Node.js is optional and only used for `node --check` and `node --test` on embedded frontend scripts during validation

---

## Naming compatibility

Autoto accepts legacy configuration paths and route aliases for backward compatibility. Canonical values always take precedence. The compatibility lifecycle and removal gates are defined internally: no legacy surface may be removed before v1.0.0 or without at least two tagged releases of migration runway.

---

## Documentation

- `docs/BUILD.md` — building from source
- `docs/WINDOWS_RUN.md` — running on Windows
- `docs/ARCHITECTURE.md` — architecture overview
- `docs/PLUGINS.md` — local MCP plugins
- `docs/DESKTOP_PACKAGING.md` — desktop packaging boundaries
- `CHANGELOG.md` — changelog
- `SECURITY.md` — vulnerability reporting
- `CONTRIBUTING.md` — contributing guide
- `AGENTS.md` — agent behavior guidelines
- `THIRD_PARTY_NOTICES.md` — third-party dependency licenses

---

## License

[MIT](LICENSE) — Copyright (c) 2026 Autoto contributors
