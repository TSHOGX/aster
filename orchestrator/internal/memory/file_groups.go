package memory

import "strings"

// CanonicalFileGroups is the bounded taxonomy used by the shared KB. Fine
// grained topics remain headings inside a group file rather than becoming new
// files.
const (
	FileGroupSchemaFields       = "schema-fields"
	FileGroupDomainTerms        = "domain-terms"
	FileGroupMetricsMethods     = "metrics-methods"
	FileGroupDataSources        = "data-sources"
	FileGroupToolUsage          = "tool-usage"
	FileGroupTroubleshooting    = "troubleshooting"
	FileGroupRuntimeEnvironment = "runtime-environment"
	FileGroupWorkflows          = "workflows"
	FileGroupArchitecture       = "architecture-contracts"
	FileGroupUserPreferences    = "user-preferences"
	FileGroupOther              = "other"
)

// classifyFileGroup is deterministic so historical rows can be re-rendered
// without a migration or another LLM call.
func classifyFileGroup(mType, scope, statement, when string) string {
	text := strings.ToLower(strings.Join([]string{mType, scope, statement, when}, " "))
	has := func(words ...string) bool {
		for _, word := range words {
			if strings.Contains(text, word) {
				return true
			}
		}
		return false
	}
	if mType == "bug" || has("error", "失败", "报错", "故障", "排查", "修复", "exception", "timeout", "permission denied", "缺少", "不存在") {
		return FileGroupTroubleshooting
	}
	if mType == "preference" || has("偏好", "习惯", "prefer", "默认使用", "输出格式", "用户要求") {
		return FileGroupUserPreferences
	}
	if has("公式", "计算", "指标", "算法", "统计口径", "聚合", "dtw", "metric", "method", "阈值", "过滤规则") {
		return FileGroupMetricsMethods
	}
	if mType == "procedure" || has("步骤", "流程", "workflow", "procedure", "先", "然后", "最后") {
		return FileGroupWorkflows
	}
	if has("字段", "列名", "column", "schema", "signal", "信号", "表结构", "数据类型", "datatype", "vid") {
		return FileGroupSchemaFields
	}
	if has("doris", "vhr_data", "vehicle_signal", "obs", "parquet", "数据源", "数据表", "查询来源", "data source") {
		return FileGroupDataSources
	}
	if has("术语", "专有名词", "概念", "定义", "meaning", "definition", "工况", "bms", "电池包", "车型") {
		return FileGroupDomainTerms
	}
	if has("sandbox", "rootfs", "容器", "网络", "依赖", "字体", "runsc", "runtime", "环境变量", "system package") {
		return FileGroupRuntimeEnvironment
	}
	if has("bridge", "协议", "contract", "架构", "接口", "api", "数据库边界", "orchestrator", "control plane") {
		return FileGroupArchitecture
	}
	if has("命令", "工具", "cli", "sql", "调用", "api call", "脚本", "执行") {
		return FileGroupToolUsage
	}
	return FileGroupOther
}

func fileGroupTitle(group string) string {
	switch group {
	case FileGroupSchemaFields:
		return "Schema and fields"
	case FileGroupDomainTerms:
		return "Domain terms"
	case FileGroupMetricsMethods:
		return "Metrics and methods"
	case FileGroupDataSources:
		return "Data sources"
	case FileGroupToolUsage:
		return "Tool usage"
	case FileGroupTroubleshooting:
		return "Troubleshooting"
	case FileGroupRuntimeEnvironment:
		return "Runtime environment"
	case FileGroupWorkflows:
		return "Workflows"
	case FileGroupArchitecture:
		return "Architecture and contracts"
	case FileGroupUserPreferences:
		return "User preferences"
	default:
		return "Other"
	}
}
