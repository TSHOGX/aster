package memory

import (
	"encoding/json"
	"reflect"
	"testing"

	"harness-platform/orchestrator/internal/store"
)

func TestParseCriticResult(t *testing.T) {
	// A representative critic reply: one memory veto, one skill veto, plus counts.
	reply := `{"vetoes":[{"kind":"memory","id":12,"issue":"contradicts accepted fact #7 about the skills path"},{"kind":"skill","id":3,"issue":"duplicates seed skill doris-query"}],"ok_count":5,"rationale":"两条条目与现有知识冲突"}`
	var res criticResult
	if err := json.Unmarshal([]byte(reply), &res); err != nil {
		t.Fatalf("unmarshal criticResult: %v", err)
	}
	if len(res.Vetoes) != 2 {
		t.Fatalf("want 2 vetoes, got %d", len(res.Vetoes))
	}
	if res.OKCount != 5 {
		t.Errorf("want ok_count=5, got %d", res.OKCount)
	}
	if res.Vetoes[0].Kind != "memory" || res.Vetoes[0].ID != 12 {
		t.Errorf("veto[0] = %+v", res.Vetoes[0])
	}
	if res.Vetoes[1].Kind != "skill" || res.Vetoes[1].ID != 3 {
		t.Errorf("veto[1] = %+v", res.Vetoes[1])
	}
	if res.Rationale == "" {
		t.Errorf("rationale should be preserved")
	}
}

func TestParseCriticResultEmpty(t *testing.T) {
	var res criticResult
	if err := json.Unmarshal([]byte(`{"vetoes":[],"ok_count":9,"rationale":"all consistent"}`), &res); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(res.Vetoes) != 0 {
		t.Errorf("want no vetoes, got %d", len(res.Vetoes))
	}
	if res.OKCount != 9 {
		t.Errorf("want ok_count=9, got %d", res.OKCount)
	}
}

func TestPartitionVetoes(t *testing.T) {
	memIDs := map[int64]bool{12: true, 13: true}
	skillIDs := map[int64]bool{3: true}

	vetoes := []criticVeto{
		{Kind: "memory", ID: 12, Issue: "contradiction"},
		{Kind: "Skill", ID: 3, Issue: "duplicate"},          // case-insensitive kind
		{Kind: "memory", ID: 999, Issue: "hallucinated id"}, // not in reviewed set → ignored
		{Kind: "bogus", ID: 13, Issue: "unknown kind"},      // unknown kind → ignored
		{Kind: "memory", ID: 12, Issue: "dup veto"},         // duplicate id → deduped
	}

	memV, skillV, notes := partitionVetoes(vetoes, memIDs, skillIDs)

	if !reflect.DeepEqual(memV, []int64{12}) {
		t.Errorf("memVetoes = %v, want [12]", memV)
	}
	if !reflect.DeepEqual(skillV, []int64{3}) {
		t.Errorf("skillVetoes = %v, want [3]", skillV)
	}
	if len(notes) == 0 {
		t.Errorf("expected explanatory notes for ignored/applied vetoes")
	}
}

func TestPartitionVetoesNone(t *testing.T) {
	memV, skillV, _ := partitionVetoes(nil, map[int64]bool{1: true}, map[int64]bool{2: true})
	if len(memV) != 0 || len(skillV) != 0 {
		t.Errorf("no vetoes should yield empty slices, got mem=%v skill=%v", memV, skillV)
	}
}

func TestNormalizeCriticKind(t *testing.T) {
	cases := map[string]string{
		"memory":   "memory",
		"MEMORY":   "memory",
		" mem ":    "memory",
		"fact":     "memory",
		"skill":    "skill",
		"Skills":   "skill",
		"whatever": "",
		"":         "",
	}
	for in, want := range cases {
		if got := normalizeCriticKind(in); got != want {
			t.Errorf("normalizeCriticKind(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFilterReviewSets(t *testing.T) {
	mems := []store.SemanticMemory{{ID: 1}, {ID: 2}, {ID: 3}}
	skills := []store.SkillCandidate{{ID: 10}, {ID: 11}}

	// nil/empty filter → review all (v1 default).
	if got := filterMemoryReviewSet(mems, nil); len(got) != 3 {
		t.Errorf("nil filter should keep all memories, got %d", len(got))
	}
	if got := filterSkillReviewSet(skills, map[int64]bool{}); len(got) != 2 {
		t.Errorf("empty filter should keep all skills, got %d", len(got))
	}

	// restricted delta.
	gotM := filterMemoryReviewSet(mems, map[int64]bool{2: true})
	if len(gotM) != 1 || gotM[0].ID != 2 {
		t.Errorf("filtered memories = %+v, want [id=2]", gotM)
	}
	gotS := filterSkillReviewSet(skills, map[int64]bool{11: true})
	if len(gotS) != 1 || gotS[0].ID != 11 {
		t.Errorf("filtered skills = %+v, want [id=11]", gotS)
	}
}

func TestIDSets(t *testing.T) {
	mems := []store.SemanticMemory{{ID: 5}, {ID: 6}}
	skills := []store.SkillCandidate{{ID: 7}}
	ms := memoryIDSet(mems)
	if !ms[5] || !ms[6] || ms[99] {
		t.Errorf("memoryIDSet wrong: %v", ms)
	}
	ss := skillIDSet(skills)
	if !ss[7] || ss[5] {
		t.Errorf("skillIDSet wrong: %v", ss)
	}
}

func TestFormatCriticData(t *testing.T) {
	mems := []store.SemanticMemory{
		{ID: 1, Type: "fact", Scope: "doris", Statement: "current_curve is double-encoded JSON", WhenToApply: "解析充电曲线时"},
	}
	skills := []store.SkillCandidate{
		{ID: 2, TargetSkillName: "doris-query", KBStatus: "observing", Title: "Query Doris for charging data"},
	}
	out := formatCriticData(mems, skills)
	for _, want := range []string{"ACCEPTED_MEMORIES", "id=1", "doris", "double-encoded", "PUBLISHED_SKILLS", "id=2", "doris-query"} {
		if !contains(out, want) {
			t.Errorf("formatCriticData output missing %q\n---\n%s", want, out)
		}
	}
}

// contains is a tiny substring helper (avoids pulling strings into the test for
// one call and mirrors the intent clearly).
func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
