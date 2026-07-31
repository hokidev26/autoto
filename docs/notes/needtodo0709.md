# needtodo0709：Autoto 后续改进计划（审查补强版）

## 0. 报告状态与审查来源

本报告已根据当前仓库的只读审查结果补强，目标是把 Autoto 从“功能型 MVP”推进到更稳定、更可用、更有产品差异化的本地 AI 编码代理工作台。

本次补强重点审查了四条主线：

1. Provider / agent loop / tool calling / retry timeout。
2. SQLite schema 初始化与迁移风险。
3. 前端运行闭环、审批、run 回顾、Git 审查与提交入口。
4. 仓库卫生、统一检查脚本、项目指令文件加载。

结论摘要：

- 报告原方向基本正确，但需要把“OpenAI 工具回灌未完成”修正为更严重的事实：默认 OpenAI 路径目前缺 native tool calling，OpenAI-compatible 同时缺 streaming 与 tools。
- DB migration 风险是实打实的 P0：当前只有 `CREATE TABLE IF NOT EXISTS`，没有 `PRAGMA user_version`。
- Run 通知与回顾很有产品价值，但前置条件是先建立 run 对象、run_id、run 状态机与可恢复审批。
- 仓库卫生整体比原报告预期更好：当前 `.gitignore` 已覆盖主要产物；真正更需要做的是统一检查脚本、清理/说明本地残留和空目录。

### 0.1 2026-07-09 当前执行进度更新

大部分 P0/P1 基础项已经完成或进入收尾：

- 已建立 SQLite migration 框架，并使用 `PRAGMA user_version` 管理版本。
- 已新增 runs / run_id / RunSummary 后端能力。
- 已补齐 provider 层的 OpenAI official / OpenAI-compatible 工具与流式方向，retry/backoff 与 first token timeout 也已有测试覆盖。
- 已支持 `AGENTS.md` / `CLAUDE.md` 项目指令加载。
- 已把 RunSummary 接到前端聊天区，形成“任务完成 → 回顾 → 查看 Git 变更 → 提交”的产品闭环入口。
- 已完成 Bash 输出流式事件，长时间运行的测试/构建命令会通过 agent WebSocket 推送实时输出，并在聊天区显示实时输出卡片。
- 已完成 Webhook 通知 MVP：服务端持久化通知配置，等待审批、任务完成、错误/中断/被替代时可异步 POST 到用户配置端点，设置页支持保存与测试发送。

因此，本文件后续旧风险段落可视为历史审查记录；新的执行重点调整为：

1. checkpoint / rollback。
2. 工具权限规则表与服务端化工作流偏好。
3. Skills 服务端化与全文搜索。
4. 成本预算与首次运行引导。
5. 通知签名、重试队列与通知历史。

## 1. 产品目标

Autoto 应继续围绕“本地优先的 AI 编码代理服务器”演进，定位类似自托管版 Claude Code / OpenHands 工作台，但差异化不应只是更多设置项，而是：

> 用户派任务，agent 后台执行；需要用户决策时主动提醒；完成后自动给出变更回顾；用户审查 diff 后一键提交。

优先级原则：

1. 先补核心 agent 能力，再继续扩展设置面板和边缘功能。
2. 先降低 provider、数据库、工具执行、账单和 Git 风险，再提升自动化程度。
3. 优先做能形成闭环的功能：派活、执行、审批、回顾、提交。
4. 避免现在进行大规模前端重写；继续通过 ES module 小步拆分。
5. 减少“配置项已展示但实际未生效”的 no-op 风险。

## 2. 当前项目定位

Autoto 当前是本地优先的 AI 编码代理工作台，核心能力包括：

- 嵌入式 Web UI。
- 多 LLM provider 接入。
- 带审批的工具执行。
- Git worktree 多工作线。
- SQLite 持久化。
- PTY 终端。
- 本地安全控制。
- 多 agent / workline 工作流。
- Git diff / commit modal。
- WebSocket 驱动的聊天与工具审批事件。

当前最值得强化的是：

1. Provider 能力对等，尤其是默认 OpenAI 路径的工具能力。
2. 数据库迁移机制。
3. Run 级状态、审批恢复、任务回顾与通知。
4. 统一开发检查入口。
5. AGENTS.md / CLAUDE.md 项目指令自动加载。

## 3. 当前优势

### 3.1 架构分层清楚

server、agent、providers、tools、db 等模块边界相对明确。Provider 和 Tool 接口已有清晰抽象，后续扩展新模型或工具有明确路径。

证据：

- Provider 统一入口在 `internal/providers`。
- agent 主循环在 `internal/agent/loop.go`。
- 工具实现集中在 `internal/tools`。
- 数据模型集中在 `internal/db/schema.go` 与 `internal/db/db.go`。

### 3.2 安全意识较强

项目已有本地 token、Origin 校验、工具审批、Git 路径边界、显式选择路径提交、禁止危险 Git 行为等机制。这些是 AI 编码代理非常重要的基础。

