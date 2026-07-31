# Autoto Go MVP 项目规划

## 1. 项目目标

本项目目标是用 Go 实现 Autoto：一个本地 AI 编程 Agent 后端。Go module 为 `autoto`，`cmd/autoto` 与 `autoto` 二进制是当前规范入口；`cmd/autoto` 仅保留为 legacy 兼容 shim。

核心目标不是一次性堆满所有功能，而是先做出一个可运行、可扩展、可逐步替换/增强的 MVP：

- 本地 HTTP 服务
- SQLite 持久化
- Project / Workline / Agent 数据模型
- Agent 会话与消息记录
- Provider 抽象
- Tool 抽象与基础工具执行
- WebSocket 事件推送
- 基础文件系统 API
- 基础开源协议依赖清单
- 简单内嵌 Web UI
- 公开仓库入口文档、MIT License、CI 与安全说明

### 1.1 Legacy compatibility lifecycle（唯一事实源）

本节是 legacy 兼容面的**唯一生命周期事实源**；README、CHANGELOG、SECURITY 与历史计划只能引用，不得另设不同移除日期或窗口。

**规范名称（canonical names）**：

- 产品、CLI、module、release asset：Autoto / `autoto`
- 本地状态与配置：`~/.autoto`、`AUTOTO_*`
- HTTP/WebSocket header 与浏览器偏好：`X-Autoto-*`、`autoto.*`
- 领域与路由：Agent / Workline、`/api/agents`、`/api/worklines`、`/ws/agent`

**當前兼容面**：

- 舊 CLI shim（保留為兼容入口點）；
- 舊配置目錄的一次性配置遷移讀取；
- 舊環境變量 fallback；
- 舊 HTTP header、cookie 與 localStorage key；
- 舊 JS global（服務端仍注入同值作為 fallback）；
- 舊 API 路由別名；
- migration、測試夾具與 CHANGELOG 歷史中的舊名。
### 3.1 Go 项目骨架

目录：

```txt
autoto/
  go.mod                         # module autoto
  go.sum
  .gitignore
  cmd/autoto/main.go              # canonical application entrypoint
  cmd/autoto/main.go          # legacy compatibility shim
  internal/config
  internal/db
  internal/server
  internal/agent
  internal/providers
  internal/tools
```

启动方式：

```bash
go run ./cmd/autoto
```

构建后的规范 CLI 名称为 `autoto`，例如 `go build -o autoto ./cmd/autoto && ./autoto`。

默认监听：

```txt
http://localhost:16888
```

默认配置路径：

```txt
~/.autoto/config.json
```

默认数据库路径：

```txt
~/.autoto/autoto.db
```

当规范配置文件不存在而旧 `~/.autoto/config.json` 存在时，启动会自动将该 legacy 配置复制到 `~/.autoto/config.json` 后继续加载；旧目录仅用于迁移兼容。

默认项目目录：

```txt
~/projects
```

---

### 3.2 配置模块

文件：

```txt
internal/config/defaults.go
```

当前默认配置包含：

- config schema version（当前 `version = 1`，老配置缺字段时加载回填）
- server host / port
- home dir
- database path
- default project dir
- agent 默认模型
- agent 默认权限模式
- 多 provider 实例配置（OpenAI 官方 / Anthropic 官方 / OpenAI-compatible / CLIProxyAPI 本地预置）

当前默认：

```txt
server.host = localhost
server.port = 16888
agent.defaultPermissionMode = acceptEdits
agent.defaultModel = openai:gpt-4.1-mini
```

Agent 与核心运行时支持的规范环境变量：

```txt
AUTOTO_DEFAULT_MODEL
AUTOTO_SUMMARY_MODEL
AUTOTO_CONTEXT_TOKEN_LIMIT
AUTOTO_EXPOSED
AUTOTO_ACCESS_PASSWORD
AUTOTO_REMOTE_TERMINAL
```

同名 legacy `AUTOTO_*` 环境变量仍作为回退兼容；当两者同时存在时，`AUTOTO_*` 优先。

Provider 支持环境变量：

```txt
OPENAI_API_KEY
OPENAI_MODEL
ANTHROPIC_API_KEY
ANTHROPIC_MODEL
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

首次生成默认 `config.json` 时，运行时仍会读取环境变量中的 API key，但写入磁盘的默认配置会清空 provider/backend API key，避免把 shell 环境里的 secret 持久化。

P2–P3 integration connection 的 bot/access token 不接受明文，只保存 `env:VARIABLE_NAME` 引用，例如：

```txt
Telegram botToken     -> env:AUTOTO_TELEGRAM_BOT_TOKEN
Home Assistant token -> env:AUTOTO_HOME_ASSISTANT_TOKEN
```

公开 API 只返回对应 logical secret 是否已配置，不返回引用目标或解析后的值。Telegram bot token 轮换会改变 credential revision 并撤销旧配对；怀疑泄漏时还应从本地 UI/API 显式撤销配对并重新配对。Home Assistant 不使用 channel pairing，token 轮换后应重启/重测或禁用连接。

---

### 3.3 SQLite 数据库

文件：

```txt
internal/db/schema.go
internal/db/db.go
```

当前核心表（节选）：

```txt
users
projects
worklines
agents
runs
agent_messages
agent_message_attachments
agent_tool_calls
api_requests
agent_backends
schedules
notification_deliveries
integration_connections
channel_pairings
channel_events
channel_cursors
device_action_requests
```

这些表的命名与字段风格尽量贴近 AI 编程工作台数据模型，方便后续迁移或扩展。

核心关系：

```txt
projects
  -> worklines
      -> agents
          -> agent_messages
          -> agent_tool_calls
