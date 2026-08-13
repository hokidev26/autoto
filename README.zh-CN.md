# Autoto

[English](README.md) | [繁體中文](README.zh-TW.md) | 简体中文

Autoto 是一个跑在你自己机器上的 coding agent。你给它一个任务，它在后台做，遇到你会想被问一声的事情，它会先问。

**任务 → 后台 run → 批准 → run 摘要 → diff → 指定路径 commit**

![Autoto local agent workflow demo](docs/demo.svg)

## 你可以用它做什么

**丢给它然后去做别的事。** 任务在后台跑，可以同时排几个。每个 run 结束会给你摘要、可查看的 diff，以及只暂存你勾选路径的 commit。它不会替你 push、amend、reset，也不会用 `git add -A`。

**用手机看它做到哪。** 界面是为小屏幕设计的，不是把桌面版缩小塞进去：下拉刷新、通知左滑关闭、输入框不会被挤掉。可以安装到手机主屏幕独立打开，没有浏览器外框，用起来像一般 App。

**从外面连回你的机器。** 在设置页开一条临时 Cloudflare 通道，会给你网址和二维码直接扫。远程连接要密码，分两种模式：受限模式只能跟进度与批准，完整控制要你明确开启。连接会过期，而且把策略改严会连已经开着的连接一起撤销。

**在手机上批准，或用 Telegram。** run 需要危险操作的权限时，你可以远程放行或拒绝。Telegram 配对只限私聊，范围刻意做窄：查状态、一次性批准、拒绝。它不是聊天助手，也没有 `/task`。

**把你的模型当 API 分享出去。** Autoto 可以把你配置好的 provider 以 OpenAI 兼容的 `/v1` 端点提供给你其他工具和设备用。每个密钥有自己的模型白名单和用量归户，所以你可以只分享一部分，而不是把全部交出去。

**在隔离的副本里动工。** 把 workline fork 成独立的 Git worktree，让 agent 在那里做，做完先检查合并是否干净再并回来。

## 它不会做的事

Autoto 是实验性质的本机开发 MVP，不是经过加固的多人或生产环境服务。

有些操作是直接拒绝，不只是需要批准。递归删除、对裸设备写入、放宽权限、把下载内容直接喂给 shell 执行，这些被归类为不可逆，任何权限模式、允许规则或人工批准都不能执行它们。这个闸门刻意做成不能为了方便而关掉。

通常存放密钥的文件对文件工具是硬阻拦：`.env*`、凭证与密钥材料、`.git` 内容。`Read`、`Write`、`Edit` 直接拒绝，`Glob` 与 `Grep` 则完全不把它们列进结果。

## 快速开始

需要 Go 1.26 或更新版本：

```bash
go run ./cmd/autoto
```

然后打开：

```text
http://localhost:16888
```

默认的本机状态位置：

```text
配置文件：~/.autoto/config.json
数据库：  ~/.autoto/autoto.db
项目目录：~/projects
```

## 下载安装

每个 tag 的 release 会发布两种可执行文件。它们是同一个产品，区别只在你怎么打开界面。

**CLI** — `autoto_<版本>_<系统>_<架构>`，支持 macOS、Linux、Windows，amd64 与 arm64 都有。跑起来是一个本机服务器，你用浏览器打开界面。这个是交叉编译的，所有平台由同一个 job 产出。

**桌面版** — `autoto-desktop_<版本>_<系统>_<架构>.tar.gz`，支持 macOS（arm64 与 amd64）和 Linux amd64。同一个服务器，但用原生窗口取代浏览器标签页。每个平台都在各自的 runner 上编译，因为桌面外壳要链接该系统的原生 WebView，那个没法交叉编译。

从 GitHub Releases 下载对应的文件，解压后执行即可。校验码一起发布：CLI 压缩包看 `checksums.txt`，桌面版每个压缩包旁边有各自的 `.sha256`。

下载桌面版之前有两件事要知道。它没有做代码签名与公证，所以 macOS 的 Gatekeeper 第一次打开会拒绝，你要到「系统设置 → 隐私与安全性」明确允许；Windows 也可能出现类似警告。另外 Linux 桌面版只有 amd64，arm64 的 Linux 请用 CLI 版。

从源码运行：

```bash
go run ./cmd/autoto
```

也可以指定自定义配置文件路径：

```bash
go run ./cmd/autoto --config /path/to/config.json
```

### 构建可执行文件

CLI 版（可以交叉编译到任何支持的平台）：

```bash
go build -o autoto ./cmd/autoto
```

桌面版必须加 `desktop` 构建标签——不加会直接编译失败（"build constraints exclude all Go files"）——而且需要该平台的原生 WebView 工具链，所以没法交叉编译。Release 没有发布 Windows 桌面版，但从源码构建没问题：

```bash
go build -tags desktop -o autoto-desktop ./cmd/autoto-desktop
```

Windows 上再加 `-ldflags "-H windowsgui"`，桌面外壳启动时才不会多开一个控制台窗口。

想要接近 release 的瘦身版，可以去掉调试信息与本机路径（大约小 25%；panic 堆栈仍保留函数名称但没有文件路径，`pprof` 之类的工具会少掉符号细节）：

```bash
go build -trimpath -ldflags "-s -w" -o autoto ./cmd/autoto
go build -tags "desktop,production" -trimpath -ldflags "-s -w -H windowsgui" -o autoto-desktop ./cmd/autoto-desktop
```