证据：

- 工具审批持久化在 `agent_tool_calls`。
- Git commit API 只提交显式选择路径，并做敏感路径检查。
- `.gitignore` 已覆盖本地 DB、运行残留、构建产物和 secrets 类文件。

### 3.3 工程纪律不错

已有测试、CI、lint、GoReleaser、架构文档、项目计划文档与安全说明。项目不是纯 demo，有继续演进为长期项目的基础。

证据：

- `.github/workflows/ci.yml` 已包含 Go lint/test/vet/build 和前端 `node --check` / `node --test`。
- `docs/ARCHITECTURE.md` 已记录前后端结构。
- `SECURITY.md` 已列出后续安全 hardening 方向。

### 3.4 已有闭环雏形

虽然还没有 run 级闭环，但已有可复用基础：

- 消息持久化。
- 工具审批卡片。
- `tool.approval_required` / `tool.finished` WebSocket 事件。
- Git status / diff / log / commit modal。
- Toast 提醒。

这意味着后续 Run 回顾视图不必从零开始，而是把现有能力接到 run_id 与 run summary 上。

## 4. 当前主要风险

### 4.1 P0：默认 OpenAI 路径缺工具能力

这是最高优先级问题。

当前 agent loop 本身是 provider-agnostic 的工具闭环，但 provider adapter 能力不对等：

- Anthropic 路径最完整，支持 tools 下发、tool_use 解析、tool_result 结构化回灌和流式文本。
- OpenAI official 路径支持 Responses API 文本流和 usage，但没有 native tool calling：没有传 `req.Tools`，没有解析 function/tool call 事件，历史被压成字符串。
- OpenAI-compatible 路径使用非流式 Chat Completions，固定 `stream: false`，没有 `tools` 参数，也没有解析 `choices[].message.tool_calls`。

关键证据：

- Anthropic：`internal/providers/anthropic_provider.go`。
- OpenAI official：`internal/providers/openai_official.go`。
- OpenAI-compatible：`internal/providers/openai_compatible.go`。
- 默认模型是 OpenAI：`internal/config/defaults.go` 中默认 `openai:gpt-4.1-mini`。

影响：新用户默认体验可能无法完整体现“编码代理”的工具调用能力。

### 4.2 P0：retry / backoff / 首 token timeout 配置存在但未真正闭环

配置中已有类似字段：

- `FirstTokenTimeoutMs`。
- `MaxTransientRetries`。

但当前主循环没有统一 retry/backoff，也没有首事件/首 token 计时器。`api_requests.ttft_ms` 字段存在，但记录链路未完整写入。

风险：用户看到配置项，以为已经生效；实际 provider 抖动、429、5xx、网络 EOF 或首 token 卡住时，体验仍可能无提示卡死或直接失败。

### 4.3 P0：缺少数据库迁移机制

当前数据库初始化主要依赖 `schemaSQL` 中的 `CREATE TABLE IF NOT EXISTS`，并由 `Store.migrate(ctx)` 执行。

问题：

- 没有 `PRAGMA user_version`。
- 没有顺序 migration 列表。
- 已存在表不会自动补字段。
- 新库可用、旧库可能运行时才出现 `no such column`。
- 无法区分全新空库、未标版本完整库、旧 schema 库和未来版本库。

关键证据：

- `internal/db/db.go` 中 `migrate(ctx)` 只执行 `schemaSQL`。
- `internal/db/schema.go` 只设置了 `PRAGMA foreign_keys = ON`，没有 `PRAGMA user_version`。
- 测试主要覆盖全新临时数据库路径，缺少旧库升级测试。

### 4.4 P1：Run 级闭环缺失

当前没有真正的 run 对象：

- 没有 `runs` 表。
- 没有 `run_id` 字段。
- 没有 run summary API。
- agent 只有 `running / idle / error / interrupted` 等粗粒度状态。
- tool approval 有持久化，但前端 pending approval 卡片主要保存在浏览器内存中，刷新后可能丢失展示。

现有 Git modal 是全局工作区视角，不知道某一轮 run 的开始和结束范围。

### 4.5 P1：功能广度略领先于核心深度

Settings 中已有不少面板和偏好，但部分能力仍停留在 localStorage 或 UI 层。例如 IM Gateway / 通知偏好目前更像浏览器本地偏好，不是服务端通知能力。

建议：先加强 agent 执行可靠性、审批恢复、通知、回顾、回滚等主流程体验，再继续扩展设置项。

### 4.6 P1：巨型文件维护风险

当前不是单文件前端，已经有 ES module 拆分；但主控文件仍偏大。

风险证据：

- `internal/agent/loop.go` 约 1400 行。
- `internal/server/static/modules/app-main.mjs` 约 1700 行。
- `internal/server/static/styles.css` 超过 4000 行。
- `model-provider-settings.mjs`、`system-settings.mjs`、`local-preferences-settings.mjs` 等设置模块也较大。

