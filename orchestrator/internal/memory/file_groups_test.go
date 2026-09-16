package memory

import "testing"

func TestClassifyFileGroup(t *testing.T) {
	tests := []struct {
		name, typ, scope, statement, want string
	}{
		{"schema", "fact", "doris", "analytics_example schema 的字段需要在线发现", FileGroupSchemaFields},
		{"method", "heuristic", "charging", "SOH 的计算公式按窗口聚合", FileGroupMetricsMethods},
		{"source", "fact", "data", "Doris 是默认数据源", FileGroupDataSources},
		{"bug", "bug", "tool", "mysql client 缺失导致命令失败", FileGroupTroubleshooting},
		{"workflow", "procedure", "query", "先检查 schema，然后执行 SQL", FileGroupWorkflows},
		{"unknown", "fact", "new-topic", "一条暂时无法归类的知识", FileGroupOther},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyFileGroup(tc.typ, tc.scope, tc.statement, ""); got != tc.want {
				t.Fatalf("classifyFileGroup() = %q, want %q", got, tc.want)
			}
		})
	}
}