```

---

### 3.4 HTTP API

当前已实现：

```txt
GET  /api/health
GET  /api/auth/status
GET  /api/settings
GET  /api/models
GET  /api/licenses
GET  /api/runtime/summary
GET  /api/storage/summary
GET  /api/usage/summary
GET  /api/monitoring/snapshot

GET  /api/notifications/settings
PUT  /api/notifications/settings
POST /api/notifications/test
GET  /api/notifications/deliveries
POST /api/notifications/deliveries/{id}/retry

GET    /api/schedules
POST   /api/schedules
PATCH  /api/schedules/{id}
DELETE /api/schedules/{id}
POST   /api/schedules/{id}/run

GET    /api/integrations/connections
POST   /api/integrations/connections
PATCH  /api/integrations/connections/{id}
DELETE /api/integrations/connections/{id}
POST   /api/integrations/connections/{id}/test

POST /api/channels/pairing-codes
GET  /api/channels/pairings
POST /api/channels/pairings/{id}/revoke
GET  /api/audit/events

GET  /api/devices?connectionId=...
POST /api/device-actions
POST /api/device-actions/{id}/approve
POST /api/device-actions/{id}/deny

PUT  /api/providers/{name}/config

GET  /api/providers/cliproxyapi/auth-files
POST /api/providers/cliproxyapi/auth-files/import

GET    /api/backends
POST   /api/backends
GET    /api/backends/{id}
PATCH  /api/backends/{id}
DELETE /api/backends/{id}
POST   /api/backends/{id}/activate
GET    /api/backends/{id}/health

GET  /api/projects
POST /api/projects
GET  /api/projects/{id}
GET  /api/projects/{id}/worklines

GET  /api/worklines/{id}
POST /api/worklines/{id}/fork
GET  /api/worklines/{id}/merge-check?targetWorklineId=...
POST /api/worklines/{id}/merge
GET  /api/worklines/{id}/agents

GET   /api/agents/{id}
PATCH /api/agents/{id}/cwd
PATCH /api/agents/{id}/model
PATCH /api/agents/{id}/permission-mode
POST  /api/agents/{id}/interrupt
GET   /api/agents/{id}/messages
POST  /api/agents/{id}/messages
GET   /api/agents/{id}/tools
POST  /api/agents/{id}/tool-calls
GET   /api/agents/{id}/tool-calls/{toolUseId}
GET   /api/agents/{id}/git/status
GET   /api/agents/{id}/git/diff
GET   /api/agents/{id}/git/log
POST  /api/agents/{id}/git/commit

GET  /api/fs/browse?path=...
GET  /api/fs/directories?path=...
GET  /api/fs/preview?path=...
POST /api/fs/mkdir

GET  /ws/agent?id={agentId}
GET  /ws/terminal?agentId={agentId}
```

规范领域实体与路由为 Agent / Workline、`/api/agents`、`/api/worklines` 和 `/ws/agent`。Legacy 客户端仍可使用 `/api/projects/{id}/worklines`、`/api/worklines/...`、`/api/agents/...` 与 `/ws/agent`；这些兼容别名复用同一组 Agent/Workline handler。

---

### 3.5 Project 创建行为

`POST /api/projects` 请求示例：

```json
{
  "name": "Demo Project",
  "description": "optional",
  "gitPath": "optional",
  "model": "optional provider:model override"
}
```

如果未传 `gitPath`，系统会自动创建：

```txt
~/projects/<project-name-slug>
```

例如：

```txt
~/projects/demo-project
```

并自动创建：

- project
- root workline
- primary agent

---

### 3.6 Agent loop

文件：

```txt
internal/agent/loop.go
internal/agent/hub.go
```

当前能力：

- 接收用户消息
- 写入 `agent_messages`
- 启动 goroutine 执行 agent loop
- 调用默认 provider
- 写入 assistant message
- 更新 agent status
- 经 WebSocket 推送事件

当前 WebSocket 事件包括：

```txt
connected
agent.started
agent.text
agent.done
agent.error
message.created
tool.started
tool.finished
```

Agent stream 已使用 protocol 2 envelope（`protocol`、`streamSession`、`sequence`）：同一进程内由有界 ring buffer 提供有限 replay，并在 cursor 过期、replay 超限、订阅者溢出、stream 淘汰、session 不匹配或前端检测到序列缺口时要求读取 authoritative live snapshot 后 resync。该机制不持久化事件，服务重启或跨进程后不能 replay，不能称为 durable event log。

---

### 3.7 Provider 抽象

文件：

```txt
internal/providers/provider.go
internal/providers/openai_compatible.go
internal/providers/openai_official.go
internal/providers/anthropic_provider.go
```

当前实现：

```txt
openai              -> OpenAI 官方 Go SDK，Responses API
anthropic           -> Anthropic 官方 Go SDK，Messages API
openai-compatible   -> 手写 OpenAI-compatible Chat Completions 兼容层
cliproxyapi         -> 基于 OpenAI-compatible 的本地 CLIProxyAPI 预置
```

模型字符串使用 `provider:model` 前缀路由，例如：

```txt
openai:gpt-4.1-mini
anthropic:claude-sonnet-4-5
openai-compatible:gpt-4.1-mini
cliproxyapi:gpt-5.5
```

如果没有设置对应 API key，provider 会返回配置提示，不会真正请求外部模型；CLIProxyAPI 本地预置例外，它默认允许无客户端 API key 连接 `http://127.0.0.1:8317/v1`，如 CLIProxyAPI 启用了 `api-keys` 再通过 `CLIPROXYAPI_API_KEY` 注入。内建 OpenAI official、Anthropic official 与 OpenAI-compatible Provider 均已接入流式输出、tool calling 与 tool result 回灌，并通过统一最小能力契约声明 `Tools`、`Streaming`、`ImageInput`。未知或未实现 capability 接口的 Provider 按不支持可选能力处理，Agent loop 按能力降级，不在业务层按 Provider 名称特判。