建议：不要全量 React/Vite 重写；继续围绕功能边界拆 `run-status.mjs`、`approval-state.mjs`、`run-summary.mjs`、`notification-center.mjs`。

### 4.7 P2：仓库卫生需要收尾，但不是大风险

当前 `.gitignore` 已覆盖主要产物：

- `/autoto`。
- `.autoto-run-*.json`。
- `*.db`、`*.db-wal`、`*.db-shm`。
- `.env`、`*.log`、`tmp/`、`coverage.out` 等。

观察到的本地残留：

- `autoto`：Mach-O 可执行文件，已被忽略。
- `.autoto-run-57314.json`：运行残留，已被忽略。
- `needtodo0709`：当前报告文件，未跟踪且未忽略。
- 空目录：`internal/auth`、`internal/agent`、`internal/project`。

因此原计划里“补 `.gitignore`”应改为“确认规则、清理残留、说明空目录用途、决定报告文件是否纳入版本控制”。

## 5. 修正版优先级总览

### P0：必须优先补齐

1. OpenAI / OpenAI-compatible 工具能力、流式与 provider parity。
2. 统一 retry/backoff、首 token timeout、可读错误提示。
3. SQLite migration 骨架与旧库升级测试。

### P1：高价值产品闭环

1. Run 对象、run_id、run 状态机。
2. 可恢复工具审批。
3. Run summary API 与回顾卡片。
4. Bash 输出流式显示。
5. AGENTS.md / CLAUDE.md 项目指令自动加载。
6. 统一检查脚本与文档命令收敛。

### P2：中期增强

1. Webhook / 多渠道通知。
2. 检查点与回滚。
3. Worklines 工作线可视化。
4. Skills 服务端化。
5. 会话全文搜索。
6. 成本预算与告警。
7. 分支 push 与 PR 草稿。
8. 首次运行引导。

## 6. P0-1：Provider tool parity 与可靠性补齐

### 6.1 目标

让 Anthropic、OpenAI official、OpenAI-compatible 在核心 agent loop 上尽量能力对等：

用户消息 → 模型请求工具 → 审批 → 工具执行 → 工具结果回灌 → 模型继续回答。

### 6.2 当前差异

#### Anthropic

当前最完整：

- 支持流式文本。
- 支持 tools schema 下发。
- 支持 tool_use 解析。
- 支持 tool_result 结构化回灌。
- 支持 usage。
- 支持图片 base64。

仍缺：

- Autoto 层统一 retry/backoff。
- 首 token timeout。
- 更统一的 provider 错误分类。

#### OpenAI official

已有：

- Responses API 文本流。
- usage。
- 部分错误事件处理。

缺口：

- 没有 native tool calling。
- 没有把 `GenerateRequest.Tools` 传给 OpenAI。
- 没有解析 function/tool call 事件。
- 历史消息被压成文本 transcript，tool_use / tool_result 结构丢失。

#### OpenAI-compatible

已有：

- `/models`。
- optional API key。
- 非流式 Chat Completions。
- usage。
- 图片以 `image_url` 发送。

缺口：

- 没有流式输出。
- 没有 `tools` 参数。
- 没有解析 `choices[].message.tool_calls`。
- tool result 只是文本化，不是标准 Chat Completions tool message。
- 如果 endpoint 返回空 `content` + `tool_calls`，当前可能把 tool call 静默吞掉。

### 6.3 建议任务

#### A. 定义跨 provider 工具 contract

明确 `providers.GenerateRequest.Tools` 的 provider contract：

- 工具 schema 如何表达。
- tool call ID 如何映射。
- tool arguments 如何累积和验证。
- tool result 历史如何传回下一轮模型请求。
- denied / error 工具结果如何表达。
- provider 不支持 tools 时如何降级提示。

#### B. OpenAI official 支持 native tools

任务：

- 不再只用纯文本 `renderTranscript`。
- 将工具 schema 传入 Responses API。
- 解析 function/tool call 事件并 emit `providers.Event{Type: "tool_call"}`。
- 将历史中的 tool_use / tool_result 转为 OpenAI 可理解的结构化 input。
- 覆盖 denied tool、tool error、JSON arguments 分片等边界。

#### C. OpenAI-compatible 支持 streaming 与 tools

任务：

- Chat Completions 请求增加 `tools`。
- 支持 `stream: true` SSE。
- 累积 `delta.tool_calls[*].function.arguments`。
- 完成后 emit `tool_call` 事件。
- 下一轮请求把 tool result 转成 `role: "tool"` 或兼容格式。
- 对不支持 tools 的 compatible endpoint 提供 capability/degrade 提示。

#### D. Provider capability 标记

建议新增能力标记：

- `SupportsTools`。
- `SupportsStreaming`。
- `SupportsVision`。
- `SupportsUsage`。
- `SupportsPromptCache`。

