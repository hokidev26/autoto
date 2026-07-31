# 从源码构建 Autoto

面向在本机改代码、需要重新编译验证的场景。如果你只是想跑一个现成的
可执行文件，看 [WINDOWS_RUN.md](WINDOWS_RUN.md)。

## 只保留一个可执行文件

**规则：构建产物永远只留一个 `codeharbor/autoto.exe`，旧的直接覆盖。**

这条规则是有代价换来的。此前根目录和 `codeharbor/` 累积过 11 个 exe
（约 590 MB），文件名分别是 `autoto-next`、`autoto-reasoning`、
`autoto-web.next`、`autoto.preview`、`autoto.minimal-preview`、
`autoto.new` 等等。问题不在磁盘占用，而在于**没人能确定哪个是最新的**：

- 曾经出现过修复已经编译进 `autoto.new.exe`，但用户双击的是旧的
  `autoto.exe`，于是「修好的 bug 看起来没修好」，白白多花一轮排查。
- `-o autoto.new.exe` 这类临时命名会绕过覆盖，让旧产物无声留下。

所以：**构建时不要另起文件名。** 需要保留旧版本时用 git，不要用文件名。

`*.exe` 和 `*.exe~` 已在 `.gitignore` 中，不会误提交。

## Go 工具链位置

这台机器上 Go **不在 PATH 里**：

```text
C:\Users\Ray\go-sdk\go\bin\go.exe
```

Git Bash 里每个会话先加进 PATH：

```bash
export PATH="$PATH:/c/Users/Ray/go-sdk/go/bin"
go version   # 应输出 go1.26.5 windows/amd64
```

直接敲 `go build` 而没设 PATH 会得到
`bash: go: command not found`。注意用 `&&` 而不是 `;` 串联命令，否则
go 失败了后面的 `echo "BUILD OK"` 照样会打印，得到假的成功信息。

## 构建

在 `codeharbor/` 目录下执行。

### 服务器 / CLI 版（平时用这个）

```bash
export PATH="$PATH:/c/Users/Ray/go-sdk/go/bin"
go build -o autoto.exe ./cmd/autoto
```

覆盖同名文件即可，不要输出成别的名字。

### 桌面窗口版

需要 `desktop` 构建标签和本机 WebView 工具链：

```bash
go build -tags desktop -o autoto-desktop.exe ./cmd/autoto-desktop
```

发布版（去掉 Wails devtools）：

```bash
VERSION=dev make build-desktop-release
```

### Makefile 快捷方式

`Makefile` 里的目标输出的是无扩展名文件（为 Unix 准备），Windows 上
直接用上面的 `go build -o autoto.exe` 更省事。

| 目标 | 作用 |
|------|------|
| `make check` | 跑 `scripts/check.sh`（格式 + vet + 测试） |
| `make check-desktop` | 同上，额外包含 Wails 桌面包 |
| `make fmt` | `gofmt -w ./cmd ./internal` |
| `make build-cli` | 构建 CLI 版 |
| `make build-desktop` | 构建桌面版 |
| `make release-desktop` | 产出 `dist/` + SHA256SUMS |

## 替换正在运行的 exe

Windows 会锁定正在执行的文件，**必须先停掉进程**，否则改名会失败：

```powershell
# 1. 关掉 Autoto（关闭窗口，或结束 autoto.exe 进程）
# 2. 确认没有残留
Get-Process -Name autoto -ErrorAction SilentlyContinue
# 3. 确认端口已释放（16888 = 管理 UI，网关端口见设置页）
Get-NetTCPConnection -State Listen -LocalPort 16888 -ErrorAction SilentlyContinue
```

两个都没输出才是干净的，然后再构建。

## 验证构建产物

```bash
./autoto.exe --help
```

应列出：

```text
-config string      path to config.json
-no-browser         do not open the web UI in a browser on startup
```

没有 `--version` 参数。想确认拿到的是不是刚编的那个，比时间戳最可靠：

```bash
ls -la autoto.exe
```

## 跑测试

```bash
export PATH="$PATH:/c/Users/Ray/go-sdk/go/bin"
go build ./...                          # 全量编译
go test ./internal/providers/...        # 单个包
go test ./internal/...                  # 全部（internal/db 较慢，约 3 分钟）
```

改动网关或供应商相关代码时，至少覆盖：

```bash
go test ./internal/providers/... ./internal/gateway/... ./internal/db/...
```

工作区里可能有他人未提交的改动，导致测试失败与你无关。判断方法是只读
对比，不要建 worktree：

```bash
git diff --stat HEAD -- <你改过的文件>       # 确认改动范围
git show HEAD:<路径> | grep -c '<关键代码>'  # HEAD 的行为
grep -c '<关键代码>' <路径>                  # 工作区的行为
```

两者不一致，说明该文件正被别人改动中，失败大概率不是你引入的。

## 开启 debug 日志

程序**不会自动写日志文件**（`~/.autoto/*.log` 是历史遗留，不再更新）。
日志走标准输出，**双击 exe 启动看不到任何日志**。

要看日志必须从终端启动：

```powershell
$env:AUTOTO_LOG_LEVEL = "debug"
& "C:\Users\Ray\Desktop\autoto\codeharbor\autoto.exe"
```

PowerShell 里两条命令要分行，写在同一行会报
`参考运算子後面遺漏屬性名稱`。

可用级别：`debug` / `info`（默认）/ `warn` / `error`。

环境变量只在当前终端会话有效，关掉窗口就失效，下次双击启动不带 debug。

### 什么时候需要 debug

网关的 `handleModels` 在供应商不可用时是**静默跳过**的：

```go
if err != nil {
    continue   // 不写日志，不报错
}
```

所以「模型莫名从 `/v1/models` 消失」这类问题，只有 debug 日志能定位。
开启后请求一次 `/v1/models`，会看到每个供应商两行：

```text
level=DEBUG msg="gateway handleModels provider check" provider=gemini-oauth permitted=true
level=DEBUG msg="gateway handleModels ListModels" provider=gemini-oauth models="[...]" err=<nil>
```

`permitted=false` 指向权限或账号授权问题；`permitted=true` 但 `err`
非空，指向该供应商的 `ListModels` 调用失败。

## 常见问题

**`bash: go: command not found`**
没设 PATH，见上面「Go 工具链位置」。

**`Access is denied` / 改名失败**
exe 正在运行，先停进程。

**`package command-line-arguments: no Go files in ...`**
Go 会忽略以 `_` 或 `.` 开头的文件。临时脚本不要命名成
`_tmp_check.go`，用 `cmd/<name>/main.go` 或不带下划线前缀的名字。

**`go.mod file not found`**
在 `codeharbor/` 目录外执行了 go 命令。

**测试失败但代码没问题**
见上面「跑测试」里的只读对比方法。
