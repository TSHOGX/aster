package memory

import (
	"reflect"
	"testing"
)

func TestReflectPlanShouldProcess(t *testing.T) {
	cases := []struct {
		name  string
		plan  ReflectPlan
		scope string
		want  bool
	}{
		{"process-all", ReflectPlan{ProcessAll: true}, "doris", true},
		{"empty-active-means-all", ReflectPlan{}, "doris", true},
		{"active-hit", ReflectPlan{ActiveScopes: []string{"doris", "frontend"}}, "doris", true},
		{"active-miss", ReflectPlan{ActiveScopes: []string{"frontend"}}, "doris", false},
		{"skip-wins-over-all", ReflectPlan{ProcessAll: true, SkipScopes: []string{"doris"}}, "doris", false},
		{"skip-wins-over-active", ReflectPlan{ActiveScopes: []string{"doris"}, SkipScopes: []string{"doris"}}, "doris", false},
		{"empty-scope-defaults-global", ReflectPlan{ActiveScopes: []string{"global"}}, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.plan.ShouldProcess(tc.scope); got != tc.want {
				t.Fatalf("ShouldProcess(%q) = %v, want %v", tc.scope, got, tc.want)
			}
		})
	}
}

func TestSortedByCountDesc(t *testing.T) {
	counts := map[string]int{"a": 1, "b": 5, "c": 5, "d": 2}
	// b and c tie at 5 → alphabetical; then d(2), then a(1).
	want := []string{"b", "c", "d", "a"}
	if got := sortedByCountDesc(counts); !reflect.DeepEqual(got, want) {
		t.Fatalf("sortedByCountDesc = %v, want %v", got, want)
	}
}

func TestParseReflectPlan(t *testing.T) {
	payload := `{"active_scopes":["doris","frontend"],"skip_scopes":["global"],"budget_hint":40,"reason":"doris has 120 new"}`
	var p ReflectPlan
	if err := parseReflectPlan(payload, &p); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(p.ActiveScopes) != 2 || p.BudgetHint != 40 || len(p.SkipScopes) != 1 {
		t.Fatalf("unexpected plan: %+v", p)
	}
}

func TestParseReflectPlanRejectsGarbage(t *testing.T) {
	var p ReflectPlan
	if err := parseReflectPlan("not json", &p); err == nil {
		t.Fatal("expected error on non-JSON payload")
	}
}
