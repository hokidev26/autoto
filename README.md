# Autoto

English | [繁體中文](docs/README.zh-TW.md) | [简体中文](docs/README.zh-CN.md)

[![Release](https://img.shields.io/github/v/release/hokidev26/autoto)](https://github.com/hokidev26/autoto/releases)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![Go 1.26+](https://img.shields.io/badge/Go-1.26%2B-00ADD8?logo=go)](go.mod)
[![Platforms](https://img.shields.io/badge/Platforms-macOS%20%7C%20Linux%20%7C%20Windows-lightgrey)](#quick-start-cli)

> **Autoto** is a coding agent that runs on your own machine.
> You give it a task, it works in the background, and it asks before it does anything you would want to be asked about.
> **Task → background run → approval → run summary → diff → explicit-path commit**

## Why Autoto

| Pain point | What Autoto does |
|---|---|
| AI takes too long, you're stuck watching | Tasks run in the background; queue them and walk away |
| AI edits a pile of files | You review a diff; commit only stages the paths you pick |
| Can't see progress from your phone | Phone UI is built for small screens, installable as a PWA |
| Can't approve risky operations remotely | Phone UI or Telegram private chat, one-time approvals only |
| Can't reach your machine from outside | Open a temporary Cloudflare tunnel from Settings; password-gated, expires |
| Want to run several tasks at once | Queue them; background agents pick them up |
| Need to correct a run that's still going | Queue the follow-up; it joins the same run after this tool turn |
| Want to try new features without dirtying main | Fork into an isolated Git worktree, merge after a clean preflight |

**Autoto will not** push, amend, reset, force, clean, or `git add -A` on your behalf. No permission mode, allow rule, or approval can execute an irreversible operation such as recursive deletes, raw disk writes, or piping a download straight into a shell. `.env*`, credential files, private keys, and `.git` contents are hard-blocked.

## Quick start (CLI)

Download `autoto_<version>_<OS>_<arch>` from [GitHub Releases](https://github.com/hokidev26/autoto/releases):

| OS | File | Arch |
|---|---|---|
| **Windows** | `autoto_<version>_windows_amd64.zip` | x64 |
| **Windows** | `autoto_<version>_windows_arm64.zip` | ARM |
| **macOS** | `autoto_<version>_darwin_arm64.tar.gz` | Apple Silicon |
| **macOS** | `autoto_<version>_darwin_amd64.tar.gz` | Intel |
| **Linux** | `autoto_<version>_linux_amd64.tar.gz` | x64 |
| **Linux** | `autoto_<version>_linux_arm64.tar.gz` | ARM |

```bash
# macOS / Linux
./autoto

# Windows
autoto.exe
```

Open http://localhost:16888

Default state: config `~/.autoto/config.json`, database `~/.autoto/autoto.db`, projects `~/projects`.

No provider yet? Open **Settings → Providers** and add an API key, or use the built-in `cliproxyapi` preset.

### Native desktop shell (optional)

Download `autoto-desktop_<version>_<OS>_<arch>.tar.gz` if you want a native window. Releases ship **macOS (arm64 / amd64)** and **Linux amd64**. Windows builds from source; see [docs/BUILD.md](docs/BUILD.md) and [docs/DESKTOP_PACKAGING.md](docs/DESKTOP_PACKAGING.md).

## Build from source

Needs **Go 1.26+** (`go.mod`).

```bash
git clone https://github.com/hokidev26/autoto
cd autoto
go run ./cmd/autoto
```

Full build, desktop flags, and Windows toolchain notes: [docs/BUILD.md](docs/BUILD.md).

## Configuration

First run writes `~/.autoto/config.json`. Common overrides:

```text
AUTOTO_DEFAULT_MODEL
AUTOTO_SUMMARY_MODEL
AUTOTO_CONTEXT_TOKEN_LIMIT
```

Provider keys, Telegram / Home Assistant `env:` secret refs, CLIProxyAPI, and Agent Server backends: [docs/CONFIGURATION.md](docs/CONFIGURATION.md).

## What it does

- Background runs with approval before risky work
- Explicit-path Git commits only; status / diff / log / timeline are read-only
- Phone-first UI, optional PWA, optional native desktop shell
- Local accounts (admin / operator / collaborator / guest) and remote collaboration
- Schedules, Webhook / Telegram delivery, and a constrained Home Assistant adapter
- Local MCP plugins and an OpenAI-compatible `/v1` gateway

Capability list: [docs/FEATURES.md](docs/FEATURES.md). Internals: [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).

## Platform support

| Component | Windows | macOS | Linux |
|---|---|---|---|
| CLI | amd64 / arm64 | arm64 / amd64 | amd64 / arm64 |
| Desktop native window | Build from source | arm64 / amd64 | amd64 |

## Troubleshooting

**Windows SmartScreen.** Right-click `autoto.exe` → Properties → Unblock, or **More info → Run anyway**.

**macOS Gatekeeper.** System Settings → Privacy & Security → **Open Anyway**.

**Port in use.** Default is 16888. Change `server.port` in `~/.autoto/config.json` or **Settings → Servers/System**.

**Stale SQLite lock.** If the process was killed, delete `~/.autoto/autoto.db-shm` and `~/.autoto/autoto.db-wal`, then restart.

**Remote access.** Do not expose Autoto on the public internet. Use **Settings → Remote Access** (Cloudflare tunnel, password, expiry).

Windows walkthrough: [docs/WINDOWS_RUN.md](docs/WINDOWS_RUN.md).

## Documentation

See [docs/README.md](docs/README.md) for the full index (build, architecture, plugins, changelog, security, contributing).

## License

[MIT](LICENSE) — Copyright (c) 2026 Autoto contributors. Third-party notices: [docs/THIRD_PARTY_NOTICES.md](docs/THIRD_PARTY_NOTICES.md).
