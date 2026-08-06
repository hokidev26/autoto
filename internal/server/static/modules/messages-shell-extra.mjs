import { currentUILocale } from "./i18n.mjs";

const messages = Object.freeze({
  "zh-CN": Object.freeze({
    shellExtra: Object.freeze({
      app: Object.freeze({
        projectSkillsRequired: "请先选择项目，再查看项目级 Skills。",
        workspaceSkillsRequired: "请先选择工作线，再查看工作区 Skills。",
        sessionId: "会话 ID",
        type: "类型",
        programWorkspace: "程序工作区",
        projectPath: "项目路径",
        projectName: "项目名称",
        workline: "工作线",
        currentModel: "当前模型",
        permissionMode: "权限模式",
        inputTokens: "总输入 Token",
        outputTokens: "总输出 Token",
        cacheTokens: "缓存读取 Token",
        tools: "工具",
        browser: "浏览器",
        pendingApprovals: "待核准数",
        viewRuntime: "查看运行状态与诊断",
        noConversationSelected: "未选择会话",
        selectConversationHint: "请选择左侧会话",
        employeeTeam: "你的 AI 团队",
        scheduleTasks: "排程任务",
        noMatchingSettings: "没有匹配的设置",
        matchingSettingsHint: "换个关键词试试，例如“模型”“终端”“备份”或“技能”。",
      }),
      chat: Object.freeze({
        checkpointNotRecorded: "未记录",
        checkpointRolledBack: "此 Run 已回滚，不能重复执行。",
        checkpointRollingBack: "此 Run 正在回滚，等待状态更新后再操作。",
        checkpointInvalid: "本轮 checkpoint 校验失败，不能安全自动回滚。",
        checkpointCapturing: "工具变更仍在采集，checkpoint 尚不可回滚。",
        checkpointTracking: "本轮仍在跟踪工具变更，完成后才可回滚。",
        checkpointDirtyWorkspace: "本轮开始时工作区不干净或无法读取 Git HEAD，不能自动回滚。",
        checkpointHasCommit: "本轮产生了提交，自动回滚不会跨 commit 执行。",
        checkpointNoSnapshot: "本轮未完成可验证的工具调用归属快照，不能安全自动回滚。",
        checkpointUnknown: "checkpoint 状态未知，已禁用自动回滚。",
        checkpointRestoreHint: "仅恢复可归属到本 Run 工具调用、且之后未变化的文件到 {hash}；不会清理其他未跟踪文件。",
        rollbackRefreshFailed: "已完成回滚，但 Git 状态刷新失败；请稍后手动刷新。",
        rollbackConfirm: "确认回滚到本轮开始前的 Git checkpoint？",
        rollbackSummary: "将恢复 {restoreCount} 个 tracked/staged 路径，并删除 {deleteCount} 个本 Run 新建未跟踪路径。",
        restorePaths: "恢复路径：",
        deletePaths: "删除路径：",
        rollbackTruncated: "仅显示部分路径；服务端会在执行前重新验证全部路径。",
        rollbackSafety: "不会清理其他文件、不会 push，也不会跨 commit 回滚。",
        approvalWarning: "请确认命令安全后再允许。",
        blockedWarning: "该命令被安全策略阻止。",
        awaitingApproval: "此工具调用正在等待审批。",
        conversationExport: "{title} 对话导出",
        exportedAt: "导出时间：{time}",
        emptyMessage: "（空消息）",
        apiKeyOpenAI: "当前 OpenAI 模型尚未配置 API Key。请在启动 Autoto 前设置 `OPENAI_API_KEY`，然后重启服务；或在下方模型菜单选择已配置的模型。",
        apiKeyAnthropic: "当前 Anthropic 模型尚未配置 API Key。请在启动 Autoto 前设置 `ANTHROPIC_API_KEY`，然后重启服务；或在下方模型菜单选择已配置的模型。",
        apiKeyCompatible: "当前 OpenAI-compatible 模型尚未配置 API Key。请设置 `OPENAI_COMPATIBLE_API_KEY` 或 `OPENAI_API_KEY`，并确认 Base URL 后重启服务。",
        cliProxyUnavailable: "无法连接本地 CLIProxyAPI。请先启动 CLIProxyAPI，并确认它监听 `http://127.0.0.1:8317/v1`；如果你改过端口，请设置 `CLIPROXYAPI_BASE_URL` 后重启 Autoto。",
        cliProxyUnauthorized: "CLIProxyAPI 返回 401。请确认 CLIProxyAPI 的 `api-keys` 配置；如启用了客户端鉴权，请在启动 Autoto 前设置 `CLIPROXYAPI_API_KEY`。",
      }),
      terminal: Object.freeze({
        description: "管理当前 AI 代理的交互式 PTY 终端，支持重连、清空、复制输出和控制本地输出保留策略。",
        localPrefsDescription: "只保存在当前浏览器，不影响后端 PTY 会话和项目配置。",
        clearOnReconnect: "重连时清空输出",
        clearOnReconnectDescription: "保持当前默认行为；关闭后重连会追加状态提示并保留旧输出。",
        focusOnConnect: "连接后自动聚焦",
        focusOnConnectDescription: "终端连接成功后自动聚焦输出区，方便直接输入命令。",
        outputRetention: "输出保留行数",
        maxOutputLines: "最多保留 {count} 行终端输出。",
      }),
      terminalExtras: Object.freeze({
        clearedOutput: "终端输出已清空。",
        copiedOutputNotice: "已复制终端输出。",
        copyFailedOutputNotice: "复制终端输出失败。",
        connectingOutput: "正在连接终端…",
        reconnectingOutput: "正在重新连接…",
        unknownError: "未知错误",
        statusText: "终端 {status}",
        status: Object.freeze({ disconnected: "已断开", connected: "已连接", connecting: "连接中", closing: "正在关闭", closed: "已关闭", error: "错误", remoteLocked: "远程策略已锁定" }),
      }),
      workspace: Object.freeze({ saveFailed: "保存失败：{message}" }),
      spec: Object.freeze({ agent: "代理", protected: "受保护", revision: "修订 {revision}", save: "保存", delete: "删除", noTasks: "暂无任务。", goalConfirmation: "目标确认", noConfirmations: "暂无目标确认。", statuses: Object.freeze({ todo: "待办", doing: "进行中", done: "已完成", blocked: "已阻塞" }) }),
    }),
  }),
  "zh-TW": Object.freeze({
    shellExtra: Object.freeze({
      app: Object.freeze({ projectSkillsRequired: "請先選擇專案，再查看專案層級 Skills。", workspaceSkillsRequired: "請先選擇工作線，再查看工作區 Skills。", sessionId: "對話 ID", type: "類型", programWorkspace: "程式工作區", projectPath: "專案路徑", projectName: "專案名稱", workline: "工作線", currentModel: "目前模型", permissionMode: "權限模式", inputTokens: "總輸入 Token", outputTokens: "總輸出 Token", cacheTokens: "快取讀取 Token", tools: "工具", browser: "瀏覽器", pendingApprovals: "待核准數", viewRuntime: "查看執行狀態與診斷", noConversationSelected: "尚未選擇對話", selectConversationHint: "請選擇左側對話", employeeTeam: "你的 AI 團隊", scheduleTasks: "排程任務", noMatchingSettings: "沒有符合的設定", matchingSettingsHint: "換個關鍵字試試，例如「模型」「終端機」「備份」或「技能」。" }),
      chat: Object.freeze({ checkpointNotRecorded: "未記錄", checkpointRolledBack: "此 Run 已回復，不能重複執行。", checkpointRollingBack: "此 Run 正在回復，請等待狀態更新後再操作。", checkpointInvalid: "本輪 checkpoint 驗證失敗，不能安全自動回復。", checkpointCapturing: "工具變更仍在收集，checkpoint 尚不可回復。", checkpointTracking: "本輪仍在追蹤工具變更，完成後才可回復。", checkpointDirtyWorkspace: "本輪開始時工作區不乾淨或無法讀取 Git HEAD，不能自動回復。", checkpointHasCommit: "本輪產生了提交，自動回復不會跨 commit 執行。", checkpointNoSnapshot: "本輪未完成可驗證的工具呼叫歸屬快照，不能安全自動回復。", checkpointUnknown: "checkpoint 狀態未知，已停用自動回復。", checkpointRestoreHint: "僅回復可歸屬到本 Run 工具呼叫、且之後未變更的檔案到 {hash}；不會清理其他未追蹤檔案。", rollbackRefreshFailed: "已完成回復，但 Git 狀態重新整理失敗；請稍後手動重新整理。", rollbackConfirm: "確認回復到本輪開始前的 Git checkpoint？", rollbackSummary: "將回復 {restoreCount} 個 tracked/staged 路徑，並刪除 {deleteCount} 個本 Run 新建未追蹤路徑。", restorePaths: "回復路徑：", deletePaths: "刪除路徑：", rollbackTruncated: "僅顯示部分路徑；伺服器會在執行前重新驗證全部路徑。", rollbackSafety: "不會清理其他檔案、不會 push，也不會跨 commit 回復。", approvalWarning: "請確認命令安全後再允許。", blockedWarning: "該命令已被安全策略阻止。", awaitingApproval: "此工具呼叫正在等待核准。", conversationExport: "{title} 對話匯出", exportedAt: "匯出時間：{time}", emptyMessage: "（空訊息）", apiKeyOpenAI: "目前 OpenAI 模型尚未設定 API Key。請在啟動 Autoto 前設定 `OPENAI_API_KEY`，然後重新啟動服務；或在下方模型選單選擇已設定的模型。", apiKeyAnthropic: "目前 Anthropic 模型尚未設定 API Key。請在啟動 Autoto 前設定 `ANTHROPIC_API_KEY`，然後重新啟動服務；或在下方模型選單選擇已設定的模型。", apiKeyCompatible: "目前 OpenAI-compatible 模型尚未設定 API Key。請設定 `OPENAI_COMPATIBLE_API_KEY` 或 `OPENAI_API_KEY`，並確認 Base URL 後重新啟動服務。", cliProxyUnavailable: "無法連線本機 CLIProxyAPI。請先啟動 CLIProxyAPI，並確認它監聽 `http://127.0.0.1:8317/v1`；如果你改過連接埠，請設定 `CLIPROXYAPI_BASE_URL` 後重新啟動 Autoto。", cliProxyUnauthorized: "CLIProxyAPI 回傳 401。請確認 CLIProxyAPI 的 `api-keys` 設定；如啟用了用戶端驗證，請在啟動 Autoto 前設定 `CLIPROXYAPI_API_KEY`。" }),
      terminal: Object.freeze({ description: "管理目前 AI 代理的互動式 PTY 終端機，支援重新連線、清空、複製輸出和控制本機輸出保留策略。", localPrefsDescription: "只儲存在目前瀏覽器，不影響後端 PTY 對話和專案設定。", clearOnReconnect: "重新連線時清空輸出", clearOnReconnectDescription: "維持目前預設行為；關閉後重新連線會追加狀態提示並保留舊輸出。", focusOnConnect: "連線後自動聚焦", focusOnConnectDescription: "終端機連線成功後自動聚焦輸出區，方便直接輸入命令。", outputRetention: "輸出保留行數", maxOutputLines: "最多保留 {count} 行終端機輸出。" }),
      terminalExtras: Object.freeze({ clearedOutput: "終端機輸出已清空。", copiedOutputNotice: "已複製終端機輸出。", copyFailedOutputNotice: "複製終端機輸出失敗。", connectingOutput: "正在連線終端機…", reconnectingOutput: "正在重新連線終端機…", unknownError: "未知錯誤", statusText: "終端機 {status}", status: Object.freeze({ disconnected: "已中斷連線", connected: "已連線", connecting: "連線中", closing: "正在關閉", closed: "已關閉", error: "錯誤", remoteLocked: "遠端策略已鎖定" }) }),
      workspace: Object.freeze({ saveFailed: "儲存失敗：{message}" }),
      spec: Object.freeze({ agent: "代理", protected: "受保護", revision: "修訂 {revision}", save: "儲存", delete: "刪除", noTasks: "暫無任務。", goalConfirmation: "目標確認", noConfirmations: "暫無目標確認。", statuses: Object.freeze({ todo: "待辦", doing: "進行中", done: "已完成", blocked: "已阻塞" }) }),
    }),
  }),
  en: Object.freeze({
    shellExtra: Object.freeze({
      app: Object.freeze({ projectSkillsRequired: "Select a project before viewing project Skills.", workspaceSkillsRequired: "Select a workline before viewing workspace Skills.", sessionId: "Conversation ID", type: "Type", programWorkspace: "Program workspace", projectPath: "Project path", projectName: "Project name", workline: "Workline", currentModel: "Current model", permissionMode: "Permission mode", inputTokens: "Input tokens", outputTokens: "Output tokens", cacheTokens: "Cache-read tokens", tools: "Tools", browser: "Browser", pendingApprovals: "Pending approvals", viewRuntime: "View runtime status and diagnostics", noConversationSelected: "No conversation selected", selectConversationHint: "Select a conversation from the left", employeeTeam: "Your AI team", scheduleTasks: "Scheduled tasks", noMatchingSettings: "No matching settings", matchingSettingsHint: "Try another keyword, such as model, terminal, backup, or skill." }),
      chat: Object.freeze({ checkpointNotRecorded: "Not recorded", checkpointRolledBack: "This run was rolled back and cannot be run again.", checkpointRollingBack: "This run is being rolled back. Wait for the status to update before trying again.", checkpointInvalid: "This run's checkpoint validation failed, so it cannot be safely rolled back automatically.", checkpointCapturing: "Tool changes are still being collected; the checkpoint cannot be rolled back yet.", checkpointTracking: "This run is still tracking tool changes and can be rolled back when complete.", checkpointDirtyWorkspace: "The workspace was dirty at the start of this run, or Git HEAD could not be read, so it cannot be rolled back automatically.", checkpointHasCommit: "This run created a commit; automatic rollback does not cross commits.", checkpointNoSnapshot: "This run did not produce a verifiable tool-call ownership snapshot, so it cannot be safely rolled back automatically.", checkpointUnknown: "The checkpoint state is unknown; automatic rollback is disabled.", checkpointRestoreHint: "Only files attributable to this run's tool calls and unchanged since then will be restored to {hash}; other untracked files will not be removed.", rollbackRefreshFailed: "Rollback completed, but Git status could not be refreshed. Refresh it manually later.", rollbackConfirm: "Roll back to the Git checkpoint from before this run started?", rollbackSummary: "This will restore {restoreCount} tracked/staged paths and delete {deleteCount} untracked paths created by this run.", restorePaths: "Restore paths:", deletePaths: "Delete paths:", rollbackTruncated: "Only some paths are shown; the server will revalidate every path before executing.", rollbackSafety: "Other files will not be cleaned, nothing will be pushed, and no rollback will cross a commit.", approvalWarning: "Confirm that the command is safe before allowing it.", blockedWarning: "This command was blocked by the security policy.", awaitingApproval: "This tool call is awaiting approval.", conversationExport: "{title} conversation export", exportedAt: "Exported at: {time}", emptyMessage: "(empty message)", apiKeyOpenAI: "The current OpenAI model has no API key configured. Set `OPENAI_API_KEY` before starting Autoto, then restart the service; or select a configured model below.", apiKeyAnthropic: "The current Anthropic model has no API key configured. Set `ANTHROPIC_API_KEY` before starting Autoto, then restart the service; or select a configured model below.", apiKeyCompatible: "The current OpenAI-compatible model has no API key configured. Set `OPENAI_COMPATIBLE_API_KEY` or `OPENAI_API_KEY`, confirm the Base URL, then restart Autoto.", cliProxyUnavailable: "Unable to connect to local CLIProxyAPI. Start CLIProxyAPI and confirm it listens on `http://127.0.0.1:8317/v1`; if you changed the port, set `CLIPROXYAPI_BASE_URL` and restart Autoto.", cliProxyUnauthorized: "CLIProxyAPI returned 401. Check its `api-keys` configuration; if client authentication is enabled, set `CLIPROXYAPI_API_KEY` before starting Autoto." }),
      terminal: Object.freeze({ description: "Manage the current AI agent's interactive PTY terminal. Reconnect, clear or copy output, and control local retention preferences.", localPrefsDescription: "Stored only in this browser; this does not affect the backend PTY session or project configuration.", clearOnReconnect: "Clear output on reconnect", clearOnReconnectDescription: "Keep the default behavior. When off, reconnecting appends a status notice and preserves prior output.", focusOnConnect: "Focus on connect", focusOnConnectDescription: "Focus the output area after the terminal connects so you can type commands immediately.", outputRetention: "Output retention", maxOutputLines: "Keep up to {count} terminal output lines." }),
      terminalExtras: Object.freeze({ clearedOutput: "Terminal output cleared.", copiedOutputNotice: "Terminal output copied.", copyFailedOutputNotice: "Failed to copy terminal output.", connectingOutput: "Connecting terminal…", reconnectingOutput: "Reconnecting terminal…", unknownError: "unknown error", statusText: "Terminal {status}", status: Object.freeze({ disconnected: "disconnected", connected: "connected", connecting: "connecting", closing: "closing", closed: "closed", error: "error", remoteLocked: "locked by remote policy" }) }),
      workspace: Object.freeze({ saveFailed: "Save failed: {message}" }),
      spec: Object.freeze({ agent: "Agent", protected: "protected", revision: "rev {revision}", save: "Save", delete: "Delete", noTasks: "No tasks yet.", goalConfirmation: "Goal confirmation", noConfirmations: "No goal confirmations yet.", statuses: Object.freeze({ todo: "To do", doing: "In progress", done: "Done", blocked: "Blocked" }) }),
    }),
  }),
});

function lookup(catalog, key) {
  return String(key || "").split(".").reduce((value, part) => value && typeof value === "object" ? value[part] : undefined, catalog);
}

export function shellExtraT(key, params = {}, locale = currentUILocale()) {
  const value = lookup(messages[locale], `shellExtra.${key}`) ?? lookup(messages["zh-CN"], `shellExtra.${key}`) ?? key;
  return String(value).replace(/\{([A-Za-z0-9_]+)\}/g, (match, name) => Object.prototype.hasOwnProperty.call(params, name) ? String(params[name] ?? "") : match);
}

export default messages;
