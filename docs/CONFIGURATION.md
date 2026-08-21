# Configuration

Environment variables, provider presets, and automation secret references. The landing page is [README.md](../README.md).

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
