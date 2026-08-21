# Autoto

[English](../README.md) | [繁體中文](README.zh-TW.md) | 简体中文

[![Release](https://img.shields.io/github/v/release/hokidev26/autoto)](https://github.com/hokidev26/autoto/releases)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](../LICENSE)
[![Go 1.26+](https://img.shields.io/badge/Go-1.26%2B-00ADD8?logo=go)](../go.mod)
[![Platforms](https://img.shields.io/badge/Platforms-macOS%20%7C%20Linux%20%7C%20Windows-lightgrey)](#快速开始命令行版)

> **Autoto** 是跑在你自己机器上的 coding agent。
> 你给它一个任务，它在后台做；遇到你会想被问一声的事，它会先问。
> **任务 → 后台 run → 批准 → run 摘要 → diff → 指定路径 commit**

## 为什么用 Autoto

| 痛点 | Autoto 的做法 |
|---|---|
| AI 跑太久，只能坐着等 | 任务在后台跑，交出去就可以做别的事 |
| AI 改了一堆文件 | 结束后看 diff；commit 只暂存你点名的路径 |
| 出门后看不到进度 | 手机 UI 按小屏设计，可装到主屏幕当 PWA |
| 出门后不能批准危险操作 | 手机 UI 或 Telegram 私聊，一次性批准 |
| 想从外面连回自己的电脑 | 在设置里开临时 Cloudflare tunnel，有密码、会过期 |
| 想同时跑几个任务 | 排队即可，后台 agent 接着做 |
| 它还在跑，想再补一句 | 排队；等本轮工具结束后进入同一趟 run |
| 想试新功能又不想弄脏主分支 | Fork 到独立 Git worktree，预检通过再合并 |

**Autoto 不会**自动 push、amend、reset、force、clean 或 `git add -A`。任何权限模式、allow rule、批准都不能执行递归删除、直接写磁盘、把下载灌进 shell 这类不可逆操作。`.env*`、凭据、私钥、`.git` 内容会被硬拦截。

## 快速开始（命令行版）

从 [GitHub Releases](https://github.com/hokidev26/autoto/releases) 下载 `autoto_<版本>_<OS>_<arch>`：

| OS | 文件 | 架构 |
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

打开 http://localhost:16888

默认路径：配置 `~/.autoto/config.json`，数据库 `~/.autoto/autoto.db`，项目 `~/projects`。

还没有模型？打开 **设置 → Providers**，填 API key，或使用内置 `cliproxyapi` 预设。

### 可选的桌面窗口

需要原生窗口时下载 `autoto-desktop_<version>_<OS>_<arch>.tar.gz`。Release 只提供 **macOS (arm64 / amd64)** 和 **Linux amd64**。Windows 可从源码构建，见 [BUILD.md](BUILD.md) 和 [DESKTOP_PACKAGING.md](DESKTOP_PACKAGING.md)。

## 从源码构建

需要 **Go 1.26+**（见 `go.mod`）。

```bash
git clone https://github.com/hokidev26/autoto
cd autoto
go run ./cmd/autoto
```

完整构建、桌面参数和 Windows 工具链说明：[BUILD.md](BUILD.md)。

## 配置

首次运行会写入 `~/.autoto/config.json`。常用覆盖：

```text
AUTOTO_DEFAULT_MODEL
AUTOTO_SUMMARY_MODEL
AUTOTO_CONTEXT_TOKEN_LIMIT
```

Provider 密钥、Telegram / Home Assistant 的 `env:` 引用、CLIProxyAPI 和 Agent Server：[CONFIGURATION.md](CONFIGURATION.md)。

## 它能做什么

- 后台执行，危险操作先批准
- Git 只做指定路径 commit；status / diff / log / 时间线只读
- 为手机设计的 UI，可选 PWA 和原生桌面窗口
- 本机账号（管理员 / 操作员 / 协作者 / 访客）和远端协作
- 调度、Webhook / Telegram 投递，以及受限的 Home Assistant 适配
- 本机 MCP 插件，以及 OpenAI 兼容的 `/v1` 网关

能力清单：[FEATURES.md](FEATURES.md)。内部结构：[ARCHITECTURE.md](ARCHITECTURE.md)。

## 平台

| 组件 | Windows | macOS | Linux |
|---|---|---|---|
| CLI | amd64 / arm64 | arm64 / amd64 | amd64 / arm64 |
| 原生桌面窗口 | 从源码构建 | arm64 / amd64 | amd64 |

## 排错

**Windows SmartScreen。** 右键 `autoto.exe` → 属性 → 解除锁定，或 **更多信息 → 仍要运行**。

**macOS Gatekeeper。** 系统设置 → 隐私与安全性 → **仍要打开**。

**端口占用。** 默认 16888。改 `~/.autoto/config.json` 里的 `server.port`，或到 **设置 → Servers/System**。

**SQLite 锁残留。** 进程被强杀后，删除 `~/.autoto/autoto.db-shm` 和 `~/.autoto/autoto.db-wal` 再启动。

**远程访问。** 不要把 Autoto 直接暴露到公网。用 **设置 → Remote Access**（Cloudflare tunnel、密码、过期）。

Windows 操作说明：[WINDOWS_RUN.md](WINDOWS_RUN.md)。

## 文档

完整目录见 [README.md](README.md)。

## 许可

[MIT](../LICENSE) — Copyright (c) 2026 Autoto contributors。第三方声明：[THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md)。
