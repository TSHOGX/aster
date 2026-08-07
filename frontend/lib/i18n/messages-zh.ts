import type { MessageKey } from "./messages-en";

/**
 * Simplified Chinese catalog.
 *
 * Typed as a total record over MessageKey, so a key added to EN without a
 * translation here fails the build rather than silently rendering English.
 *
 * Terminology decisions, kept consistent throughout:
 *   会话   session (the workspace + its history), not 对话
 *   工作区 workspace — matches the /workspace path the agent works in
 *   步骤   the agent's tool-use steps ("steps")
 *   分享   share (verb and noun), 链接 for the share link
 *   智能体 agent — the autonomous worker, not 助手 (which is the reply's author)
 *
 * Untranslated on purpose: BatteryGPT, status labels (Ready/Running/…), key
 * names (Enter, Shift, ⌘P), /workspace, and units.
 */
export const ZH: Record<MessageKey, string> = {
  // ── Sidebar ────────────────────────────────────────────────────────────────
  "sidebar.newSession": "新建会话",
  "sidebar.creating": "创建中…",
  "sidebar.history": "历史会话",
  "sidebar.sharedWithMe": "共享给我的",
  "sidebar.noSessions": "还没有会话，新建一个开始使用。",
  "sidebar.collapse": "收起侧边栏",
  "sidebar.expand": "展开侧边栏",
  "sidebar.search": "搜索",
  "sidebar.searchTitle": "搜索（⌘P / Ctrl+P）",
  "sidebar.searchSessions": "搜索会话和消息",
  "sidebar.signOut": "退出登录",
  "sidebar.sessionActions": "「{title}」的会话操作",
  "sidebar.sessionTitle": "会话标题",
  "sidebar.rename": "重命名",
  "sidebar.share": "分享",
  "sidebar.delete": "删除",
  "sidebar.collapseSection": "收起{label}",
  "sidebar.expandSection": "展开{label}",
  "sidebar.activeSessions": "{count} 个进行中的会话",
  "sidebar.activeSession": "{count} 个进行中的会话",
  "sidebar.confirmClose": "确定关闭会话 {id}？历史记录和工作区文件会保留。",
  "sidebar.createFailed": "创建会话失败",
  "sidebar.closeFailed": "关闭会话失败",
  "sidebar.sharedReadOnly": "{title} · 由 {owner} 分享 · 只读",
  "sidebar.someoneElse": "其他用户",
  "sidebar.rowTitle": "{title} · {status} · {when}",

  // ── Connection status ──────────────────────────────────────────────────────
  "connection.connecting": "正在连接事件流…",
  "connection.reconnecting": "正在重新连接…",
  "connection.disconnected": "连接已断开，正在重试。",

  // ── Conversation ───────────────────────────────────────────────────────────
  "conversation.noSessionTitle": "未选择会话",
  "conversation.noSessionBody": "从左侧列表选择一个会话，或新建一个开始对话。",
  "conversation.emptyTitle": "今天想看点什么？",
  "conversation.emptyBody":
    "描述你需要的分析，或从下面的示例开始。智能体在沙箱中的 {path} 目录工作，结果会实时回传到这里。",
  "conversation.sharedNoLive": "共享会话不会实时更新",
  "conversation.jumpToLatest": "跳到最新",

  // ── Composer ───────────────────────────────────────────────────────────────
  "composer.placeholder": "发送消息…",
  "composer.sessionEnded": "会话已结束，新建一个继续。",
  "composer.thinking": "智能体正在思考…",
  "composer.send": "发送",
  "composer.hint": "Enter 发送 · Shift+Enter 换行",
  "composer.busy": "会话正忙或已结束。",

  // ── Message actions ────────────────────────────────────────────────────────
  "message.goodResponse": "回答不错",
  "message.badResponse": "回答不佳",
  "message.copy": "复制消息",
  "message.copied": "已复制",
  "message.more": "更多",
  "message.moreActions": "更多操作",

  // ── Trace / steps ──────────────────────────────────────────────────────────
  "trace.steps": "{count} 个步骤",
  "trace.step": "{count} 个步骤",
  "trace.showSteps": "展开步骤",
  "trace.hideSteps": "收起步骤",
  "trace.input": "输入",
  "trace.output": "输出",
  "trace.error": "错误",

  // ── Workspace panel ────────────────────────────────────────────────────────
  "workspace.title": "工作区",
  "workspace.noFilesTitle": "暂无文件",
  "workspace.noFilesBody": "会话运行后，文件会显示在这里。",
  "workspace.empty": "还没有生成文件。{brand} 会把文件写入工作区。",
  "workspace.noMatches": "没有匹配的文件。",
  "workspace.filter": "筛选文件",
  "workspace.uploadFiles": "上传文件",
  "workspace.uploadFolder": "上传文件夹",
  "workspace.uploadPaused": "智能体工作期间暂停上传",
  "workspace.uploading": "正在上传 {count} 个文件…",
  "workspace.uploadingOne": "正在上传 1 个文件…",
  "workspace.uploadFailed": "上传失败",
  "workspace.download": "下载",
  "workspace.resize": "调整工作区面板宽度",
  "workspace.resizeHint": "拖动调整宽度 · 双击恢复默认",

  // ── Artifact viewer ────────────────────────────────────────────────────────
  "viewer.close": "关闭预览",
  "viewer.fullScreen": "全屏",
  "viewer.exitFullScreen": "退出全屏",
  "viewer.edit": "编辑",
  "viewer.save": "保存",
  "viewer.saving": "保存中…",
  "viewer.cancel": "取消",
  "viewer.saved": "已保存",
  "viewer.saveFailed": "保存失败",
  "viewer.viewSource": "查看源码",
  "viewer.viewRendered": "查看渲染结果",
  "viewer.loadFailed": "无法加载此文件。",
  "viewer.tooLarge": "文件过大，无法预览。",
  "viewer.binary": "此文件无法预览。",
  "viewer.downloadInstead": "可以直接下载。",
  "viewer.loading": "加载中…",

  // ── Search palette ─────────────────────────────────────────────────────────
  "search.placeholder": "搜索会话和消息…",
  "search.empty": "输入关键词开始搜索。",
  "search.noResults": "没有结果。",
  "search.searching": "搜索中…",
  "search.sessions": "会话",
  "search.messages": "消息",
  "search.close": "关闭搜索",

  // ── Notifications ──────────────────────────────────────────────────────────
  "notifications.title": "通知",
  "notifications.empty": "暂无通知。",
  "notifications.markAllRead": "全部标为已读",
  "notifications.unread": "{count} 条未读",
  "notifications.close": "关闭",

  // ── Share dialog ───────────────────────────────────────────────────────────
  "share.title": "分享会话",
  "share.description": "拿到链接的人都可以查看此会话，但不能发送消息或修改文件。",
  "share.createLink": "创建链接",
  "share.creating": "创建中…",
  "share.copyLink": "复制链接",
  "share.copied": "链接已复制",
  "share.revoke": "撤销",
  "share.revoking": "撤销中…",
  "share.revoked": "链接已撤销",
  "share.noLink": "此会话尚未分享。",
  "share.close": "关闭",
  "share.failed": "出错了，请重试。",

  // ── Login ──────────────────────────────────────────────────────────────────
  "login.title": "登录",
  "login.user": "用户名",
  "login.password": "密码",
  "login.submit": "登录",
  "login.submitting": "登录中…",
  "login.failed": "登录失败",
  "login.rateLimited": "尝试次数过多，请稍后再试。",

  // ── Shell / boot errors ────────────────────────────────────────────────────
  "boot.title": "无法连接后端",
  "boot.hint": "启动 orchestrator（./orchestrator）后页面会自动恢复。",
  "nav.sessions": "会话",
  "nav.files": "文件",

  // ── Shared (public read-only) view ─────────────────────────────────────────
  "shared.readOnly": "只读共享会话",
  "shared.files": "文件",
  "shared.notFound": "此链接已失效。",
  "shared.expired": "此分享链接已过期或被撤销。",

  // ── Code blocks ────────────────────────────────────────────────────────────
  "code.copy": "复制",
  "code.copied": "已复制",
  "code.copyCode": "复制代码",
  // ── Upload / save toasts ───────────────────────────────────────────────────
  "workspace.searchFiles": "搜索文件",
  "workspace.uploadBusy": "智能体工作期间无法上传，请等本轮结束后重试。",
  "workspace.tooManyFiles": "文件数量过多（上限 {limit} 个），请减少选择。",
  "workspace.fileTooLarge": "{name} 超过 100 MiB。",
  "workspace.uploadedOne": "已上传 {name}",
  "workspace.uploadedMany": "已上传 {count} 个文件",
  "workspace.uploadLimit": "上传超出大小或文件数量限制。",
  "workspace.uploadFailedReason": "上传失败：{error}",
  "workspace.readOnlyShared": "此会话是他人分享给你的，只读。",
  "workspace.dropReadFailed": "无法读取拖入的文件。",
  "viewer.backToFiles": "返回文件列表",
  "viewer.cancelEdit": "取消编辑",
  "viewer.fullScreenPreview": "全屏预览",
  "viewer.exitFullScreenDock": "退出全屏（回到侧栏）",
  "viewer.openRaw": "打开原始文件",
  "viewer.savedFile": "已保存 {name}",
  "viewer.saveBusy": "智能体工作期间无法保存，请等本轮结束后重试。",
  "viewer.saveTooLarge": "文件过大，无法保存（上限 1 MiB）。",
  "viewer.saveFailedReason": "保存失败：{error}",
  "trace.executionTrace": "执行过程",
  "trace.stepError": "某个步骤报错",
  "trace.tools": "{count} 次工具调用",
  "trace.tool": "{count} 次工具调用",
  "search.recent": "最近",
  "search.noMatches": "没有找到匹配项。",
  "search.noSessionsYet": "还没有会话。",
  "login.signingIn": "登录中…",
  "share.people": "成员",
  "share.add": "添加",
  "share.username": "用户名",
  "share.noOtherUsers": "没有配置其他用户",
  "share.visibility": "可见范围",
  "share.linkVisibility": "链接可见范围",
  "share.summary": "摘要",
  "share.full": "完整",
  "share.publicLink": "公开链接",
  "share.publicLinkBody": "拿到链接的人无需登录即可查看此对话。链接会过期，且只显示当前内容，后续新增的消息不会包含在内。",
  "share.shellAlwaysFull": "Shell 会话始终以完整内容分享：命令输出本身就是内容。",
  "share.fullIncludes": "「完整」包含执行过程中的 shell 命令、文件内容和查询。",
  "share.filesNote": "只有你勾选的内容可以通过链接访问，且会冻结当前副本，之后对这些文件的改动不会显示。不勾选则只分享对话。",
  "share.replaceLink": "更换链接",
  "share.replaceLinkTitle": "签发新链接并使旧链接失效",
  "share.revokeLink": "撤销链接",
  "share.oldLinkInvalidated": "旧链接已失效",
  "share.copyFailed": "无法复制链接",
  "common.close": "关闭",
  "common.closeTab": "关闭标签",
  "common.closePanel": "关闭面板",
  "common.loadingConversation": "正在加载对话",
  "common.loadingSessions": "正在加载会话",
  "search.results": "搜索结果",
  "trace.toolError": "工具调用报错",
  "message.copiedSessionId": "已复制 session id",
  "message.copiedMarkdown": "已复制为 Markdown",
  "prompt.fade.title": "容量衰减分析",
  "prompt.fade.body": "绘制容量保持率随循环次数的变化，拟合衰减曲线，并推算健康度（SOH）降到 80% 时的循环次数。",
  "prompt.fade.prompt": "我在 /workspace 放了循环测试数据。请读取数据，绘制容量保持率随循环次数的变化曲线，拟合衰减趋势，并估算 SOH 降到 80% 时的循环次数。",
  "prompt.dqdv.title": "dQ/dV 峰位追踪",
  "prompt.dqdv.body": "计算各循环的微分容量曲线，追踪峰位漂移，区分活性锂损失与活性物质损失。",
  "prompt.dqdv.prompt": "用 /workspace 里的充电数据计算 dQ/dV 曲线，按循环次数分别作图，并追踪峰位变化以判断主要的衰减机理。",
  "prompt.thermal.title": "温度日志分析",
  "prompt.thermal.body": "扫描温度日志中的异常升温，与充放电倍率（C-rate）关联，总结哪些工步温度最高。",
  "prompt.thermal.prompt": "分析 /workspace 里的温度日志：找出异常升温点，与 C-rate 和工步类型关联，并用表格总结温度最高的几个工步。",
  "prompt.compare.title": "对比不同电芯方案",
  "prompt.compare.body": "把多个电芯或配方放在一起对比关键指标，指出差异所在。",
  "prompt.compare.prompt": "对比 /workspace 里各个电芯的容量、库伦效率和内阻，输出对比表格，并指出各方案之间的差异。",
  "login.subtitle": "请输入你的工作台账号密码。",
  "workspace.uploadsPausedNote": "智能体工作期间暂停上传。",
  "viewer.binaryTitle": "二进制文件无法预览。",
  "viewer.binaryHint": "可以用右上角的操作打开或下载原始文件。",
  "viewer.noRows": "没有可预览的数据行。"
};
