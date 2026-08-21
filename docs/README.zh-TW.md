# Autoto

[English](../README.md) | 繁體中文 | [簡體中文](README.zh-CN.md)

[![Release](https://img.shields.io/github/v/release/hokidev26/autoto)](https://github.com/hokidev26/autoto/releases)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](../LICENSE)
[![Go 1.26+](https://img.shields.io/badge/Go-1.26%2B-00ADD8?logo=go)](../go.mod)
[![Platforms](https://img.shields.io/badge/Platforms-macOS%20%7C%20Linux%20%7C%20Windows-lightgrey)](#快速開始命令列版)

> **Autoto** 是跑在你自己機器上的 coding agent。
> 你給它一個任務，它在背景做；遇到你會想被問一聲的事，它會先問。
> **任務 → 背景 run → 核准 → run 摘要 → diff → 指定路徑 commit**

## 為什麼用 Autoto

| 痛點 | Autoto 的做法 |
|---|---|
| AI 跑太久，只能坐著等 | 任務在背景跑，送出後就可以做別的事 |
| AI 改了一堆檔案 | 結束後看 diff；commit 只暫存你點名的路徑 |
| 出門後看不到進度 | 手機 UI 按小螢幕設計，可裝到主畫面當 PWA |
| 出門後不能核准危險操作 | 手機 UI 或 Telegram 私聊，一次性核准 |
| 想從外面連回自己的電腦 | 在設定裡開臨時 Cloudflare tunnel，有密碼、會過期 |
| 想同時跑幾個任務 | 排隊即可，背景 agent 接著做 |
| 它還在跑，想再補一句 | 排隊；等本輪工具結束後進入同一趟 run |
| 想試新功能又不想弄髒主分支 | Fork 到獨立 Git worktree，預檢通過再合併 |

**Autoto 不會**自動 push、amend、reset、force、clean 或 `git add -A`。任何權限模式、allow rule、核准都不能執行遞迴刪除、直接寫磁碟、把下載灌進 shell 這類不可逆操作。`.env*`、憑證、私鑰、`.git` 內容會被硬攔截。

## 快速開始（命令列版）

從 [GitHub Releases](https://github.com/hokidev26/autoto/releases) 下載 `autoto_<版本>_<OS>_<arch>`：

| OS | 檔案 | 架構 |
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

開啟 http://localhost:16888

預設路徑：設定 `~/.autoto/config.json`，資料庫 `~/.autoto/autoto.db`，專案 `~/projects`。

還沒有模型？開啟 **設定 → Providers**，填 API key，或使用內建 `cliproxyapi` 預設。

### 可選的桌面視窗

需要原生視窗時下載 `autoto-desktop_<version>_<OS>_<arch>.tar.gz`。Release 只提供 **macOS (arm64 / amd64)** 與 **Linux amd64**。Windows 可從原始碼建構，見 [BUILD.md](BUILD.md) 與 [DESKTOP_PACKAGING.md](DESKTOP_PACKAGING.md)。

## 從原始碼建構

需要 **Go 1.26+**（見 `go.mod`）。

```bash
git clone https://github.com/hokidev26/autoto
cd autoto
go run ./cmd/autoto
```

完整建構、桌面參數與 Windows 工具鏈說明：[BUILD.md](BUILD.md)。

## 設定

首次執行會寫入 `~/.autoto/config.json`。常用覆寫：

```text
AUTOTO_DEFAULT_MODEL
AUTOTO_SUMMARY_MODEL
AUTOTO_CONTEXT_TOKEN_LIMIT
```

Provider 金鑰、Telegram / Home Assistant 的 `env:` 參照、CLIProxyAPI 與 Agent Server：[CONFIGURATION.md](CONFIGURATION.md)。

## 它能做什麼

- 背景執行，危險操作先核准
- Git 只做指定路徑 commit；status / diff / log / 時間線唯讀
- 為手機設計的 UI，可選 PWA 與原生桌面視窗
- 本機帳號（管理員 / 操作者 / 協作者 / 訪客）與遠端協作
- 排程、Webhook / Telegram 投遞，以及受限的 Home Assistant 適配
- 本機 MCP 外掛，以及 OpenAI 相容的 `/v1` 閘道

能力清單：[FEATURES.md](FEATURES.md)。內部結構：[ARCHITECTURE.md](ARCHITECTURE.md)。

## 平台

| 元件 | Windows | macOS | Linux |
|---|---|---|---|
| CLI | amd64 / arm64 | arm64 / amd64 | amd64 / arm64 |
| 原生桌面視窗 | 從原始碼建構 | arm64 / amd64 | amd64 |

## 疑難排解

**Windows SmartScreen。** 右鍵 `autoto.exe` → 內容 → 解除封鎖，或 **更多資訊 → 仍要執行**。

**macOS Gatekeeper。** 系統設定 → 隱私權與安全性 → **仍要打開**。

**連接埠占用。** 預設 16888。改 `~/.autoto/config.json` 裡的 `server.port`，或到 **設定 → Servers/System**。

**SQLite 鎖殘留。** 行程被強制結束後，刪除 `~/.autoto/autoto.db-shm` 與 `~/.autoto/autoto.db-wal` 再啟動。

**遠端存取。** 不要把 Autoto 直接暴露到公網。用 **設定 → Remote Access**（Cloudflare tunnel、密碼、過期）。

Windows 操作說明：[WINDOWS_RUN.md](WINDOWS_RUN.md)。

## 文件

完整目錄見 [README.md](README.md)。

## 授權

[MIT](../LICENSE) — Copyright (c) 2026 Autoto contributors。第三方聲明：[THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md)。