用途：

- UI 可以给出明确提示。
- agent 可以在不支持 tools 时避免假装可用。
- 设置页可以更准确展示 provider 能力差异。

#### E. 统一 retry/backoff 与首 token timeout

任务：

- 在 provider 外层或 `Runner.runModelTurn` 实现统一 retry。
- 使用现有 `FirstTokenTimeoutMs` 与 `MaxTransientRetries` 配置。
- transient 错误至少包含 HTTP 408 / 409 / 429 / 5xx、网络 reset、EOF、temporary timeout。
- 401/403、invalid request/schema、context canceled 不重试。
- 流式规则：首事件前失败可重试；已经向 UI 输出文本或 tool_call 后不静默重试，避免重复输出或重复工具调用。
- 记录 TTFT 到 `api_requests.ttft_ms`。

### 6.4 验收标准

- OpenAI official fake server 返回 function/tool call 后，Runner 会执行工具并发起第二次模型请求。
- 第二次 OpenAI 请求包含结构化工具结果，不是纯文本拼接。
- OpenAI-compatible fake SSE 返回 `tool_calls` delta 后，Runner 能执行工具并继续回答。
- denied tool result 能作为错误工具结果回灌。
- compatible 非流式 tool_calls 不会被吞掉。
- provider 临时 500/429 前几次失败、后续成功时能按配置重试。
- 401/403 不重试，并显示可读错误。
- 首事件超过 `FirstTokenTimeoutMs` 后退出，并在 UI/DB 中显示明确 timeout。
- partial stream 后错误不自动重试。
- `api_requests.ttft_ms` 有非零记录，并可在 usage/runtime 视图中展示。
- 测试覆盖 `internal/providers/*_test.go` 与 `internal/agent/loop_test.go` 的端到端 adapter case。

## 7. P0-2：引入数据库迁移机制

### 7.1 目标

为后续 runs、skills、预算、搜索等 schema 演进打基础，避免旧数据库升级事故。

### 7.2 当前状态

当前 DB 初始化路径：

- `cmd/autoto/main.go` 打开数据库。
- `internal/config/defaults.go` 设置默认 DB 路径。
- `internal/db/db.go` 的 `Open(...)` 调用 `store.migrate(ctx)`。
- `store.migrate(ctx)` 只执行 `schemaSQL`。
- `schemaSQL` 在 `internal/db/schema.go` 中，用一大段 `CREATE TABLE IF NOT EXISTS` / `CREATE INDEX IF NOT EXISTS`。

当前表包括：

- `users`。
- `projects`。
- `worklines`。
- `agents`。
- `agent_messages`。
- `agent_message_attachments`。
- `agent_tool_calls`。
- `api_requests`。
- `agent_backends`。
- `mcp_servers`。

注意：部分文档里的表清单可能已旧，需同步补上 `agent_message_attachments` 与 `mcp_servers`。

### 7.3 风险

- 旧库已有表但缺字段时，`CREATE TABLE IF NOT EXISTS` 不会补字段。
- 代码可能在运行时才遇到 `no such column`。
- 没有版本号，无法判断当前 DB 是新库、完整旧库、部分旧库还是未来版本。
- 没有 migration 事务，失败后可能半升级。
- 未来新增字段若只改 baseline schema，新用户正常，老用户失败。

### 7.4 建议任务

新增 `internal/db/migrations.go`：

- `const CurrentDBVersion = 1`。
- migration struct：`version`、`name`、`up func(context.Context, *sql.Tx) error`。
- `getUserVersion`。
- `setUserVersion`。
- `runMigrations`。
- `tableExists`。
- `columnExists`。
- `ensureColumn`。
- `ensureIndex`。

迁移流程：

1. 打开 SQLite。
2. 明确启用 `PRAGMA foreign_keys = ON`。
3. 读取 `PRAGMA user_version`。
4. 如果版本大于 `CurrentDBVersion`，拒绝启动并返回可读错误。
5. 如果是空库，执行 v1 baseline schema 并设置版本。
6. 如果是未标版本但已有表的旧库，检查并补齐当前代码依赖的缺失表、字段和索引。
7. 每个 migration 在事务中执行。
8. 每个 migration 成功后更新 `PRAGMA user_version`。
9. 后续新增表/字段必须追加 migration 和测试，不得只修改 baseline schema。

### 7.5 Quick win 范围

1-2 天内可以先做骨架：

- 不引入复杂业务 schema。
- 将当前 schema 作为 v1 baseline。
- 支持空库初始化。
- 支持未标版本完整旧库设为当前版本。
- 至少构造一个缺字段旧库测试，证明能补列且不丢数据。

### 7.6 验收标准

