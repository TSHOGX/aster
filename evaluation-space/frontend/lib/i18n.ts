"use client";
import { useTranslator } from "../../../web/i18n";
const zh = {
  "Workflow editor": "工作流编辑器", "Workflow draft is unavailable. Retry or ask a workflow editor.": "工作流草稿不可用。请重试或联系工作流编辑者。", "Back to group": "返回团队", "Save draft": "保存草稿", "Publish revision": "发布版本", "Executable workflow JSON": "可执行工作流 JSON", "Canvas layout JSON": "画布布局 JSON", "Draft revision": "草稿版本", "Layout revision": "布局版本", "Draft saved.": "草稿已保存。", "The draft changed elsewhere. Reload before saving.": "草稿已在其他位置更改，请重新加载后保存。", "Workflow and layout must be valid JSON.": "工作流和布局必须是有效 JSON。", "Workflow validation failed. Check the field errors and graph connections.": "工作流验证失败，请检查字段错误和图连接。", "Published immutable workflow revision.": "已发布不可变工作流版本。", "Publishing requires workflow-edit permission and a valid saved draft.": "发布需要工作流编辑权限和有效的已保存草稿。", "Executable JSON and layout use separate revisions. Saving validates the same graph used for execution.": "可执行 JSON 和布局使用独立版本。保存时会验证实际执行使用的同一图结构。", "Evaluation Space": "评估空间", "Groups": "团队", "Central application": "主应用", "Volume Studio": "卷工作室",
  "Checking your session…": "正在检查会话…", "Sign in": "登录", "Sign in to Evaluation Space": "登录评估空间",
  "Use your central account to continue.": "使用主应用账户继续。", "Identity is unavailable. Retry when the service is restored.": "身份服务不可用。服务恢复后请重试。",
  "Retry": "重试", "Refresh": "刷新", "Sign out of this central session": "退出此主应用会话", "Signing out…": "正在退出…",
  "Sign out is unavailable. Please retry.": "暂时无法退出，请重试。", "Search groups": "搜索团队", "Search": "搜索", "Next page": "下一页", "First page": "首页",
  "Loading groups…": "正在加载团队…", "No matching groups": "没有匹配的团队", "No readable groups": "没有可读取的团队",
  "Ask an administrator for group membership and configuration read access.": "请联系管理员，申请团队成员身份和配置读取权限。",
  "Group access was revoked. Sign in again or contact an administrator.": "团队访问权限已撤销。请重新登录或联系管理员。",
  "Group access is unavailable. Please retry.": "团队访问服务不可用，请重试。", "You no longer have access to this group.": "您已无权访问此团队。",
  "Select a group": "选择团队", "Each group keeps its own configuration and permissions.": "每个团队分别管理自己的配置和权限。",
  "Configuration": "配置", "Desired revision": "期望版本", "Active revision": "生效版本", "Automatic optimization": "自动优化",
  "On": "开启", "Off": "关闭", "Not configured": "尚未配置", "Draft": "草稿", "Activating": "正在激活", "Active": "已生效", "Activation failed": "激活失败", "Discarding": "正在放弃", "Discarded": "已放弃",
  "Edit and publish workflows": "编辑和发布工作流",
  "Your permissions": "您的权限", "Read configuration": "读取配置", "Manage configuration": "管理配置", "Operate runs": "操作运行", "Read source evidence": "读取源证据",
  "Allowed": "允许", "Not granted": "未授权", "Configuration access does not grant source evidence access.": "配置访问权限不包含源证据访问权限。",
  "No active configuration. Ask a configuration manager to complete setup.": "尚无生效配置。请联系配置管理员完成设置。",
  "A configuration change is pending. The active revision remains separate.": "配置更改尚未完成。当前生效版本保持独立。",
  "Language": "语言", "Group overview": "团队概览", "Loading group…": "正在加载团队…", "Back to groups": "返回团队列表",
};
export function useT() { return useTranslator(zh); }
