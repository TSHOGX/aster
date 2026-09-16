package memory

import "testing"

func TestVerifyEligible(t *testing.T) {
	cases := []struct {
		name      string
		statement string
		when      string
		want      bool
	}{
		// --- positive: path / tool / env / fault bearing ---
		{
			name:      "stale read-only skills path",
			statement: "技能位于 /usr/local/share/harness-skills/doris-query/SKILL.md 只读挂载",
			want:      true,
		},
		{
			name:      "missing mysql client fault",
			statement: "mysql command not found，需要改用其它客户端",
			want:      true,
		},
		{
			name:      "CJK font not installed",
			statement: "SimHei 字体未安装，导致中文渲染为方框",
			want:      true,
		},
		{
			name:      "agents skill path",
			statement: "skills now live at /agents/skills/",
			want:      true,
		},
		{
			name:      "tool name pymysql",
			statement: "使用 pymysql 连接数据库",
			want:      true,
		},
		{
			name:      "fault language only",
			statement: "图片输入不支持，任务失败",
			want:      true,
		},
		{
			name:      "bare workspace root",
			statement: "产物应写入 /workspace 目录",
			want:      true,
		},

		// --- negative: pure data-schema facts ---
		{
			name:      "table name only",
			statement: "充电工况表名为 example_measurements",
			want:      false,
		},
		{
			name:      "row count only",
			statement: "该表约有1787亿行",
			want:      false,
		},
		{
			name:      "column semantics only",
			statement: "current_curve 字段是双重编码的 JSON，需要二次解析",
			want:      false,
		},
		{
			name:      "empty",
			statement: "",
			want:      false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := verifyEligible(tc.statement, tc.when); got != tc.want {
				t.Fatalf("verifyEligible(%q) = %v, want %v", tc.statement, got, tc.want)
			}
		})
	}
}

func TestMajorityVote(t *testing.T) {
	cases := []struct {
		name     string
		verdicts []string
		want     string
		wantN    int
	}{
		{
			name:     "unanimous fixed",
			verdicts: []string{"fixed", "fixed", "fixed"},
			want:     "fixed",
			wantN:    3,
		},
		{
			name:     "two of three fixed",
			verdicts: []string{"fixed", "fixed", "current"},
			want:     "fixed",
			wantN:    2,
		},
		{
			name:     "panel overturns fixed to current",
			verdicts: []string{"fixed", "current", "current"},
			want:     "current",
			wantN:    2,
		},
		{
			name:     "three-way split → no majority",
			verdicts: []string{"fixed", "current", "stale"},
			want:     "",
			wantN:    1,
		},
		{
			name:     "case-insensitive normalization",
			verdicts: []string{"Fixed", "FIXED", " fixed "},
			want:     "fixed",
			wantN:    3,
		},
		{
			name:     "empties ignored, no clear majority",
			verdicts: []string{"", "stale", "current"},
			want:     "",
			wantN:    1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, n := majorityVote(tc.verdicts)
			if got != tc.want || n != tc.wantN {
				t.Fatalf("majorityVote(%v) = (%q, %d), want (%q, %d)", tc.verdicts, got, n, tc.want, tc.wantN)
			}
		})
	}
}

func TestNormalizeVerdict(t *testing.T) {
	cases := map[string]string{
		"current":      "current",
		"  Stale ":     "stale",
		"FIXED":        "fixed",
		"unverifiable": "unverifiable",
		"bogus":        "",
		"":             "",
		"current-ish":  "",
	}
	for in, want := range cases {
		if got := normalizeVerdict(in); got != want {
			t.Fatalf("normalizeVerdict(%q) = %q, want %q", in, got, want)
		}
	}
}