- 新建空数据库后，所有当前表和索引存在。
- 新建空数据库后，`PRAGMA user_version = CurrentDBVersion`。
- 当前未标版本但 schema 完整的旧库，打开后不丢数据并设置版本。
- 人工构造旧库缺代表性字段，例如 `agents.prune_boundary_message_id`，打开后自动补齐。
- 连续 `Open` 两次不报错，数据行数不变化，migration 幂等。
- 未来版本库被拒绝，错误信息清晰。
- 迁移后 `PRAGMA foreign_keys = 1`。
- 测试建议放在 `internal/db/db_test.go`：
  - `TestOpenInitializesUserVersionForNewDatabase`。
  - `TestOpenMigratesLegacyZeroVersionDatabase`。
  - `TestOpenMigratesLegacyDatabaseMissingAgentColumns`。
  - `TestMigrateIsIdempotent`。
  - `TestOpenRejectsFutureDatabaseVersion`。
  - `TestForeignKeysEnabledAfterOpen`。

## 8. P1：Run 级执行闭环、审批恢复与回顾视图

### 8.1 目标

形成 Autoto 的核心产品闭环：

派活 → 后台执行 → 需要审批时提醒 → 用户批准/拒绝 → 工具继续执行 → 完成后回顾 → 审查 diff → 提交。

### 8.2 当前状态

已有基础：

- agent loop 可后台 goroutine 执行。
- WebSocket 有 `agent.started`、`agent.done`、`agent.error`、`tool.approval_required`、`tool.finished` 等事件。
- 工具审批记录在 `agent_tool_calls`。
- 前端能显示审批卡片并调用审批 API。
- Git modal 已支持 status / diff / log / commit。
- Toast 能显示本地提醒。

缺口：

- 没有 `runs` 表。
- 没有 `run_id`。
- 没有 run 状态机。
- 没有 run summary API。
- pending approval 前端状态主要保存在内存，刷新后展示可能丢失。
- run 结束后不会自动展示 summary card。
- Git modal 不是 run-aware，无法自动关联本轮触碰文件。

### 8.3 建议把 P0-3 修正为 P1-核心闭环

原报告把“后台任务 + 审批通知 + Run 回顾视图”列为 P0。经审查，它很有产品价值，但应排在 provider parity 和 DB migration 之后。

更准确的标题：

> P1：Run 级执行闭环与审批恢复。

### 8.4 数据模型建议

新增 `runs` 表：

- `id`。
- `agent_id`。
- `trigger_message_id`。
- `status`：`queued / running / waiting_approval / completed / error / interrupted`。
- `started_at`。
- `completed_at`。
- `error_message`。
- `base_head`。
- `end_head`。
- `created_at`。
- `updated_at`。

新增字段：

- `agent_messages.run_id`。
- `agent_tool_calls.run_id`。
- 可选：`api_requests.run_id`。

### 8.5 API 建议

新增：

- `GET /api/agents/{id}/runs?limit=...`。
- `GET /api/agents/{id}/runs/{runId}`。
- `GET /api/agents/{id}/runs/{runId}/summary`。
- `GET /api/agents/{id}/tool-calls?status=pending_approval` 或 run-scoped pending approvals API。

Run summary 内容：

- run 状态、耗时、触发消息、最终消息。
- 模型请求次数、usage、成本。
- 工具调用列表：名称、状态、耗时、错误。
- pending / denied / error 工具数。
- 触碰文件：初版可从工具 input/output 近似提取，后续再做可靠追踪。
- Git diff 文件数与 +/- 统计。
- 错误信息。
- 操作入口：查看 diff、去提交、复制 summary。

### 8.6 前端建议

新增或拆分模块：

- `run-status.mjs`：当前 run 状态、顶部/聊天区状态卡。
- `approval-state.mjs`：pending approval 恢复、审批卡片状态、审批 API。
- `run-summary.mjs`：summary card、summary API、diff/commit 入口。
- `notification-center.mjs`：in-app toast 与后续 webhook 配置入口。

第一版不必立刻做 webhook，先做 in-app durable 状态：

- running 时显示 run 状态。
- waiting approval 时显示明显提醒。
- 刷新页面后仍能恢复 pending approval。
- done/error/interrupted 时显示 summary card。

### 8.7 验收标准

- 发送一条用户消息后创建一个 run。
- `agent.started / tool.approval_required / tool.finished / agent.done / agent.error` 都携带 `runId`。
- UI 可区分 `running / waiting_approval / completed / error / interrupted`。
- 工具等待审批时，刷新页面后仍能看到审批卡片并完成审批。
- run 完成后显示 summary card，至少包含工具调用数量、changed files 数量、diff +/- 统计、错误文本。
- “查看 diff / 去提交”复用现有 Git modal。
- 若无法可靠识别本轮文件，第一版至少打开 Git modal 并显示提示。
- error/interrupted 也生成 summary，不只成功 run。
- 增加 e2e 或集成测试覆盖：提交消息 → 等待审批 → 批准 → tool finished → run done → summary card → Git modal 可打开。

## 9. P1：后台通知能力