环境变量：

```txt
OPENAI_API_KEY
OPENAI_MODEL
ANTHROPIC_API_KEY
ANTHROPIC_MODEL
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

后续计划支持：

- Codex 凭证导入体验继续完善（账号状态、额度、错误恢复）
- Kiro-like provider
- 本地模型 provider
- 多 provider fallback / load balancing

---

### 3.8 Tool 抽象与核心工具

文件：

```txt
internal/tools
```

当前工具：

```txt
Read
Write
Edit
Bash
Glob
Grep
WebFetch
WebSearch
MCPListTools
MCPCallTool
```

工具接口：

```go
type Tool interface {
    Name() string
    Description() string
    Schema() any
    Risk(input json.RawMessage) Risk
    Execute(ctx context.Context, call Call, env Env) (Result, error)
}
```

当前风险类型：

```txt
read
write
exec
danger
```

当前权限模式：

```txt
readOnly
acceptEdits
default
dontAsk
bypassPermissions
```

初版策略：

- `readOnly`：只允许 read 风险工具
- `acceptEdits/default/dontAsk`：允许 read/write，默认不允许 Bash exec
- `bypassPermissions`：允许大多数工具，但仍阻止 danger
- danger：当前总是拒绝

危险命令初步识别：

```txt
rm
rmdir
shred
```

P2–P3 进一步把敏感路径阻断下沉到文件路径工具：`Read`、`Write`、`Edit` 直接拒绝，`Glob`、`Grep` 遍历时过滤 `.env*`、credentials/secret、常见私钥文件及 `.git`；同时继续拒绝 symlink 逃逸。此边界不覆盖 Bash/stdio MCP，二者仍是强本地执行能力，不能把敏感路径过滤描述成完整 sandbox。

---

### 3.9 文件系统 API

文件：

```txt
internal/server/fs.go
```

当前 API：

```txt
GET  /api/fs/browse?path=...
GET  /api/fs/directories?path=...
GET  /api/fs/preview?path=...
POST /api/fs/mkdir
```

安全边界：

- 默认限制在 `paths.defaultProjectDir`
- 相对路径基于 default project dir
- 阻止 `..` 逃逸

后续计划：

- 支持 agent cwd 边界
- 支持项目维度 path scope
- 支持二进制文件识别
- 支持图片/Notebook/PDF 预览

---

### 3.10 Agent Server 后端注册表

文件：

```txt
internal/server/backends.go
internal/db/db.go
internal/db/schema.go
```

当前能力：

- 持久化多个兼容 OpenHands Agent Server 的后端
- 保证同一时间只有一个 active 后端
- 支持本地后端 `X-Session-API-Key` 与云端后端 `Authorization: Bearer ...`
- 健康检查 `/alive`、`/health`、`/ready`、`/server_info`
- UI 中可以添加、检测、切换、删除后端
- 可通过环境变量 seed 初始后端：
  - `AUTOTO_AGENT_BACKEND_URL`
  - `AUTOTO_AGENT_BACKEND_NAME`
  - `AUTOTO_AGENT_BACKEND_KIND`
  - `AUTOTO_AGENT_BACKEND_API_KEY`
  - `OPENHANDS_AGENT_SERVER_URL`
  - `AGENT_SERVER_URL`
  - `OPENHANDS_SESSION_API_KEY`
  - `AGENT_SERVER_API_KEY`
- `AUTOTO_AGENT_BACKEND_*` 优先于同名 legacy `AUTOTO_AGENT_BACKEND_*`；后者仅保留为回退兼容。

注意：API 返回时只暴露 `apiKeyConfigured`，不会回显后端 API key。

---

### 3.11 内嵌 Web UI

文件：

```txt
internal/server/ui.go
internal/server/static/index.html
internal/server/static/styles.css
internal/server/static/app.js                  # 轻量 bootstrap
internal/server/static/modules/app-main.mjs    # 当前主 UI 模块
internal/server/static/modules/backend-registry.mjs # Agent Server backend registry/modal/Admin controller
internal/server/static/modules/chat-composer.mjs # chat send/draft/history/attachments/slash command controller
internal/server/static/modules/chat-rendering.mjs # chat message rendering/approval/markdown controller
internal/server/static/modules/directory-browser.mjs # directory chooser/browser/recent paths controller
internal/server/static/modules/formatters.mjs  # shared number/size/money/time formatters
internal/server/static/modules/git-workflow.mjs # Git status/diff/log/commit modal controller
internal/server/static/modules/terminal.mjs    # terminal preferences/settings/WebSocket controller
internal/server/static/modules/runtime.mjs     # API/token/WebSocket helper
internal/server/static/modules/mcp-registry.mjs # MCP registry form parsing helpers
internal/server/static/modules/mcp-registry-ui.mjs # backend MCP registry UI/actions controller
internal/server/static/modules/model-provider-settings.mjs # Settings Models/Providers UI and model helpers
internal/server/static/modules/local-preferences-settings.mjs # Settings local preference panels UI/actions controller
internal/server/static/modules/system-settings.mjs # Settings system/storage/usage/users/about panels controller
internal/server/static/modules/skills-workbench.mjs # Settings Skills workbench UI/actions controller
internal/server/static/modules/ui-shell.mjs     # global shortcuts/sidebar/mobile shell/project search
internal/server/static/modules/settings-preferences.mjs # browser-local settings preferences/backup/import
internal/server/static/modules/dom.mjs          # DOM/query/escape/button helpers
internal/server/static/modules/settings-data.mjs # settings/skills static navigation data
internal/server/static/modules/preferences-data.mjs # localStorage keys/default preference data
```

当前 UI 是 **shadcn-inspired**，参考 shadcn/ui 的简洁 card、button、input、badge、border、radius 风格，但没有直接引入 React、Tailwind、Radix 或 shadcn 组件源码。前端已开始无构建 ES module 拆分：`app.js` 只负责 bootstrap，业务主模块在 `modules/app-main.mjs`，Agent Server backend registry/弹窗/Agent Admin controller 在 `modules/backend-registry.mjs`，Chat 发送/草稿/历史/附件/slash command controller 在 `modules/chat-composer.mjs`，Chat 消息渲染/审批/Markdown controller 在 `modules/chat-rendering.mjs`，目录选择/浏览/最近目录/路径格式化 controller 在 `modules/directory-browser.mjs`，通用格式化函数在 `modules/formatters.mjs`，Git status/diff/log/commit modal controller 在 `modules/git-workflow.mjs`，终端偏好/设置页/WebSocket controller 在 `modules/terminal.mjs`，API/token/WebSocket helper 在 `modules/runtime.mjs`，后端 MCP registry UI/action controller 在 `modules/mcp-registry-ui.mjs`，Settings Models/Providers UI 与模型选择 helper 在 `modules/model-provider-settings.mjs`，Settings 本地偏好面板（Profile/Network Search/IM Gateway/Notifications/Appearance）UI/action controller 在 `modules/local-preferences-settings.mjs`，Settings 系统/存储/使用/用户/About 面板 controller 在 `modules/system-settings.mjs`，Settings Skills 工作台 UI/action controller 在 `modules/skills-workbench.mjs`，全局快捷键/侧栏/移动端 shell/项目搜索 controller 在 `modules/ui-shell.mjs`，浏览器本地 Settings 偏好/备份/导入 controller 在 `modules/settings-preferences.mjs`。

当前路由：

```txt
GET /
GET /ui/styles.css
GET /ui/app.js
```

当前页面能力：

- 查看健康状态
- 查看项目列表
- 创建项目
- 自动选择 root workline / primary agent
- 查看 agent messages
- 复制任意用户/助手消息原文，或一键复制当前对话 Markdown，便于整理 issue、PR 描述或外部笔记
- 发送消息
- 按当前 agent 浏览器本地自动保存/恢复聊天输入草稿，切换项目或刷新页面不丢失未发送内容
- 在聊天输入框中通过浏览器本地提示词历史保存最近提示，并在空输入时用 ↑/↓ 快速召回
- 在聊天输入框输入 `/` 调出已启用的本地技能命令模板，并通过键盘或点击插入提示词
- 连接 `/ws/agent`
- 查看 WebSocket event log
- 连接 `/ws/terminal` 交互式 PTY
- 通过设置 → 终端管理查看 PTY 状态、重连/清空/复制/聚焦终端，并管理输出保留和连接后聚焦偏好
- 更新 agent cwd / model / permission mode
- 浏览 `/api/fs/browse`
- 预览 `/api/fs/preview`
- 在设置弹窗内搜索/过滤个人设置、实例管理和各产品化设置面板，并支持快捷键聚焦搜索
- 查看 settings 简要统计，并在设置 → 关于中通过 `/api/licenses` 查看第三方依赖许可证清单
- 在设置 → 关于中复制、下载、导入浏览器本地偏好备份，迁移个人资料、技能草案、聊天草稿、提示词历史、搜索/IM/通知/外观/终端/模型和中转协议设置
- 查看 `/api/runtime/summary` 驱动的服务器与系统、运行资源、Go runtime、内存和 Agent 限制概览
- 查看 `/api/storage/summary` 驱动的储存空间、数据库、配置文件和默认项目目录容量统计
- 查看 `/api/usage/summary` 驱动的使用历史、消息/工具/模型请求和成本统计；未实现真实后台任务前不创建/展示 background_tasks 僵尸模型
- 查看 `/api/auth/status` 驱动的用户初始化和注册开放状态
- 从 `/api/models` 动态刷新 CLIProxyAPI 凭证账号可用模型
- 在 Git 变更面板中查看 status/diff/log，并显式选择文件创建本地 commit（不自动 push）

- 设置 → 个人资料页内完成浏览器本地显示名、头像缩写、身份标签、工作台标签和 Git 身份辅助
- 设置 → 网络搜索页内完成浏览器本地搜索提供商、结果数、安全/确认开关、GitHub 优先和域名规则策略；Agent 工具层已提供 `WebSearch` 公网搜索结果工具和 `WebFetch` 公网 HTTP(S) 文档抓取工具
- 设置 → P2–P3 管理控制台已接入服务端 schedules、durable deliveries、integration connections、Telegram pairing/revoke、Home Assistant 只读实体/本地动作审批、monitoring snapshot 与 audit events。旧 `localStorage` IM 草稿只作为“已停用”的迁移提示，不会启动服务或计入运行状态
- Telegram 当前只通过 long polling 接收私聊 `/pair`、`/status`、`/approve`（固定一次性 `allow_once`）与 `/deny`；无 `/task`、无自由聊天、无 Telegram webhook、无 Slack/Discord。未配对与错误配对保持静默
- Home Assistant 只允许本机/私网 endpoint；状态列表只读，动作仅限固定 allowlist，创建和批准均要求本地 UI 双确认，最终执行批准还要求 direct loopback。critical/未知动作硬阻断，IM 不得控制设备
- 设置 → 技能页已接入服务端 Skills：后端支持 global/project/workspace CRUD、effective Skills、revision 历史/restore 与 snapshot-stable cursor 分页；scoped 面板支持按作用域浏览、详情、分页、修订历史与恢复，但创建、SKILL.md 导入、启停、编辑、删除 UI 仍只操作 global scope。MCP registry 仍可创建/启停/删除 server、运行 tools/list，并通过 exec-risk 审批调用 stdio MCP tools
- 设置 → 生命周期钩子页已接入 global/project/agent 配置、CAS 更新、执行历史、测试/取消/重试与独立中英繁中目录。Shell/HTTP 动作复用现有工具审批和审计网关；`env:` 引用只在审批通过后的执行阶段解析，Shell cwd 保持在工作区内，HTTP 复用防 SSRF 网络策略，LLM gate 保持隔离且不开放工具
- 设置 → 工作线与容器页内完成当前项目工作线、当前工作线 Agent、worktree/branch/容器隔离边界概览和快速切换
- 设置 → AI 代理页内完成默认 Agent 策略概览、当前 agent 状态、模型/权限/workdir 快速调整和 ID 复制
- 设置 → 用户管理页内完成本地 auth status 只读视图、注册状态、安全边界和后续多用户路线提示
- 设置 → 通知页内完成浏览器本地 toast 类型、显示时长和 UI 终端提示偏好；服务端 Webhook/Telegram 通知改为持久 delivery history，具去重、lease、指数退避、最大尝试次数、delivered/dead 状态和显式 retry
- 设置 → 外观与界面页内完成浏览器本地主题、布局密度、终端默认展开和 Agent 事件日志显示偏好
- 设置 → 关于页内完成浏览器本地偏好备份、下载、复制和导入恢复，便于跨浏览器或跨机器迁移工作台设置
- 设置 → 模型/提供商页内完成模型刷新、Codex Token/JSON 凭证导入、账号列表刷新、中转站 API Key/Base URL/协议/默认模型保存、模型选择和首选模型保存
- 设置 → 代理管理页内完成 Agent Server 后端列表、健康检测、启用切换、双击确认删除和新增后端

后续如果需要正式使用 shadcn/ui，可升级为：

```txt
web/
  package.json
  vite.config.ts
  src/
  components/ui/*
```

并使用 React + Tailwind + shadcn registry。正式引入前需要重新整理 Node 依赖协议。

---

### 3.12 License API

文件：

```txt
internal/server/licenses.go
```

当前 API：

```txt
GET /api/licenses
```

当前用途：

- 读取 Go build info 中的依赖
- 对已确认模块标注 license
- 未确认模块标为 `unknown`

当前已确认直接依赖：

```txt
github.com/go-chi/chi/v5               MIT
github.com/google/uuid                 BSD-3-Clause
modernc.org/sqlite                     BSD-3-Clause
github.com/coder/websocket             ISC
github.com/openai/openai-go/v3         Apache-2.0
github.com/anthropics/anthropic-sdk-go MIT
github.com/creack/pty                  MIT
```

注意：

此接口只是开发期合规辅助，不是法律意见。发布前仍需生成完整 third-party notice。

---

### 3.13 公开仓库基础建设

当前已补齐：

```txt
README.md
LICENSE
SECURITY.md
CONTRIBUTING.md
THIRD_PARTY_NOTICES.md
CHANGELOG.md
docs/ARCHITECTURE.md
.github/workflows/ci.yml
.github/workflows/release.yml
.goreleaser.yaml
```

说明：

- 仓库入口以 `README.md` 为准。
- `PROJECT_PLAN.md` 用于开发规划和实现状态跟踪。
- `CHANGELOG.md` 记录 tag 级用户可见变更、安全边界和已知缺口。
- `docs/ARCHITECTURE.md` 面向贡献者说明请求如何流过 server、agent、provider、tools、WebSocket 和 SQLite。
- `THIRD_PARTY_NOTICES.md` 是直接依赖初版说明，不是法律意见；正式发布前仍应生成完整 transitive notice。
- CI 会检查 Go 格式、测试、vet、构建、内嵌 JavaScript 语法，并通过 `golangci-lint` 增加 static analysis。
- `v*` tag 会触发 GoReleaser release workflow，构建 macOS/Linux/Windows archives；README 保留轻量 `docs/demo.svg` 工作流预览，后续如有真实录屏可再替换。

---

## 4. 当前测试

已有测试：

```txt
internal/agent/loop_test.go
internal/config/defaults_test.go
internal/db/db_test.go
internal/providers/anthropic_provider_test.go
internal/providers/openai_compatible_test.go
internal/providers/openai_official_test.go
internal/server/backends_test.go
internal/server/workline_workflow_test.go
internal/server/e2e_test.go
internal/server/git_test.go
internal/server/interrupt_test.go
internal/server/mcp_servers_test.go
internal/server/security_test.go
internal/tools/tools_test.go
internal/runtime/supervisor_test.go
internal/app/run_test.go
internal/automation/manager_test.go
internal/channels/telegram_test.go
internal/devices/action_test.go
internal/devices/client_test.go
internal/schedules/expression_test.go
internal/db/automation_p2p3_test.go
internal/server/automation_api_test.go
internal/server/static/modules/automation-control.test.mjs
```

覆盖：

- 默认配置与后端环境变量 seed
- 创建 project/workline/agent
- agent backend registry 单 active 约束
- OpenHands Agent Server 健康检查
- 工具路径越界检查
- Write 后 Read
- WebFetch HTML 简化与 local/private host 拒绝
- WebSearch query 校验、DuckDuckGo HTML 结果解析、格式化输出和 core 注册
- MCP stdio client 初始化、tools/list、tools/call、文本结果格式化、registered serverId 查找和 core 注册
- MCP server registry：SQLite CRUD、HTTP CRUD、Settings UI 创建/启停/删除/发现工具、env value 响应脱敏、`GET /api/mcp/servers/{id}/tools` discovery
- 本地 token、Origin、Sec-Fetch-Site 与 WebSocket 握手防护
- 官方 Anthropic/OpenAI SDK provider 流式事件、usage 与 fallback 行为
- usage cost 估算：OpenAI、Anthropic Sonnet/Opus 与未知模型分支
- Git commit API 的显式 paths 提交、安全路径拒绝、空仓库 diff 降级
- 全链路 E2E：真实 httptest server、WebSocket agent stream、HTTP message submit、假 provider tool call、审批 route、Bash 工具执行、tool result 回灌模型、消息/tool_call/api_requests 落库
- Workline workflow：fork API 创建 Git worktree/child workline/agent，fork agent Git API 边界可用，merge-check 能报告冲突文件，merge API 能成功合并 clean 分支并在冲突时 abort
- V19–V22 migration 与 schedules/deliveries/integration/channel/device action 持久状态、CAS/lease、统计和敏感 payload 拒绝
- Schedule cron/`@every`/timezone、busy skip、不替换人工 run，以及 run permission cap 不放宽 Agent 权限
- Webhook/Telegram delivery retry/backoff、`dead`、历史与 Agent-scoped Telegram 路由/脱敏
- Telegram 私聊配对、失败锁定与静默、event/cursor 幂等、`/status`、一次性 `/approve`、`/deny`、danger 拒绝、审计 fail-closed 与限流
- Home Assistant 私网 endpoint、只读属性过滤、固定动作 catalog、canonical seal、本地 direct-loopback 二次批准，以及 unlock/camera/script 等 critical/未知动作硬阻断
- monitoring snapshot 聚合、runtime Supervisor 启停/回滚顺序、Settings P2–P3 控制台的有界 DOM 与 secret 不回显
- 文件路径工具对 `.env*`、credentials/secrets、私钥与 `.git` 的硬阻断/过滤

当前验证命令已收敛为统一入口：

```bash
make check
```

如果本地没有 `make`，可直接运行 `./scripts/check.sh`。该脚本会检查 Go 格式但不自动改写，随后运行 Go tests/vet/build、前端 `node --check` 与前端 `node --test`。如需格式化 Go 代码，运行 `make fmt`。

短启动验证包括：

- `/api/health`
- `/api/licenses`
- `/api/backends`
- `/api/backends/{id}/health`
- `/api/mcp/servers`
- `/api/mcp/servers/{id}/tools`
- `POST /api/projects`
- `POST /api/agents/{id}/tool-calls`
- `GET /api/agents/{id}/git/status`
- `GET /api/agents/{id}/git/diff`
- `POST /api/agents/{id}/git/commit`

历史 dogfood 证据（Autoto 更名前，以下服务名称、补丁文本和提交信息保留为 legacy 原始记录）：2026-07-07 UTC / 2026-07-08 +08:00 使用临时 Autoto 服务与临时 Git 仓库，通过 API 创建项目，执行 `Write` / `Read` / `Grep`，让已跟踪文件 `demo/notes.md` 变为 `worktree=M`，通过 Git diff API 看到 `added=2 deleted=0` 和补丁行 `+- Updated through Autoto Write tool for tracked diff review.`，再用显式 `paths: ["demo/notes.md"]` 调用 Git commit API 创建提交 `96cd79e Dogfood tracked diff workflow`，提交后仓库 `clean=true`。较早的未跟踪文件 smoke 也创建并提交了 `2484ab7 Dogfood Autoto API workflow`。

---

## 5. 工程工作流状态（历史 Phase 1–6）

本节的 Phase 1–6 是早期**工程工作流编号**，只用于追踪实现主题；它们不是 `docs/notes/needtodo0712.md` 的产品 Phase A/B/C。产品 **Phase B 专指 IM 控制面**；当前只完成受限 Telegram 配对/状态/一次性审批/拒绝，不包含 `/task`、自由聊天或其他渠道。不得把本节的 Provider、Tools、Skills 或前端工作称为产品 Phase B。

### Engineering Phase 1：当前 MVP 完善

目标：让后端更适合手工/CLI 调试。

待做：

- [x] `GET /api/projects/{id}/worklines`
- [x] `GET /api/worklines/{id}/agents`
- [x] `PATCH /api/agents/{id}/cwd`
- [x] `PATCH /api/agents/{id}/model`
- [x] `PATCH /api/agents/{id}/permission-mode`
- [x] `POST /api/agents/{id}/interrupt`
- [x] 工具调用 WebSocket 事件
- [x] provider request/response 记录到 `api_requests`
- [x] 最简 context 管理（粗略 token 估算、旧消息摘要、旧工具输出降级）
- [x] agent status 更细化：`idle/running/error/interrupted`

---

### Engineering Phase 2：工具系统增强

目标：让工具更接近可用编码 Agent。

待做：

- [x] Edit 工具
- [x] Bash 支持显式审批状态
- [x] Bash 输出流式事件
- [ ] 工具执行超时配置
- [ ] 工具输出截断策略配置
- [ ] 工具输入 JSON schema 输出
- [ ] 工具权限规则表
- [ ] whitelist/blacklist dirs
- [ ] whitelist/blacklist commands（已内置 exec 白名单 matcher 与 danger 阻断，规则配置 UI/表待补）

---

### Engineering Phase 3：Provider 增强

目标：支持真实模型流式与 tool calling。

待做：

- [x] OpenAI-compatible streaming
- [x] OpenAI 官方 Responses API streaming
- [x] Anthropic 官方 Messages API streaming
- [x] tool call parsing（Anthropic / OpenAI official / OpenAI-compatible）
- [x] tool result 回灌模型（Anthropic / OpenAI official / OpenAI-compatible）
- [x] Anthropic 官方 SDK provider（非流式 MVP）
- [x] OpenAI 官方 Responses API provider（非流式 MVP）
- [x] provider 前缀路由与基础 model list
- [x] usage/cost 统计（usage 写入 `api_requests`，cost 使用内置 per-model USD/MTok 价格表估算；价格来源在 `internal/agent/loop.go` 注释和 README 中记录，未知模型估算为 0）
- [x] Anthropic prompt caching（足够大的 system/tool/message 请求自动添加 5m cache_control breakpoint，小请求跳过以避免额外 cache write 成本）
- [x] retry/backoff
- [x] first token timeout

---

### Engineering Phase 4：Git / Workline 工作流

目标：实现多分支、多工作线能力。

待做：

- [x] Git status/diff/log API（只读）
- [x] UI diff 查看器（只读 Git 变更面板）
- [x] Git commit API
- [x] project git path 检查（repo root 必须位于项目路径或 default project dir 内）
- [x] workline fork（后端 API 创建 child workline + primary agent）
- [x] git worktree 创建（`POST /api/worklines/{id}/fork` 使用 sibling `.autoto-worktrees`，避免嵌套进主 repo）
- [x] workline merge-check（`GET /api/worklines/{id}/merge-check` 使用临时 worktree 做非破坏性冲突预检）
- [x] merge（`POST /api/worklines/{id}/merge` 要求 source/target clean，冲突时 abort 并返回 409，成功后记录 merge metadata）
- [ ] AI resolve conflict
- [ ] review workline

---

### Engineering Phase 5：MCP / Terminal / Runtime

目标：补齐高级能力。

待做：

- [x] WebFetch 公网 HTTP(S) 文档抓取工具（local/private host 默认拒绝）
- [x] WebSearch 公网搜索结果工具（默认 DuckDuckGo HTML，query/limit 校验，local/private search endpoint 防护）
- [x] MCP server registry（后端持久注册表/API + Settings UI 创建/启停/删除/发现工具：CRUD、env value 脱敏响应、registered server tools/list discovery）
- [x] MCP tool discovery（`MCPListTools` 通过 stdio initialize + tools/list，并支持 `serverId` 引用已注册 server）
- [x] MCP tool execution（`MCPCallTool` 通过 stdio initialize + tools/call，支持 `serverId`，exec-risk 审批）
- [x] PTY terminal
- [x] `/ws/terminal`
- [x] V19 schedules + run source/permission cap（仅 `readOnly` / `acceptEdits`，busy skip，不取消人工 run）
- [x] V20 durable Webhook/Telegram deliveries（历史、去重、lease、指数退避、`dead`、retry）
- [x] V21 Telegram pairing/events/cursor（long polling，`/pair` `/status` `/approve`-once `/deny`，未配对静默）
- [x] V22 Home Assistant device action requests（本机/私网、只读状态、固定 allowlist、本地双确认、critical hard block、IM 禁止）
- [x] V51 profile configuration + lifecycle hooks（global/project/agent 配置、快照绑定、CAS、历史、测试；Shell/HTTP 复用审批审计，密钥延迟解析，HTTP 防 SSRF）
- [x] monitoring snapshot 聚合与 runtime Supervisor 管理 channels / automation / HTTP
- [ ] Slack/Discord channel adapter
- [ ] IM `/task` 与自由聊天（当前明确不提供）
- [ ] 通用 IoT、摄像头动作、门锁解锁、云监控
- [ ] 显式通用 background task queue（schedule 已实现，但不等于通用任务队列）
- [ ] process list
- [ ] runtime cleanup

---

### Engineering Phase 6：前端

目标：提供本地 Web UI。

初版 UI 页面：

- [x] Project list
- [ ] Workline detail
- [x] Agent chat
- [x] Run summary 回顾卡片（接入 `/api/agents/{id}/runs/{runId}`，支持复制摘要与打开 Git 变更）
- [ ] Tool calls panel
- [x] File browser
- [x] Settings
- [x] License report

可选技术：

- React + Vite
- SvelteKit
- HTMX + Go templates

建议先用简单 React/Vite，后端静态托管 `web/dist`。

---

## 6. 开源协议整理计划

### 当前 Go MVP

可以从：

```txt
go.mod
go.sum
Go module cache LICENSE files
runtime/debug BuildInfo
```

生成依赖协议表。

后续可以增加命令：

```txt
autoto licenses export
```

生成：

```txt
THIRD_PARTY_NOTICES.md
licenses.json
```

### 上游参考二进制

仅靠二进制字符串不能可靠确定完整依赖协议。

若要整理上游参考实现的协议，需要输入：

```txt
package.json
bun.lockb / bun.lock
pnpm-lock.yaml / package-lock.json / yarn.lock
LICENSE
NOTICE
THIRD_PARTY_NOTICES
licenses 目录
其它子项目的 go.mod / Cargo.lock 等
```

拿到这些文件后，可以整理：

```txt
依赖名
版本
license
是否 copyleft
是否需要 NOTICE
是否需要源代码公开
是否可商用
风险等级
备注
```

---

## 7. 当前已知限制

当前 MVP 仍有这些限制：

- Telegram 是唯一入站渠道且只使用 long polling；命令仅 `/pair`、`/status`、`/approve <toolCallId>`（一次性）和 `/deny`。没有 `/task`、自由聊天、Telegram webhook、Slack 或 Discord。
- Telegram durable event/cursor 与 notification delivery history 不等于 Agent durable event log。Agent stream protocol 2 的 replay 仍只位于当前进程的有界内存；没有持久 retention、服务重启后或跨进程 replay。
- Home Assistant 是唯一设备适配器，且只允许本机/私网 endpoint。没有通用 IoT、摄像头动作、门锁解锁或云监控；本地 monitoring snapshot 只是聚合状态。
- Home Assistant 状态读取只返回过滤后的实体/属性；动作仅限固定 allowlist，并要求本地双确认和 direct-loopback 最终批准。IM 永远不能控制设备。
- Schedule 已实现，但不是通用任务队列：只允许 `readOnly` / `acceptEdits`，Agent busy 时跳过并记录，不排队，也不取消人工 run。
- 文件路径工具已硬阻断敏感路径，但 Bash 与 stdio MCP 仍能执行强本地操作，不能视为 sandbox。
- 前端 UI 已按 ES module 拆分，但仍有较多业务逻辑留在 `app-main.mjs`，不是完整 React/shadcn 实现。
- `/api/fs` 当前以 default project dir 为边界，尚未按 agent cwd 动态限制。
- Browser-originated API / WebSocket 已有本地 token 与 Origin/Sec-Fetch-Site 防护，但仍应只绑定可信本地地址。
- Git API 与 workline merge API 已限制 repo root 位于项目路径、default project dir 或 Autoto 创建的 `.autoto-worktrees` workline worktree 内；尚未实现 AI conflict resolve 与完整 review workline。
- license API 只确认了部分依赖协议。
- 已有 stdio MCP discovery/execution 与 registry；尚未实现 MCP 长连接会话池。
- 显式通用任务队列、进程列表与 runtime cleanup 尚未实现。

---

## 8. 下一步建议

产品 Phase A 的 Provider capability、Agent stream 与 Skills 基础已经收口；P2–P3 已把 schedules、durable deliveries、Telegram pairing/status/一次性 approval/deny、Home Assistant 受限适配、监控聚合和 Supervisor 生命周期接通。下一轮应先稳定现有边界，而不是扩张渠道或设备矩阵：

1. 为真实 Telegram bot + Home Assistant 环境补一份可重复的本地 dogfood/重启恢复记录，尤其验证 token 轮换撤销配对、delivery 重试和 busy schedule skip；
2. 保持 Telegram 命令面只含 `/pair`、`/status`、一次性 `/approve` 与 `/deny`，除非完成独立威胁模型与默认关闭设计，否则不加入 `/task`；
3. 保持 IM 与设备控制隔离，不允许 Telegram 创建或批准 Home Assistant action；
4. 补齐通知历史、channel events、device actions 的 retention/清理策略与更细监控，但不要称为云监控；
5. Slack/Discord、通用 IoT、摄像头动作和门锁解锁继续保持未完成，只有真实需求与安全审查后再立项；
6. 继续推进 review workline / AI conflict resolve、通用队列、process list 与 runtime cleanup。

所有文档与 UI 必须持续明确：当前有受限 Telegram 入站控制，但没有 `/task` 或通用 IM 聊天；当前有受限 Home Assistant 动作，但没有通用 IoT、摄像头动作、门锁解锁或云监控。
