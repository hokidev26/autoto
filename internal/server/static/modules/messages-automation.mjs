import { currentUILocale, t as baseT } from "./i18n.mjs";

export const automationMessages = Object.freeze({
  "zh-CN": {
    automation: {
      validation: {
        envReference: "{label}只能填写 env:变量名 引用，禁止输入 token 或 secret 明文",
        scheduleExpressionRequired: "Cron 表达式不能为空",
        agentIdRequired: "Agent ID 不能为空",
        schedulePromptRequired: "排程任务内容不能为空",
        unsupportedConnection: "只支持 Telegram 或 Home Assistant 连接",
        homeAssistantUrl: "Home Assistant URL 必须使用 http:// 或 https://",
        pairingCodeRequired: "生成配对码需要 connectionId 与 agentId",
        deviceParametersJson: "设备动作参数必须是有效 JSON 对象",
        deviceParametersObject: "设备动作参数必须是 JSON 对象",
        deviceActionRequired: "设备动作需要连接、实体、domain 与 service",
        requestFailed: "请求失败",
        actionExpired: "设备动作请求已过期，不能批准",
        requestFunction: "自动化控制请求必须是函数",
      },
      defaults: {
        credential: "凭据",
        telegram: "Telegram",
        homeAssistant: "Home Assistant",
        unnamedSchedule: "未命名排程",
        schedule: "排程",
        cronUnconfigured: "未配置 Cron",
        taskMissing: "未提供任务内容",
        unknown: "未知",
        unknownChannel: "未知渠道",
        unknownSchedule: "未知排程",
        unknownPairing: "未知配对",
        unknownEntity: "未知实体",
        notification: "通知",
        deviceAction: "设备动作",
        event: "事件",
        system: "system",
        manual: "manual",
      },
      status: {
        active: "有效", approved: "已批准", connected: "已连接", delivered: "已投递", disabled: "已停用", denied: "已拒绝", enabled: "已启用", expired: "已过期", failed: "失败", healthy: "正常", pending: "待本地审批", pending_approval: "待本地审批", queued: "排队中", inflight: "投递中", retry_wait: "等待重试", dead: "重试耗尽", executing: "执行中", ready: "就绪", revoked: "已撤销", running: "运行中", succeeded: "成功", success: "成功", unknown: "未知",
      },
      risk: { critical: "极高风险", high: "高风险", medium: "中风险", low: "低风险", blocked: "已阻止" },
      timestamp: { empty: "—", invalid: "无效日期" },
      section: { loading: "正在加载…", noData: "暂无数据", loadingHistory: "正在加载运行历史…", noHistory: "暂无运行历史。", selectHistory: "请选择排程查看历史。" },
      buttons: { refreshAll: "刷新全部", refreshing: "刷新中…", history: "历史", loading: "加载中…", disable: "停用", enable: "启用", runNow: "立即运行", delete: "删除", returnSchedules: "返回排程", testConnection: "测试连接", retry: "重试", revoke: "撤销", approveLocal: "本地双确认批准", deny: "拒绝", createSchedule: "创建排程", creating: "创建中…", createTelegram: "创建 Telegram 连接", createHomeAssistant: "创建 Home Assistant 连接", createPairingCode: "生成配对码", requestLocal: "本地双确认并请求" },
      confirm: {
        dangerousFirst: "确认从本地控制台发起此危险操作？",
        dangerousSecond: "最终确认：此操作可能改变真实设备状态，仍要继续？",
        deleteSchedule: "确定删除此排程？此操作不可撤销。",
        deleteConnection: "删除连接会停止其通知、配对或设备访问。确定继续？",
        revokePairing: "确定撤销此渠道配对？撤销后该会话不能再审批。",
        denyDeviceAction: "确定拒绝此设备动作请求？",
        requestDeviceActionFirst: "设备动作只能从本地 Web UI 发起，IM 无法触发。确认请求 {entityId} → {action}？",
        requestDeviceActionSecond: "最终确认：此动作可能改变真实设备状态。请核对实体、service 和 input 后再继续。",
        approveDeviceActionFirst: "仅本地 Web UI 可批准。确认批准 {entityId} → {action}？",
        approveDeviceActionSecond: "最终确认：风险等级为 {risk}，过期时间 {expiresAt}。仍要执行？",
      },
      toast: { scheduleCreated: "排程已创建。", scheduleEnabled: "排程已启用。", scheduleDisabled: "排程已停用。", scheduleRunRequested: "已请求立即运行排程。", scheduleDeleted: "排程已删除。", deliveryRetry: "通知已加入重试。", connectionCreated: "{kind} 连接已创建；secret 未回显。", connectionEnabled: "连接已启用。", connectionDisabled: "连接已停用。", connectionTested: "连接测试已完成。", connectionDeleted: "连接已删除。", pairingCodeCreated: "一次性配对码已生成。", pairingRevoked: "渠道配对已撤销。", deviceActionRequested: "设备动作请求已提交，等待本地审批或执行结果。", deviceActionApproved: "设备动作已在本地批准。", deviceActionDenied: "设备动作已拒绝。" },
      activity: { error: "{section}：{message}", refreshed: "已刷新渠道、排程、设备、通知、监控与审计数据" },
      hero: { kicker: "P2–P3 管理控制台", title: "渠道、排程与家电", description: "运行状态只以后端 API 为准。Telegram 仅接收环境变量引用；危险设备动作只能在本地 Web UI 双确认，IM 永远不能触发。", safetyTitle: "安全边界", safetyDescription: "排程仅允许 readOnly / acceptEdits；凭据使用 env: 引用；设备动作需在本地双确认。", migrationTitle: "旧草稿已停用", migration: "检测到 {key} 中的旧 IM 配置{channel}。它只用于迁移提示，不会启动渠道、排程或设备，也绝不计入运行状态。", migrationChannel: "（{channel}）" },
      metrics: { ariaLabel: "自动化监控快照", activeRuns: "活跃运行", pendingApprovals: "待审批", schedules: "排程", notifications: "通知", channels: "渠道", devices: "设备" },
      schedule: {
        kicker: "排程", title: "受限权限后台任务", description: "Cron 到点或手动立即运行；危险的 bypassPermissions 不在此提供。", empty: "暂无排程，先创建一个受限权限任务。", name: "名称", namePlaceholder: "每晚测试", preset: "常用 Preset", custom: "自定义", expression: "Cron 表达式", expressionPlaceholder: "@every 15m", agentId: "Agent ID", agentIdPlaceholder: "agent-id", timezone: "时区", timezonePlaceholder: "Asia/Shanghai", permission: "权限", environment: "环境", narrator: "叙述者", prompt: "任务内容", promptPlaceholder: "运行测试并汇总失败；不要修改文件。", nextRun: "下次运行", permissions: "权限", environments: "环境", narratorLabel: "叙述者", history: "运行历史", runMs: "{duration} ms", permissionModes: { readOnly: "只读（推荐）", acceptEdits: "允许修改文件" }, environmentModes: { workline: "关联项目工作区", standalone: "独立工作区" }, narratorModes: { reuse: "复用关联叙述者", new: "每次运行新建叙述者" }, unknownValue: "未知选项（{value}）"
      },
      deliveries: { kicker: "通知", title: "投递历史", description: "失败可见、原因脱敏，并可显式重试。", empty: "暂无通知投递记录。", attempts: "尝试 {count} 次" },
      connections: { kicker: "Telegram", title: "渠道连接", description: "只填写 env: 引用；页面不保存也不回显 token 明文。", name: "名称", namePlaceholder: "个人 Telegram", credential: "Bot token 环境变量引用", credentialPlaceholder: "env:AUTOTO_TELEGRAM_BOT_TOKEN", empty: "暂无真实连接；旧 localStorage 草稿不会被视为运行中。", emptyTelegram: "暂无 Telegram 连接。", credentialConfigured: "环境变量凭据：已配置（引用目标与 secret 均不回显）", credentialMissing: "环境变量凭据：未配置" },
      pairing: { kicker: "配对", title: "一次性配对码", description: "未配对 chat 不应获得响应；撤销后立即失效。", connection: "Telegram 连接", selectConnection: "选择连接", agentId: "Agent ID", agentIdPlaceholder: "agent-id", code: "一次性配对码", expiresAt: "过期：{timestamp}", empty: "暂无已配对会话。", agentAt: "Agent {agentId} · {timestamp}" },
      homeAssistant: { kicker: "Home Assistant", title: "家电连接与只读实体", description: "实体列表只读展示；Access token 仅允许 env: 引用。", name: "名称", url: "Base URL", urlPlaceholder: "http://homeassistant.local:8123", credential: "Access token 环境变量引用", credentialPlaceholder: "env:AUTOTO_HOME_ASSISTANT_TOKEN", empty: "暂无 Home Assistant 连接。", viewConnection: "查看连接", selectConnection: "选择 Home Assistant 连接", devicesLimit: "最多显示 {count} 个实体，DOM 有界。", noDevices: "该连接没有可读实体。", createConnectionFirst: "先创建 Home Assistant 连接。", readonly: "只读" },
      deviceActions: { kicker: "真实世界动作", title: "设备动作请求与本地审批", description: "无法从 IM 发起。提交和危险批准均需本地双确认；过期请求不能批准。", riskBoundary: "高风险边界", connection: "Home Assistant 连接", selectConnection: "选择连接", entityId: "实体 ID", entityPlaceholder: "light.living_room", service: "Service", servicePlaceholder: "turn_off", input: "附加 input JSON", inputPlaceholder: "{\"brightness\": 0}", empty: "暂无设备动作请求。", risk: "风险", expires: "过期" },
      audit: { kicker: "审计", title: "最近 50 条事件", description: "动态内容转义、敏感片段脱敏；不会无界追加日志。", empty: "暂无审计事件。", activity: "本页操作记录（最近 {count} 条）" },
    },
  },
  "zh-TW": {
    automation: {
      validation: {
        envReference: "{label}只能填寫 env:變數名稱引用，禁止輸入 token 或 secret 明文",
        scheduleExpressionRequired: "Cron 表達式不可為空",
        agentIdRequired: "Agent ID 不可為空",
        schedulePromptRequired: "排程任務內容不可為空",
        unsupportedConnection: "僅支援 Telegram 或 Home Assistant 連線",
        homeAssistantUrl: "Home Assistant URL 必須使用 http:// 或 https://",
        pairingCodeRequired: "產生配對碼需要 connectionId 與 agentId",
        deviceParametersJson: "裝置動作參數必須是有效的 JSON 物件",
        deviceParametersObject: "裝置動作參數必須是 JSON 物件",
        deviceActionRequired: "裝置動作需要連線、實體、domain 與 service",
        requestFailed: "請求失敗",
        actionExpired: "裝置動作請求已過期，無法批准",
        requestFunction: "自動化控制請求必須是函式",
      },
      defaults: { credential: "憑證", telegram: "Telegram", homeAssistant: "Home Assistant", unnamedSchedule: "未命名排程", schedule: "排程", cronUnconfigured: "未設定 Cron", taskMissing: "未提供任務內容", unknown: "未知", unknownChannel: "未知渠道", unknownSchedule: "未知排程", unknownPairing: "未知配對", unknownEntity: "未知實體", notification: "通知", deviceAction: "裝置動作", event: "事件", system: "system", manual: "manual" },
      status: { active: "有效", approved: "已批准", connected: "已連線", delivered: "已投遞", disabled: "已停用", denied: "已拒絕", enabled: "已啟用", expired: "已過期", failed: "失敗", healthy: "正常", pending: "待本機審批", pending_approval: "待本機審批", queued: "排隊中", inflight: "投遞中", retry_wait: "等待重試", dead: "重試耗盡", executing: "執行中", ready: "就緒", revoked: "已撤銷", running: "執行中", succeeded: "成功", success: "成功", unknown: "未知" },
      risk: { critical: "極高風險", high: "高風險", medium: "中風險", low: "低風險", blocked: "已阻止" },
      timestamp: { empty: "—", invalid: "無效日期" },
      section: { loading: "正在載入…", noData: "暫無資料", loadingHistory: "正在載入執行歷史…", noHistory: "暫無執行歷史。", selectHistory: "請選擇排程查看歷史。" },
      buttons: { refreshAll: "重新整理全部", refreshing: "重新整理中…", history: "歷史", loading: "載入中…", disable: "停用", enable: "啟用", runNow: "立即執行", delete: "刪除", returnSchedules: "返回排程", testConnection: "測試連線", retry: "重試", revoke: "撤銷", approveLocal: "本機雙重確認批准", deny: "拒絕", createSchedule: "建立排程", creating: "建立中…", createTelegram: "建立 Telegram 連線", createHomeAssistant: "建立 Home Assistant 連線", createPairingCode: "產生配對碼", requestLocal: "本機雙重確認並請求" },
      confirm: { dangerousFirst: "確認從本機控制台發起此危險操作？", dangerousSecond: "最終確認：此操作可能變更真實裝置狀態，仍要繼續？", deleteSchedule: "確定刪除此排程？此操作無法復原。", deleteConnection: "刪除連線會停止其通知、配對或裝置存取。確定繼續？", revokePairing: "確定撤銷此渠道配對？撤銷後該工作階段不能再審批。", denyDeviceAction: "確定拒絕此裝置動作請求？", requestDeviceActionFirst: "裝置動作僅能從本機 Web UI 發起，IM 無法觸發。確認請求 {entityId} → {action}？", requestDeviceActionSecond: "最終確認：此動作可能變更真實裝置狀態。請核對實體、service 和 input 後再繼續。", approveDeviceActionFirst: "僅本機 Web UI 可批准。確認批准 {entityId} → {action}？", approveDeviceActionSecond: "最終確認：風險等級為 {risk}，過期時間 {expiresAt}。仍要執行？" },
      toast: { scheduleCreated: "排程已建立。", scheduleEnabled: "排程已啟用。", scheduleDisabled: "排程已停用。", scheduleRunRequested: "已請求立即執行排程。", scheduleDeleted: "排程已刪除。", deliveryRetry: "通知已加入重試。", connectionCreated: "{kind} 連線已建立；secret 未回顯。", connectionEnabled: "連線已啟用。", connectionDisabled: "連線已停用。", connectionTested: "連線測試已完成。", connectionDeleted: "連線已刪除。", pairingCodeCreated: "一次性配對碼已產生。", pairingRevoked: "渠道配對已撤銷。", deviceActionRequested: "裝置動作請求已提交，等待本機審批或執行結果。", deviceActionApproved: "裝置動作已在本機批准。", deviceActionDenied: "裝置動作已拒絕。" },
      activity: { error: "{section}：{message}", refreshed: "已重新整理渠道、排程、裝置、通知、監控與稽核資料" },
      hero: { kicker: "P2–P3 管理控制台", title: "渠道、排程與家電", description: "執行狀態僅以後端 API 為準。Telegram 僅接收環境變數引用；危險裝置動作只能在本機 Web UI 雙重確認，IM 永遠不能觸發。", safetyTitle: "安全邊界", safetyDescription: "排程僅允許 readOnly / acceptEdits；憑證使用 env: 引用；裝置動作需在本機雙重確認。", migrationTitle: "舊草稿已停用", migration: "偵測到 {key} 中的舊 IM 設定{channel}。它僅用於遷移提示，不會啟動渠道、排程或裝置，也絕不計入執行狀態。", migrationChannel: "（{channel}）" },
      metrics: { ariaLabel: "自動化監控快照", activeRuns: "活躍執行", pendingApprovals: "待審批", schedules: "排程", notifications: "通知", channels: "渠道", devices: "裝置" },
      schedule: { kicker: "排程", title: "受限權限背景任務", description: "Cron 到點或手動立即執行；此處不提供危險的 bypassPermissions。", empty: "暫無排程，先建立一個受限權限任務。", name: "名稱", namePlaceholder: "每晚測試", preset: "常用 Preset", custom: "自訂", expression: "Cron 表達式", expressionPlaceholder: "@every 15m", agentId: "Agent ID", agentIdPlaceholder: "agent-id", timezone: "時區", timezonePlaceholder: "Asia/Shanghai", permission: "權限", environment: "環境", narrator: "敘述者", prompt: "任務內容", promptPlaceholder: "執行測試並彙總失敗；不要修改檔案。", nextRun: "下次執行", permissions: "權限", environments: "環境", narratorLabel: "敘述者", history: "執行歷史", runMs: "{duration} ms", permissionModes: { readOnly: "唯讀（建議）", acceptEdits: "允許修改檔案" }, environmentModes: { workline: "關聯專案工作區", standalone: "獨立工作區" }, narratorModes: { reuse: "重用關聯敘述者", new: "每次執行新建敘述者" }, unknownValue: "未知選項（{value}）" },
      deliveries: { kicker: "通知", title: "投遞歷史", description: "失敗可見、原因脫敏，並可明確重試。", empty: "暫無通知投遞記錄。", attempts: "嘗試 {count} 次" },
      connections: { kicker: "Telegram", title: "渠道連線", description: "僅填寫 env: 引用；頁面不儲存也不回顯 token 明文。", name: "名稱", namePlaceholder: "個人 Telegram", credential: "Bot token 環境變數引用", credentialPlaceholder: "env:AUTOTO_TELEGRAM_BOT_TOKEN", empty: "暫無真實連線；舊 localStorage 草稿不會被視為執行中。", emptyTelegram: "暫無 Telegram 連線。", credentialConfigured: "環境變數憑證：已設定（引用目標與 secret 均不回顯）", credentialMissing: "環境變數憑證：未設定" },
      pairing: { kicker: "配對", title: "一次性配對碼", description: "未配對 chat 不應獲得回應；撤銷後立即失效。", connection: "Telegram 連線", selectConnection: "選擇連線", agentId: "Agent ID", agentIdPlaceholder: "agent-id", code: "一次性配對碼", expiresAt: "過期：{timestamp}", empty: "暫無已配對工作階段。", agentAt: "Agent {agentId} · {timestamp}" },
      homeAssistant: { kicker: "Home Assistant", title: "家電連線與唯讀實體", description: "實體清單僅供唯讀展示；Access token 僅允許 env: 引用。", name: "名稱", url: "Base URL", urlPlaceholder: "http://homeassistant.local:8123", credential: "Access token 環境變數引用", credentialPlaceholder: "env:AUTOTO_HOME_ASSISTANT_TOKEN", empty: "暫無 Home Assistant 連線。", viewConnection: "查看連線", selectConnection: "選擇 Home Assistant 連線", devicesLimit: "最多顯示 {count} 個實體，DOM 有界。", noDevices: "此連線沒有可讀實體。", createConnectionFirst: "請先建立 Home Assistant 連線。", readonly: "唯讀" },
      deviceActions: { kicker: "真實世界動作", title: "裝置動作請求與本機審批", description: "無法從 IM 發起。提交和危險批准均需本機雙重確認；過期請求不能批准。", riskBoundary: "高風險邊界", connection: "Home Assistant 連線", selectConnection: "選擇連線", entityId: "實體 ID", entityPlaceholder: "light.living_room", service: "Service", servicePlaceholder: "turn_off", input: "附加 input JSON", inputPlaceholder: "{\"brightness\": 0}", empty: "暫無裝置動作請求。", risk: "風險", expires: "過期" },
      audit: { kicker: "稽核", title: "最近 50 條事件", description: "動態內容已逸出、敏感片段已脫敏；不會無界追加日誌。", empty: "暫無稽核事件。", activity: "本頁操作記錄（最近 {count} 條）" },
    },
  },
  en: {
    automation: {
      validation: {
        envReference: "{label} must use an env:VARIABLE_NAME reference; plaintext tokens and secrets are not allowed",
        scheduleExpressionRequired: "Cron expression is required",
        agentIdRequired: "Agent ID is required",
        schedulePromptRequired: "Scheduled task content is required",
        unsupportedConnection: "Only Telegram and Home Assistant connections are supported",
        homeAssistantUrl: "Home Assistant URL must use http:// or https://",
        pairingCodeRequired: "connectionId and agentId are required to generate a pairing code",
        deviceParametersJson: "Device action parameters must be a valid JSON object",
        deviceParametersObject: "Device action parameters must be a JSON object",
        deviceActionRequired: "A device action requires a connection, entity, domain, and service",
        requestFailed: "Request failed",
        actionExpired: "The device action request has expired and cannot be approved",
        requestFunction: "Automation control request must be a function",
      },
      defaults: { credential: "Credential", telegram: "Telegram", homeAssistant: "Home Assistant", unnamedSchedule: "Untitled schedule", schedule: "Schedule", cronUnconfigured: "Cron not configured", taskMissing: "No task content provided", unknown: "Unknown", unknownChannel: "Unknown channel", unknownSchedule: "Unknown schedule", unknownPairing: "Unknown pairing", unknownEntity: "Unknown entity", notification: "Notification", deviceAction: "Device action", event: "Event", system: "system", manual: "manual" },
      status: { active: "Active", approved: "Approved", connected: "Connected", delivered: "Delivered", disabled: "Disabled", denied: "Denied", enabled: "Enabled", expired: "Expired", failed: "Failed", healthy: "Healthy", pending: "Pending local approval", pending_approval: "Pending local approval", queued: "Queued", inflight: "Delivering", retry_wait: "Waiting to retry", dead: "Retries exhausted", executing: "Executing", ready: "Ready", revoked: "Revoked", running: "Running", succeeded: "Succeeded", success: "Succeeded", unknown: "Unknown" },
      risk: { critical: "Critical risk", high: "High risk", medium: "Medium risk", low: "Low risk", blocked: "Blocked" },
      timestamp: { empty: "—", invalid: "Invalid date" },
      section: { loading: "Loading…", noData: "No data yet", loadingHistory: "Loading run history…", noHistory: "No run history yet.", selectHistory: "Choose a schedule to view its history." },
      buttons: { refreshAll: "Refresh all", refreshing: "Refreshing…", history: "History", loading: "Loading…", disable: "Disable", enable: "Enable", runNow: "Run now", delete: "Delete", returnSchedules: "Back to schedules", testConnection: "Test connection", retry: "Retry", revoke: "Revoke", approveLocal: "Approve with local double confirmation", deny: "Deny", createSchedule: "Create schedule", creating: "Creating…", createTelegram: "Create Telegram connection", createHomeAssistant: "Create Home Assistant connection", createPairingCode: "Generate pairing code", requestLocal: "Double-confirm locally and request" },
      confirm: { dangerousFirst: "Confirm this dangerous action from the local console?", dangerousSecond: "Final confirmation: this action may change a real device state. Continue?", deleteSchedule: "Delete this schedule? This action cannot be undone.", deleteConnection: "Deleting this connection stops its notifications, pairings, or device access. Continue?", revokePairing: "Revoke this channel pairing? This session can no longer approve after revocation.", denyDeviceAction: "Deny this device action request?", requestDeviceActionFirst: "Device actions can only be initiated from the local Web UI; IM cannot trigger them. Request {entityId} → {action}?", requestDeviceActionSecond: "Final confirmation: this action may change a real device state. Verify the entity, service, and input before continuing.", approveDeviceActionFirst: "Only the local Web UI can approve this. Approve {entityId} → {action}?", approveDeviceActionSecond: "Final confirmation: risk level is {risk}; expiry is {expiresAt}. Execute anyway?" },
      toast: { scheduleCreated: "Schedule created.", scheduleEnabled: "Schedule enabled.", scheduleDisabled: "Schedule disabled.", scheduleRunRequested: "Schedule run requested.", scheduleDeleted: "Schedule deleted.", deliveryRetry: "Notification queued for retry.", connectionCreated: "{kind} connection created; the secret was not echoed.", connectionEnabled: "Connection enabled.", connectionDisabled: "Connection disabled.", connectionTested: "Connection test completed.", connectionDeleted: "Connection deleted.", pairingCodeCreated: "One-time pairing code generated.", pairingRevoked: "Channel pairing revoked.", deviceActionRequested: "Device action request submitted; awaiting local approval or execution result.", deviceActionApproved: "Device action approved locally.", deviceActionDenied: "Device action denied." },
      activity: { error: "{section}: {message}", refreshed: "Refreshed channels, schedules, devices, notifications, monitoring, and audit data" },
      hero: { kicker: "P2–P3 management console", title: "Channels, schedules, and home", description: "Runtime status is authoritative only from the backend API. Telegram accepts environment-variable references only; dangerous device actions require double confirmation in the local Web UI and can never be triggered by IM.", safetyTitle: "Safety boundary", safetyDescription: "Schedules allow readOnly or acceptEdits only; credentials use env: references; device actions require two local confirmations.", migrationTitle: "Legacy draft disabled", migration: "Detected legacy IM configuration in {key}{channel}. It is only a migration hint: it cannot start channels, schedules, or devices and is never counted as runtime state.", migrationChannel: " ({channel})" },
      metrics: { ariaLabel: "Automation monitoring snapshot", activeRuns: "Active runs", pendingApprovals: "Pending approvals", schedules: "Schedules", notifications: "Notifications", channels: "Channels", devices: "Devices" },
      schedule: { kicker: "Schedules", title: "Restricted background tasks", description: "Run on Cron or manually; dangerous bypassPermissions is not available here.", empty: "No schedules yet. Create a restricted task first.", name: "Name", namePlaceholder: "Nightly tests", preset: "Preset", custom: "Custom", expression: "Cron expression", expressionPlaceholder: "@every 15m", agentId: "Agent ID", agentIdPlaceholder: "agent-id", timezone: "Time zone", timezonePlaceholder: "America/Los_Angeles", permission: "Permission", environment: "Environment", narrator: "Narrator", prompt: "Task content", promptPlaceholder: "Run tests and summarize failures; do not modify files.", nextRun: "Next run", permissions: "Permission", environments: "Environment", narratorLabel: "Narrator", history: "Run history", runMs: "{duration} ms", permissionModes: { readOnly: "Read only (recommended)", acceptEdits: "Allow file edits" }, environmentModes: { workline: "Linked project workspace", standalone: "Standalone workspace" }, narratorModes: { reuse: "Reuse linked narrator", new: "Create a narrator for each run" }, unknownValue: "Unknown option ({value})" },
      deliveries: { kicker: "Notifications", title: "Delivery history", description: "Failures and redacted reasons stay visible, and retries are explicit.", empty: "No notification deliveries yet.", attempts: "{count} attempts" },
      connections: { kicker: "Telegram", title: "Channel connections", description: "Enter env: references only; this page neither saves nor echoes plaintext tokens.", name: "Name", namePlaceholder: "Personal Telegram", credential: "Bot token environment-variable reference", credentialPlaceholder: "env:AUTOTO_TELEGRAM_BOT_TOKEN", empty: "No live connections; legacy localStorage drafts are not treated as running.", emptyTelegram: "No Telegram connections yet.", credentialConfigured: "Environment credential: configured (neither reference target nor secret is echoed)", credentialMissing: "Environment credential: not configured" },
      pairing: { kicker: "Pairing", title: "One-time pairing code", description: "Unpaired chats must not receive responses; revocation takes effect immediately.", connection: "Telegram connection", selectConnection: "Select connection", agentId: "Agent ID", agentIdPlaceholder: "agent-id", code: "One-time pairing code", expiresAt: "Expires: {timestamp}", empty: "No paired sessions yet.", agentAt: "Agent {agentId} · {timestamp}" },
      homeAssistant: { kicker: "Home Assistant", title: "Home connections and read-only entities", description: "The entity list is read-only; Access tokens may only use env: references.", name: "Name", url: "Base URL", urlPlaceholder: "http://homeassistant.local:8123", credential: "Access token environment-variable reference", credentialPlaceholder: "env:AUTOTO_HOME_ASSISTANT_TOKEN", empty: "No Home Assistant connections yet.", viewConnection: "View connection", selectConnection: "Select Home Assistant connection", devicesLimit: "Showing at most {count} entities; DOM is bounded.", noDevices: "This connection has no readable entities.", createConnectionFirst: "Create a Home Assistant connection first.", readonly: "Read-only" },
      deviceActions: { kicker: "Real-world actions", title: "Device action requests and local approval", description: "They cannot originate from IM. Submission and dangerous approval both require local double confirmation; expired requests cannot be approved.", riskBoundary: "High-risk boundary", connection: "Home Assistant connection", selectConnection: "Select connection", entityId: "Entity ID", entityPlaceholder: "light.living_room", service: "Service", servicePlaceholder: "turn_off", input: "Additional input JSON", inputPlaceholder: "{\"brightness\": 0}", empty: "No device action requests yet.", risk: "Risk", expires: "Expires" },
      audit: { kicker: "Audit", title: "Most recent 50 events", description: "Dynamic content is escaped and sensitive fragments are redacted; logs are never appended without bounds.", empty: "No audit events yet.", activity: "Actions on this page (most recent {count})" },
    },
  },
});

function lookup(catalog, key) {
  return String(key || "").split(".").reduce((value, part) => value && typeof value === "object" ? value[part] : undefined, catalog);
}

function interpolate(message, params = {}) {
  return String(message).replace(/\{([A-Za-z0-9_]+)\}/g, (match, name) => (
    Object.prototype.hasOwnProperty.call(params, name) ? String(params[name] ?? "") : match
  ));
}

export function t(key, params = {}) {
  const locale = currentUILocale();
  const message = lookup(automationMessages[locale], key) ?? lookup(automationMessages["zh-CN"], key);
  return message === undefined ? baseT(key, params) : interpolate(message, params);
}

export default automationMessages;