### 9.1 当前状态

已有：

- 前端 toast。
- 浏览器本地通知偏好。
- IM Gateway / 通知相关设置页雏形。

但当前没有真正的服务端 webhook 通知能力：

- 没有 webhook URL 服务端配置。
- 没有通知发送器。
- 没有签名、重试、脱敏。
- 没有 run 状态变化触发器。

### 9.2 建议分阶段实现

#### 阶段 1：in-app durable notification

依赖 run 状态机：

- run waiting_approval 时 UI 明显提醒。
- run completed/error/interrupted 时 UI toast + summary card。
- 页面刷新后可恢复当前等待审批的 run。

#### 阶段 2：webhook MVP

新增：

- server-side webhook URL 配置。
- run 状态变化触发通知。
- 通知包含项目名、agent、run status、工具摘要、跳转链接。
- 基础重试。
- 敏感内容脱敏。

#### 阶段 3：多渠道与安全增强

- 通知签名。
- 每项目/每 agent 策略。
- 多渠道。
- 失败队列与重放。

### 9.3 验收标准

- run 进入 waiting_approval / completed / error / interrupted 时至少有 in-app durable 提醒。
- webhook 配置开启后，服务端能发送状态变化通知。
- 通知失败不会阻塞 agent loop。
- 通知内容不泄露 API key、token、完整敏感命令输出。
- 重试次数有限，失败可诊断。

## 10. 1-2 天 Quick Wins（修正版）

### 10.1 统一检查脚本

优先级：高。

当前没有 `Makefile`、`scripts/check.sh`、`package.json`。README / CONTRIBUTING / docs / PROJECT_PLAN 中存在多处手写检查命令，容易和 CI 漂移。

任务：

- 新增 `scripts/check.sh` 作为唯一权威本地检查入口。
- 可选新增 `Makefile`，让 `make check` 调用 `scripts/check.sh`。
- CI 改为调用同一个脚本，减少重复维护。
- README / CONTRIBUTING / docs 中只保留一条统一命令。

建议检查内容：

- `gofmt -l ./cmd ./internal`，检查格式但不自动改写。
- `go test ./...`。
- `go vet ./...`。
- `go build ./...`。
- 前端 `node --check`。
- `node --test internal/server/static/modules/*.test.mjs`。

验收标准：

- 开发者运行一个命令即可完成主要检查。
- CI 与本地检查入口一致。
- README 不再维护一长串手写 `node --check` 清单。
- `check` 不做破坏性或自动格式化改写；如需格式化，另设 `fmt`。

### 10.2 DB migration 骨架

优先级：高。

任务：

- 建立 `PRAGMA user_version` 迁移框架。
- 将当前 schema 作为 v1 baseline。
- 添加新库、未标版本旧库、缺字段旧库、幂等、未来版本保护测试。

验收标准见第 7 节。

### 10.3 OpenAI tool calling 实现点梳理

优先级：高。

任务：

- 明确 OpenAI official Responses API tools 映射。
- 明确 OpenAI-compatible Chat Completions tools 映射。
- 明确 agent message history 中 tool_use / tool_result 的 provider-specific 转换。
- 写 fake server 测试设计。

产出：

- provider contract 文档或代码注释。
- 至少一个 OpenAI official tool call fake test。
- 至少一个 OpenAI-compatible streaming tool call fake test。

### 10.4 AGENTS.md / CLAUDE.md 项目指令加载

优先级：高，难度：低到中。

当前未实现。

任务：

- 支持读取 agent cwd 下的 `AGENTS.md`。
- 可选支持 `CLAUDE.md`。
- 做路径边界校验。
- 设置大小上限和截断提示。
- 将内容合并到 system prompt。
- UI/API 显示“已加载项目指令：文件名 / 是否截断”。

验收标准：

- 项目根目录存在 `AGENTS.md` 时，agent 自动参考其中规范。
- 同时存在 `AGENTS.md` 与 `CLAUDE.md` 时，有明确优先级或合并规则。
- 文件过大时截断，不导致上下文失控。
- 读取失败不阻塞主流程，但给出可诊断提示。
- 用户能知道当前是否加载了项目指令。

### 10.5 抽出模型价格表

优先级：中。

任务：

- 将硬编码价格表从 `internal/agent/loop.go` 中拆出。
- 可以放到独立 Go 文件或嵌入 JSON。
- 给价格表单独测试。

验收标准：

- loop 主逻辑更短。
- 价格表可单独测试和更新。
- 未知模型有明确 fallback。

### 10.6 仓库卫生收尾

优先级：中。

任务：

- 确认 `.gitignore` 已覆盖当前观察到的 `/autoto` 和 `.autoto-run-*.json`。
- 删除或保留说明本地残留 `autoto`、`.autoto-run-57314.json`。
- 处理空目录 `internal/auth`、`internal/agent`、`internal/project`：删除或加明确用途占位。
- 明确 `needtodo0709` 是否纳入版本控制。

