# Autoto

[English](README.md) | [繁體中文](README.zh-TW.md) | 简体中文

[![Release](https://img.shields.io/github/v/release/hokidev26/autoto)](https://github.com/hokidev26/autoto/releases)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![Go 1.26+](https://img.shields.io/badge/Go-1.26%2B-00ADD8?logo=go)](go.mod)
[![Platforms](https://img.shields.io/badge/Platforms-macOS%20%7C%20Linux%20%7C%20Windows-lightgrey)](#安装)

> **Autoto** 是一个跑在你自己机器上的 coding agent。
> 你给它一个任务，它在后台做，遇到你会想被问一声的事情，它会先问。
> **任务 → 后台 run → 批准 → run 摘要 → diff → 指定路径 commit**

---

## 为什么用 Autoto

| 痛点 | Autoto 的做法 |
|---|---|
| AI 跑太久，坐电脑前等 | 任务在后台跑，提交后可以去做别的事 |
| AI 乱改一堆文件 | 结束后只给你看 diff，commit 只能送你点名的路径 |
| 出門後看不到進度 | 手机 UI 为小屏幕设计，可裝到主畫面當 PWA |
| 出門後不能批准危險操作 | 手機 UI 或 Telegram 私聊遠端批准（一次性） |
| 想從外面連回自家電腦 | 從設定頁開臨時 Cloudflare tunnel，密碼保護、會過期 |
| 一次想跑幾個任務 | 任務可以排隊，背景 agent 自動接著做 |
| 想試新功能又怕髒掉主分支 | Fork 到獨立的 Git worktree，預檢通過再合併 |
| AI 一直重複同一個指令 | 連續重複會插進漸進式提醒，但不會否決呼叫 |
| 工具輸出太大塞爆上下文 | 超過門檻的輸出自動落盤，模型用 Read/Grep 分頁 |
| 想把模型分享給其他工具 | 把 provider 包成 OpenAI 兼容的 `/v1` 端點，每個 API key 獨立配額 |

**Autoto 不会**做的事：自动 push、amend、reset、force、clean、`git add -A`。任何模式、allow rule、批准都不能执行「递归删除」「直接写入 `/dev/sda`」「把 curl 灌进 shell」这类不可逆操作。`.env*`、credential、私钥、`.git` 内部：Read/Write/Edit 直接拒绝，Glob/Grep 跳过。

---

## 快速开始（命令行版）

从 [GitHub Releases](https://github.com/hokidev26/autoto/releases) 下载 `autoto_<版本>_<OS>_<arch>`：

| OS | 文件 | 架构 |
|---|---|---|
| **Windows** | `autoto_<版本>_windows_amd64.zip` | x64 |
| **Windows** | `autoto_<版本>_windows_arm64.zip` | ARM |
| **macOS** | `autoto_<版本>_darwin_arm64.tar.gz` | Apple Silicon |
| **macOS** | `autoto_<版本>_darwin_amd64.tar.gz` | Intel |
| **Linux** | `autoto_<版本>_linux_amd64.tar.gz` | x64 |
| **Linux** | `autoto_<版本>_linux_arm64.tar.gz` | ARM |

解压后直接执行：

```bash
# macOS / Linux
./autoto

# Windows
autoto.exe
```

打开 http://localhost:16888

> **默认状态路径**
> ```
> Config:   ~/.autoto/config.json
> Database: ~/.autoto/autoto.db
> Projects: ~/projects
> ```

> **第一次跑缺 Provider？** 进到 `Settings → Providers` 配 OpenAI / Anthropic / Gemini / 兼容中转站 之一的 API key，或直接用內建的 `cliproxyapi` 预设组。

### 原生桌面版（可选）

想要原生窗口、不用开浏览器，下载 `autoto-desktop_<版本>_<OS>_<arch>.tar.gz`：

> 桌面版需要该平台原生 WebView 工具链，**无法交叉编译**。GitHub Release 只发布 **macOS（arm64 / amd64）** 与 **Linux amd64**。
> Windows 桌面版要從原始碼自編，参考下面「从源代码构建」。

---

## 从源代码构建

要求：**Go 1.26+**（`go.mod` 声明）。

### CLI 版本（跨平台）

```bash
git clone https://github.com/hokidev26/autoto
cd autoto
go run ./cmd/autoto
```

### 桌面版（需原生 WebView）

```bash
# macOS / Linux
go build -tags desktop -o autoto-desktop ./cmd/autoto-desktop

# Windows（额外避免 console 窗口）
go build -tags desktop -ldflags "-H windowsgui" -o autoto-desktop.exe ./cmd/autoto-desktop
```

### 接近 release 的精简版

加上 `-trimpath -ldflags "-s -w"`（小约 25%；panic 堆栈仍保留函数名但没有文件路径）：

```bash
go build -trimpath -ldflags "-s -w" -o autoto ./cmd/autoto
go build -tags "desktop,production" -trimpath -ldflags "-s -w -H windowsgui" -o autoto-desktop ./cmd/autoto-desktop
```

`production` 标签额外关掉 Wails devtools。完整构建说明见 `docs/BUILD.md` 与 `Makefile`。

---

## 配置

首次启动会自动建立 `~/.autoto/config.json`（schema 内含 `version` 字段；没这字段的旧配置会被当成版本 `1` 加载并在内存中规范化）。

### Agent 模型

```text
AUTOTO_DEFAULT_MODEL        # 默认 agent 模型
AUTOTO_SUMMARY_MODEL        # 摘要用较小模型
AUTOTO_CONTEXT_TOKEN_LIMIT  # 上下文 token 上限
```

### Provider（环境变量）

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

### 自动化集成（环境变量优于硬编码密钥）

Telegram 与 Home Assistant 的连接分两部分存储：非机密的元数据 + 逻辑上的密钥引用。目前只接受 `env:VAR_NAME` 格式；UI 与 API 会直接拒绝明文 token，公开响应只显示「密钥是否已配置」。

```sh
export AUTOTO_TELEGRAM_BOT_TOKEN='replace-with-current-bot-token'
export AUTOTO_HOME_ASSISTANT_TOKEN='replace-with-current-ha-token'
```

然后在配置中引用：

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

Telegram 命令集（限定私聊）：`/pair`, `/status`, `/approve <toolCallId>`（一次性）, `/deny <toolCallId> [reason]`。**没有** `/task` 或自由对话。token 换新会自动撤销所有已配对的会话。

Home Assistant 端点必须是 loopback、`.local`、link-local 或私网 IP。

### CLIProxyAPI 预设组

内建 `cliproxyapi` provider profile 对接本机 [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI)：

```text
Provider: cliproxyapi
Type:     openai-compatible
Base URL: http://127.0.0.1:8317/v1
Model:    gpt-5.5
```

启动 CLIProxyAPI 后，进到 `Settings → Providers → Codex 凭證 + 中轉站` 设置。Codex 采「导入凭證」：贴 Codex auth JSON 或 refresh token / token / account 列表后直接导入到 CLIProxyAPI，Autoto 会在之后刷新 CLIProxyAPI 的 auth 文件与 `/v1/models`。

让新项目默认就用这个 profile：

```sh
AUTOTO_DEFAULT_MODEL=cliproxyapi:gpt-5.5 ./autoto
```

### Agent Server 后端

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

本地后端用 `X-Session-API-Key` 头；云端后端用 `Authorization: Bearer ...`。

---

## 功能

完整逐项清单，比例上说更具体。

### Agent 与模型

- 本机 HTTP 服务器，内嵌 HTML/CSS/JS 界面，前端用免构建的 ES module 架构，并抽出 Settings 本地偏好面板
- SQLite 持久化：项目、workline、agent、消息、工具调用、backend registry、stdio MCP registry
- Provider 抽象层，以最小 `Tools` / `Streaming` / `ImageInput` 能力契约串接：
  - OpenAI 官方 Responses API（SDK 流式 text delta + usage 收集）
  - Anthropic 官方 Messages API（流式 text delta、tool-use delta、usage 收集、大请求自动 5m prompt-cache breakpoint）
  - OpenAI 兼容 Chat Completions
  - Gemini Interactions API（SSE 流式、图片、原生 function call、reasoning effort、内部 thought-signature replay）
  - Kiro（Amazon Q）原生订阅 provider（Event Stream、OAuth token refresh、`ksk_*` API key 认证）
  - CLIProxyAPI 内建本机 OpenAI 兼容预设组
- 核心工具：Read、Write、Edit、Bash、Glob、Grep、WebFetch、WebSearch、MCPListTools、MCPCallTool、AgentSnapshot、AgentSendMessage
- 跨对话协作：`AgentSnapshot` 列出同实例上其他主对话并读近期内容；`AgentSendMessage`（exec 风险、走审批）把消息送进另一个对话，让它以自己的权限跑一轮后把回报自动送回发问的对话（与子代理相同的唤醒机制）。直接的 A↔B 循环会被拒绝，子代理不能发送也不能被指定为目标。从别的对话读到的内容——包括经 `PeerSnapshot` 从配对实例读到的——会被标记为不可信的只读背景资料，以免别人转录里的伪造指令靠「从工具结果送来」就取得权限
- 工具输出落盘：结果超过 `agent.toolOutputSpillBytes`（默认 50000 bytes）时，完整内容写到 Autoto 主目录，回给模型的换成首尾预览加上文件路径，由模型用 `Read` 分页或 `Grep` 搜索。`Read` 与 `Grep` 本身豁免，避免用「再去读一次」回答一次读取；写文件失败一律保留原本的內联结果，绝不把成功的工具调用变成错误；落盘文件 7 天后清理
- 重复工具调用检测：同一工具带同样参数的连续调用按 run 计数，达到 `agent.repeatToolCallThresholds`（默认 `3, 5, 8`）之一时，下次请求中插入渐进式提醒。参数先规范化再比较，被拒绝的调用同样计入；该检测只观察、永不否决调用
- 续跑预算：可按工作区限制续跑次数、总回合、总 token、实际执行时间，默认无上限（负值明确选择不设限）
- Project sidebar 拖拽排序，服务器持久化

### 安全边界

- 敏感路径硬阻拦：`Read`、`Write`、`Edit` 直接拒绝受保护文件，`Glob` 与 `Grep` 跳过不列出。范围含 `.env*`、凭證／密鑰文件、通用私鑰材料、`.git` 内部
- Bash 危险反思：可设强度（off / loose / medium / strict）的 LLM 安全闸，执行前用当前对话模型审查高风险命令，按结构化判定放行或阻拦
- 递归删除、磁盘写入、权限放宽、下载直接灌进 shell 等不可逆操作属硬阻拦层级，任何权限模式、allow rule、批准都不能执行
- Git 工作区限定在 status、diff、log、指定路径 commit，不会自动 push、amend、reset、clean、force、`git add -A`。Git 面板会画当前分支的提交时间线（泳道图与 ref 标签），该视图是只读的。

### 调度与自动化

- 调度 worker：cron / `@every` 表达式 + IANA 时区。调度权限上限 `readOnly` 或 `acceptEdits`，不会中断或取代正在跑的手动 run；无人值守的 run 不沿用交互时的 session 批准；含停止／重启 Autoto 本身的命令会在创建与更新时被拒绝
- 持久化 Webhook / Telegram 通知投递记录：去重、租约、指数退避、尝试次数上限、delivered / dead 状态、汇总指标、显式重试
- Telegram Bot API long polling：私聊 `/pair`、`/status`、`/approve <toolCallId>`（一次性 `allow_once`）、`/deny <toolCallId> [reason]`；未授权命令与失败配对静默；已处理 update 用持久化 event ID 与 cursor 保护
- Home Assistant 集成：限本地／私网端点、只读状态、固定的 action allowlist、短时效 action request、雙重 UI 确认、loopback 批准。门锁解锁／摄像头截图等未知／高风险动作硬阻拦；IM 无法控制设备
- 持久化 SQLite migrations V19–V22 与 API：调度、通知投递、整合连接、channel 配对／事件／cursor、device-action request
- 本地监控汇总：活跃 run、待批准、调度、投递状态、channel、device action、自动化 worker 健康

### 后端与工作区

- Agent Server 后端 registry：sidebar 与 Agent Admin 管理 UI，支援 OpenHands Agent Server 兼容端点
- Workline 与容器设置：创建 Git worktree 的 workline fork、合并前预检、干净 worktree 合并 API
- 交互式 PTY 终端 WebSocket（`/ws/terminal`），含终端管理与浏览器端保留／聚焦偏好
- 文件系统浏览／预览／mkdir API
- 持久化 Server 端 Skills：global / project / workspace CRUD、有效 skill 解析、修订历史／还原、快照稳定的 cursor 分页；MCP registry 操作仍需显式 exec-risk 批准
- Server 端 lifecycle hooks：global / project / agent 三层 run / tool 边界，快照稳定分派、CAS 更新、执行历史、隔离测试执行（不建立普通 Agent run）
- 本机 plugin registry：从本地目录安装 stdio MCP 插件，工具以 `plugin__<slug>__<tool>` 动态发现供 agent 调用。安装后一律停用，启用需明确确认执行本机代码；插件进程以干净环境与 `env:` 密钥引用运行，manifest 支援每插件超时，并有更新与健康检查端点（见 [docs/PLUGINS.md](docs/PLUGINS.md)）

### 界面与体验

- 为小屏幕设计的 UI（不是缩放桌面版）：下拉刷新、通知左滑关闭、输入框不被挤掉；可装到主画面当 PWA，没有浏览器外框
- 设置 modal 实时搜索／过滤 + 键盘焦点快捷键
- 聊天消息复制动作：导出单则消息与整个对话为 Markdown
- 按登录用户与 Agent 区分的版本化私讯草稿；浏览器本地草稿仅作为未登录兼容后备
- Unicode／大小写不敏感的本地账号 handle、`@handle` 建议、不可变的用户消息更正（保留新旧附件）
- 剪贴板图片／文件附件、Unicode 安全的多语系草稿上限、浏览器原生 text undo／redo
- 浏览器本地 prompt history：空输入时 ↑/↓ 召回；旧设置可由 preference 备份迁移
- 聊天输入框 slash command palette：来自已启用的本地 Skills command template
- 浏览器本地 Settings → Profile：显示名、头像字首、工作区标签、Git identity 助手
- 浏览器本地 Settings → Network Search：provider 预设、结果数上限、是否确认、网域规则；`WebSearch` 与 `WebFetch` 工具提供公开网页／文件查询
- 浏览器本地 Settings → Notifications：toast 类别、工作事件提示音（完成／等待核准／失败，含内置音效或本地自定义音文件、音量与同时播放上限）、系统通知与明确的权限请求、显示时长、UI 终端提示；服务器端持久化 Webhook / Telegram 投递历史与重试
- 浏览器本地 Settings → Appearance：主题、密度、默认终端可见性、Agent event 显示
- 设置 → Servers/System + Runtime 面板：runtime 摘要、Go runtime、路径、Agent 限制
- 设置 → Users：管理员可创建只能查看对话的访客、签发访问密钥，并授权项目。访客限制由服务器执行，只能看已授权对话并编辑个人资料
- 设置 → Storage 面板：config、database、home、projects 体积
- 设置 → Usage 面板：projects、messages、tool calls、model requests、估计 token 成本、backends
- 设置 → About 依赖授权面板（开发期的 `/api/licenses` 端点）
- 设置 → About 浏览器本地偏好备份／导入：profile、skills、chat drafts、prompt history、search、IM、notification、appearance、terminal、recent directory、model、relay-protocol
- 设置 → IM 网关 自动化控制：调度、通知历史／重试、Telegram 与 Home Assistant、pairing／revocation、监控、device state、本地 device-action 确认、审计事件

### Agent WebSocket 协议

- `ws/agent` 上跑协议 v2，每处理程序单调递增序号、有限内存 replay、权威 live-snapshot 重同步；**不是** 持久化或跨处理程序事件 log

---

## 平台支援

| 元件 | Windows | macOS | Linux |
|---|---|---|---|
| CLI | ✅ amd64 / arm64 | ✅ arm64 / amd64 | ✅ amd64 / arm64 |
| 桌面原生窗口 | ⚠️ 从源码自编（Release 不发） | ✅ arm64 / amd64 | ✅ amd64 |

桌面版需该平台原生 WebView 工具链，无法交叉编译。

---

## 疑难排解

### Windows：「Windows 已保护你的电脑」

因为 Autoto 没有 Authenticode 签名。执行步骤：
1. 对 `autoto.exe` 按右键 → 属性
2. 勾「解除封锁」→ 应用
3. 或在 SmartScreen 窗口点「更多信息」→「仍要运行」

未来可加代码签名，但需要购买与维护凭证。

### macOS：「无法打开，因为它来自未识别的开发者」

对 `autoto` 在 Finder 第一次执行会被 Gatekeeper 拒绝：
1. 打开 **系统设置 → 隐私与安全性**
2. 捲到最下面，会看到「已阻擋 `autoto` 的使用」
3. 点「仍要打开」

未来可加公證（notarization），但需要 Apple Developer 账号（年费 USD 99）。

### 端口被占用

默认 16888 被占用的话，编辑 `~/.autoto/config.json`：

```json
{
  "server": { "host": "localhost", "port": 17888 }
}
```

或通过 Web UI `Settings → Servers/System` 改。

### SQLite 锁住

如果程序被强制 kill，database 可能留下 stale lock。删掉 `~/.autoto/autoto.db-shm` 与 `~/.autoto/autoto.db-wal` 后重启。

### 出门在外连不到

从外面连回自家网络，**不要**直接把 Autoto 对外公开。打开 `Settings → Remote Access` 开一条临时 Cloudflare tunnel，会拿到网址 + QR code，密码保护、过期自动失效。

---

## 费用估算

Autoto 把 provider 用量记在 `api_requests` 表，并在 `Settings → Usage` 显示汇总成本。估算来自 `internal/pricing/pricing.go` 中的 USD／百万 token 表，最近一次对齐公开定价页面是 2026-07-07（OpenAI API pricing、GPT-4.1 pricing announcement、Anthropic Claude pricing）。未知名稱刻意估算为 `0`；OpenAI 兼容中转／本地模型的费率可能与公开名稱对应的官方费率不同。

---

## 系统要求

- Go 1.26+（`go.mod` 声明）
- SQLite 走纯 Go `modernc.org/sqlite` driver，**不需要** 本机 sqlite3
- Node.js **非必要**，只在验证阶段对内嵌前端脚本跑 `node --check` 与 `node --test`

---

## 命名兼容性

设置路径与 route 别名保留向后兼容闸。Canonical 名稱总是优先。兼容性生命周期与移除闸内部定义：任何兼容表面都要在 v1.0.0 以后、且至少经过两个 tagged release 的迁移窗口才能移除。

---

## 相关文件

- `docs/BUILD.md` — 从源码构建
- `docs/WINDOWS_RUN.md` — 在 Windows 上跑
- `docs/ARCHITECTURE.md` — 架构总览
- `docs/PLUGINS.md` — 本地 MCP 插件
- `docs/DESKTOP_PACKAGING.md` — 桌面打包边界
- `CHANGELOG.md` — 变更记录
- `SECURITY.md` — 漏洞回报
- `CONTRIBUTING.md` — 贡献指南
- `AGENTS.md` — Agent 行为准则
- `THIRD_PARTY_NOTICES.md` — 第三方依赖授权

---

## 授权

[MIT](LICENSE) — Copyright (c) 2026 Autoto contributors