`production` 标签会另外关掉 Wails 的 devtools。完整构建参考见 `docs/BUILD.md` 和 `Makefile`。

## 系统要求

- Go 1.26 或更新版本，以 `go.mod` 的声明为准
- SQLite 通过纯 Go 的 `modernc.org/sqlite` 驱动提供，不需要另外安装
- Node.js 是可选的，只在验证阶段用来对内嵌前端脚本跑 `node --check` 与 `node --test`

## 主要功能

英文版 README 有逐项的完整清单。这里按类别整理，方便先掌握轮廓。

**Agent 与模型**

- 本机 HTTP 服务器，内嵌 HTML/CSS/JS 界面，前端用免构建的 ES module 架构
- SQLite 持久化：项目、workline、agent、消息、工具调用、backend 注册、stdio MCP 注册
- Provider 抽象层，以最小的 `Tools` / `Streaming` / `ImageInput` 能力契约接上 OpenAI Responses API、Anthropic Messages API、OpenAI 兼容 Chat Completions、Gemini Interactions API、Kiro（Amazon Q）原生订阅，以及本机 CLIProxyAPI 预设组
- 核心工具：Read、Write、Edit、Bash、Glob、Grep、WebFetch、WebSearch、MCPListTools、MCPCallTool
- 续跑预算设置：可按工作区限制续跑次数、总回合、总 token 与实际执行时间，默认无上限

**安全边界**

- 敏感路径硬阻拦：`Read`、`Write`、`Edit` 直接拒绝受保护文件，`Glob` 与 `Grep` 则跳过不列出。范围包含 `.env*`、凭证与密钥文件、常见私钥材料，以及 `.git` 内容
- Bash 危险反思：可设置强度（关闭／宽松／中等／严格）的 LLM 安全闸，在执行前用当前对话模型审查高风险命令，按结构化判定放行或阻拦
- 递归删除、磁盘写入、权限放宽等灾难性且不可逆的操作属于硬阻拦层级，任何权限模式都不能执行，也不能用批准绕过
- Git 操作限定在 status、diff、log 与指定路径 commit，不会自动 push、amend、reset、clean、force，也不会用 `git add -A`

**工作流程与界面**

- Workline 与容器设置，支持创建 Git worktree 的 workline fork、合并前检查，以及干净 worktree 的合并 API
- 交互式 PTY 终端 WebSocket，含终端管理与浏览器端的保留／聚焦偏好
- 调度 worker，支持 cron 与 `@every` 表达式和 IANA 时区。调度权限上限为 `readOnly` 或 `acceptEdits`，不会中断或取代正在跑的手动 run，且无人值守的 run 不会沿用交互时给过的 session 批准
- 具持久性的 Webhook／Telegram 通知投递记录，含去重、租约、指数退避、次数上限与显式重试
- 服务器端 Skills 与生命周期 hooks，含版本历史、还原、快照稳定的分派，以及沿用既有批准与审计闸道的 Shell／HTTP 动作
- 设置页涵盖 Providers、自动化、通知、外观、存储、用量、用户与授权信息

## 配置

首次启动时，Autoto 会在配置文件不存在时创建一份。运行期的密钥可以用环境变量提供。`config.json` 有 schema `version` 字段；没有这个字段的旧配置会被当成版本 `1` 载入并在内存中规范化。

Agent 模型相关环境变量：

```text
AUTOTO_DEFAULT_MODEL
AUTOTO_SUMMARY_MODEL
AUTOTO_CONTEXT_TOKEN_LIMIT
```

Provider 环境变量的完整清单请看 [英文版 README](README.md#configuration)，包含 OpenAI、Anthropic、Gemini、OpenAI 兼容端点与 CLIProxyAPI 各自的变量名。

### 自动化集成与密钥引用

Telegram 与 Home Assistant 的连接配置分成两部分存储：非密钥的元数据，加上密钥的逻辑引用。目前只接受 `env:变量名` 这种引用格式；连接 API 与界面会直接拒绝明文 token，而公开的响应只会显示每个必要密钥「是否已配置」，不会返回值。

先在 Autoto 的进程环境设置实际值：

```sh
export AUTOTO_TELEGRAM_BOT_TOKEN='replace-with-current-bot-token'
export AUTOTO_HOME_ASSISTANT_TOKEN='replace-with-current-ha-token'
```

然后在连接配置里用引用指向它：

```json
{
  "kind": "telegram",
  "name": "Personal Telegram",
  "secretRefs": { "botToken": "env:AUTOTO_TELEGRAM_BOT_TOKEN" }
}
```

Telegram 固定连官方 API 端点，只通过 long polling 收更新。在本机界面生成短效配对码，再从私聊发送 `/pair <码>`。可用命令只有 `/status`、`/approve <toolCallId>`（一律是一次性批准）与 `/deny <toolCallId> [原因]`。没有 `/task`，也没有自由对话。token 若可能泄露就轮换它，token 版本号会改变、旧配对会被撤销，之后需要重新配对。

Home Assistant 的端点必须是 loopback、`.local`、link-local 或私有网段。门锁解锁、摄像头截图这类未知或关键动作是硬阻拦，IM 也不能控制设备。

## 授权与安全性

- 安全问题上报方式见 [SECURITY.md](SECURITY.md)
- 开发规范见 [CONTRIBUTING.md](CONTRIBUTING.md)
- 架构说明见 [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)
- 第三方授权见 [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md)