验收标准：

- `git status` 中没有无意义产物。
- 编译产物和运行残留不会被提交。
- 空目录若保留，应有明确用途或占位说明。
- 报告文件的版本控制策略明确。

## 11. 1-2 周短期计划

### 11.1 Provider 可靠性补齐

包含：

- OpenAI official native tool calling。
- OpenAI-compatible streaming。
- OpenAI-compatible tool_calls。
- provider capability 标记。
- retry/backoff。
- first token timeout。
- TTFT 记录。
- 错误分类与 UI 提示。

优先级：最高。

### 11.2 DB migration 完整化

包含：

- migration skeleton。
- legacy zero-version database 兼容。
- 缺字段补齐。
- 未来版本保护。
- migration 文档。
- schema change policy。

### 11.3 Bash 输出流式显示

目标：长命令执行时不要等结束才看到结果。

当前问题：`BashTool.Execute` 使用类似 `CombinedOutput()` 的结束后返回模式，前端只能在工具完成后看到结果。

MVP：

- Bash 执行时按块发送 stdout/stderr chunk。
- 前端工具卡片实时追加输出。
- DB 中仍保存最终截断结果。
- timeout 后保留已输出内容并标记 error。

价值：

- 运行测试、构建、安装依赖时体感明显提升。
- 用户更容易判断 agent 是否卡住。

### 11.4 Plan Mode 闭环

目标：让 agent 在动手修改前先只读分析并产出计划。

当前有一些 plan mode 痕迹，但行为未完整闭环。

MVP：

- 新增或明确 `plan` 权限模式。
- 只允许只读工具。
- system prompt 要求先产出计划。
- 用户批准后再切回可写模式。
- UI 明确显示当前处于 plan mode。

价值：

- 降低 agent 误改风险。
- 适合复杂任务和高成本模型。

### 11.5 结构化日志与 run ID

目标：提升问题排查能力。

任务：

- 使用结构化日志记录 provider 请求、工具调用、审批等待、错误。
- run ID 贯穿 agent loop、工具执行和 WebSocket 事件。

验收标准：

- 用户反馈“卡住”时，可以通过 run ID 快速定位阶段。
- provider 失败、工具失败、审批等待可区分。

## 12. 1-2 个月中期计划

### 12.1 Run 回顾视图

MVP：

- 新增 run summary API。
- 聚合本轮工具调用、触碰文件、diff 统计、错误信息。
- 前端在 run 结束时显示摘要卡片。
- 摘要卡片提供“查看 diff / 去提交”入口。

价值：

- 用户不用在聊天、工具卡片、Git 面板之间来回切。
- 更容易审查 agent 结果。

### 12.2 后台任务 + 审批通知

MVP：

- 支持配置 webhook URL。
- run 进入 waiting_approval / completed / error / interrupted 时发送通知。
- 通知包含项目名、agent、工具摘要、跳转链接。

后续增强：

- 重试机制。
- 通知签名。
- 多通知渠道。
- 每个项目单独配置通知策略。

### 12.3 检查点与回滚

MVP：

- 每轮 run 开始前记录 Git 状态。
- 如果 worktree 干净，记录 HEAD。
- 如果 worktree 脏，考虑创建临时快照。
- 提供恢复到 run 开始前的 API 和 UI 按钮。

注意：

- 初版只支持 Git 仓库内文件。
- 必须明确提示会影响当前未提交改动。
- 不应偷偷执行破坏性 Git 命令。

### 12.4 Worklines 工作线可视化面板

MVP：

- 侧边栏或独立面板展示 workline 树/列表。
- 显示分支名、worktree 状态、merge-check 状态。
- 提供 fork、merge-check、merge 的入口。

价值：

- 将 Autoto 独特的多工作线能力产品化。
- 降低用户理解 workline / fork / merge 的门槛。

### 12.5 Skills 服务端化

MVP：

- 新增 `skills` 表。
- 提供 CRUD API。
- 前端从服务端读取和保存 skills。
- 保留从 localStorage 导入的迁移路径。

价值：

- 技能不再只存在浏览器。
- 可跨 agent、跨项目或未来跨设备复用。

### 12.6 会话全文搜索

MVP：

- 使用 SQLite FTS5 为消息内容建立索引。
- 新增搜索 API。
- 前端提供全局搜索入口。
- 搜索结果可跳转到对应 agent / message。

价值：

- 长期使用后能找回历史决策、bug 修复过程和 agent 操作记录。

### 12.7 项目成本预算与告警

MVP：

- 项目级预算。
- agent 或 run 级成本统计。
- 超过阈值时提醒或阻止继续执行。
- 明确展示估算价格来源和未知模型 fallback。

## 13. 长期方向

### 13.1 真实登录与 session 认证

当前不必立刻做多用户，但建议提前把本地 token、远程硬化模式、用户表等概念抽象成统一 session 接口。

长期价值：

- 支持更安全的远程访问。
- 为未来多设备或多用户能力留接口。

### 13.2 AI 冲突解决与 review workline

目标：进一步发挥 workline / worktree 架构价值，让 agent 可以辅助处理 merge conflict 或独立审查另一条工作线。

### 13.3 MCP 长连接会话池

目标：提升 MCP 工具调用效率，减少重复初始化开销。

### 13.4 前端框架迁移评估

目前不建议立刻全量迁移 React / Vite。应先继续拆分现有模块，等 UI 复杂度确实超过原生模块维护能力后，再做框架迁移评估。

## 14. 可新增功能清单

| 功能 | 优先级 | 难度 | MVP |
| --- | --- | --- | --- |
| OpenAI native tool calling | P0 | 中 | OpenAI 模型可完成工具调用闭环 |
| OpenAI-compatible streaming + tools | P0 | 中 | SSE 流式与 tool_calls 解析 |
| retry/backoff/first token timeout | P0 | 中 | 临时错误有限重试，首 token 超时可见 |
| DB migration | P0 | 中 | `PRAGMA user_version` + baseline migration |
| 统一检查脚本 | P1 | 低 | `scripts/check.sh` / `make check` |
| AGENTS.md / CLAUDE.md 加载 | P1 | 低到中 | 自动加载项目指令并显示状态 |
| Run 状态机 | P1 | 中 | `runs` 表 + run_id + 状态展示 |
| 可恢复审批 | P1 | 中 | 刷新后仍能看到 pending approval |
| Run 回顾视图 | P1 | 中 | summary API + summary card |
| Bash 输出流式显示 | P1 | 中 | 工具卡片实时追加输出 |
| 后台 webhook 通知 | P2 | 中 | run 状态变化发送 webhook |
| 检查点与回滚 | P2 | 中 | run 开始前记录 Git 状态 |
| Worklines 工作线可视化 | P2 | 中 | 展示 workline、branch、worktree 状态 |
| 会话全文搜索 | P2 | 低到中 | SQLite FTS5 搜索消息 |
| 成本预算与告警 | P2 | 低 | 项目预算和阈值提醒 |
| Skills 服务端化 | P2 | 中 | skills 表 + CRUD API |
| 分支 push 与 PR 草稿 | P3 | 中 | 显式确认后生成/push PR 草稿流程 |
| 首次运行引导 | P2 | 低 | 配置 key、验证模型、创建项目三步向导 |

## 15. 暂时不建议优先做的事情

1. 继续增加 Settings 面板。
2. 继续新增 browser-local 偏好。
3. 前端全量重写到 React / Vite。
4. 多用户或团队协作能力。
5. 在没有真实服务端通知能力前继续打磨 IM Gateway 策略面板。
6. 过早做复杂云同步或账号体系。
7. 在没有 run_id 前做复杂 run summary UI。
8. 在没有 provider capability 前假设所有 provider 能力等价。

## 16. 建议的 0709 执行顺序

如果 0709 只安排一天，建议按这个顺序做：

1. 建立 `scripts/check.sh` / `make check`，把 README 和 CI 收敛到同一入口。
2. 加 DB migration 骨架：`PRAGMA user_version`、baseline migration、基础旧库测试。
3. 梳理并实现 OpenAI official tool calling 的最小闭环 fake test。
4. 梳理 OpenAI-compatible streaming + tool_calls 的 fake test 和实现点。
5. 加 `AGENTS.md` 项目指令加载。
6. 把模型价格表从 `loop.go` 中拆出。
7. 清理或说明本地残留与空目录。

如果当天时间充足，再继续：

8. 开始实现 provider retry / first token timeout。
9. 草拟 `runs` 表和 run summary API 数据结构。
10. 为 pending approval 恢复设计 API。

## 17. 最重要的三个下一步

1. **修复默认 OpenAI 路径的工具能力**：OpenAI official 和 OpenAI-compatible 必须能完成工具调用闭环，否则默认体验不像 AI coding agent。
2. **建立 DB migration 机制**：尽快引入 `PRAGMA user_version`，避免后续 runs、skills、search 等 schema 演进造成旧库事故。
3. **建立 run_id 与可恢复审批**：这是后台通知、Run 回顾视图、diff/commit 闭环的共同前置条件。

## 18. 核心产品判断

Autoto 最有潜力的差异化功能不是继续堆设置项，而是把“常驻服务器 + 持久化 + 多工作线 + 审批工具执行”结合起来，形成普通 CLI agent 难以做到的体验：

> 用户派任务，agent 后台执行；需要用户决策时主动提醒；完成后自动给出变更回顾；用户审查 diff 后一键提交。

因此，后续功能应该围绕这个闭环推进。但实现顺序上必须先补底座：provider parity、DB migration、run_id。只有底座稳定，通知、回顾、回滚和自动提交才不会变成脆弱的 UI 包装。