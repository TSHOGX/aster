package memory

import (
	"reflect"
	"sort"
	"testing"

	"harness-platform/orchestrator/internal/memorystore"
)

func TestDistinctScopes(t *testing.T) {
	cands := []memorystore.SemanticMemory{
		{Scope: "doris"},
		{Scope: "doris"},
		{Scope: " vhr_data "},
		{Scope: ""}, // → global
		{Scope: "数据源选择"},
	}
	got := distinctScopes(cands)
	want := []string{"global", "vhr_data", "doris", "数据源选择"}
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("distinctScopes = %v, want %v", got, want)
	}
}

func TestIdentityMap(t *testing.T) {
	scopes := []string{"doris", "f2"}
	m := identityMap(scopes)
	if len(m) != 2 || m["doris"] != "doris" || m["f2"] != "f2" {
		t.Fatalf("identityMap = %v, want identity", m)
	}
}

func TestCanonicalScope(t *testing.T) {
	mapping := map[string]string{"vhr_data": "doris", "f2": "doris"}
	cases := []struct {
		raw, want string
	}{
		{"vhr_data", "doris"},
		{"f2", "doris"},
		{"unmapped", "unmapped"}, // default to raw
		{"", "global"},           // empty → global, unmapped → itself
		{"  doris  ", "doris"},   // trimmed, unmapped → itself
	}
	for _, c := range cases {
		if got := canonicalScope(c.raw, mapping); got != c.want {
			t.Errorf("canonicalScope(%q) = %q, want %q", c.raw, got, c.want)
		}
	}
}

func TestGroupCandidates(t *testing.T) {
	// vhr_data and doris collapse to the same canonical scope; fact vs bug split.
	mapping := map[string]string{"doris": "doris", "vhr_data": "doris", "gvisor": "gvisor"}
	cands := []memorystore.SemanticMemory{
		{ID: 1, Scope: "doris", Type: "fact"},
		{ID: 2, Scope: "vhr_data", Type: "fact"}, // same bucket as #1
		{ID: 3, Scope: "doris", Type: "bug"},     // different type → own bucket
		{ID: 4, Scope: "gvisor", Type: "fact"},   // different scope → own bucket
	}
	buckets := groupCandidates(cands, mapping)
	if len(buckets) != 3 {
		t.Fatalf("expected 3 buckets, got %d: %v", len(buckets), buckets)
	}
	dorisFact := buckets[bucketKey{scope: "doris", typ: "fact"}]
	if len(dorisFact) != 2 {
		t.Fatalf("doris/fact bucket should hold #1 and #2, got %d", len(dorisFact))
	}
}

func TestGroupCandidatesUnknownTypeNormalizes(t *testing.T) {
	buckets := groupCandidates(
		[]memorystore.SemanticMemory{{ID: 1, Scope: "s", Type: "garbage"}},
		map[string]string{"s": "s"},
	)
	if _, ok := buckets[bucketKey{scope: "s", typ: "fact"}]; !ok {
		t.Fatalf("unknown type should normalize to 'fact', buckets=%v", buckets)
	}
}

func TestShardByBudget(t *testing.T) {
	small := []memorystore.SemanticMemory{
		{ID: 1, Statement: "a"},
		{ID: 2, Statement: "b"},
	}
	shards := shardByBudget(small, clusterCharBudget)
	if len(shards) != 1 {
		t.Fatalf("under-budget bucket should be one shard, got %d", len(shards))
	}

	// Force sharding with a tiny budget: each candidate has ~48 base cost.
	big := []memorystore.SemanticMemory{
		{ID: 1, Statement: "aaaa"},
		{ID: 2, Statement: "bbbb"},
		{ID: 3, Statement: "cccc"},
	}
	shards = shardByBudget(big, 60)
	if len(shards) < 2 {
		t.Fatalf("tiny budget should force multiple shards, got %d", len(shards))
	}
	// Every candidate must survive exactly once across shards.
	seen := map[int64]int{}
	for _, sh := range shards {
		for _, m := range sh {
			seen[m.ID]++
		}
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("candidate %d appears %d times across shards, want 1", id, n)
		}
	}
	if len(seen) != 3 {
		t.Fatalf("all 3 candidates should survive sharding, got %d", len(seen))
	}
}

func TestShardByBudgetSingleOversized(t *testing.T) {
	// One candidate larger than the budget still gets processed (own shard).
	big := []memorystore.SemanticMemory{{ID: 1, Statement: string(make([]byte, 500))}}
	shards := shardByBudget(big, 10)
	if len(shards) != 1 || len(shards[0]) != 1 {
		t.Fatalf("single oversized candidate should be one shard of one, got %v", shards)
	}
}

func TestUnionEvidence(t *testing.T) {
	members := []memorystore.SemanticMemory{
		{EvidenceSessionIDs: []string{"s1", "s2"}},
		{EvidenceSessionIDs: []string{"s2", "s3", ""}}, // s2 dup, empty dropped
		{EvidenceSessionIDs: []string{" s3 "}},         // trimmed → s3 dup
	}
	got := unionEvidence(members)
	want := []string{"s1", "s2", "s3"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unionEvidence = %v, want %v", got, want)
	}
}

func TestConfidenceFromSupport(t *testing.T) {
	cases := []struct {
		support int
		want    float64
	}{
		{1, 0.65},
		{2, 0.80},
		{3, 0.95}, // 0.5+0.45 = 0.95, at cap
		{10, 0.95},
	}
	for _, c := range cases {
		if got := confidenceFromSupport(c.support); got != c.want {
			t.Errorf("confidenceFromSupport(%d) = %v, want %v", c.support, got, c.want)
		}
	}
}

func TestSingletons(t *testing.T) {
	members := []memorystore.SemanticMemory{
		{ID: 1, Type: "fact", Scope: "doris", Statement: "x"},
		{ID: 2, Type: "bug", Scope: "doris", Statement: "y"},
	}
	cl := singletons(members)
	if len(cl) != 2 {
		t.Fatalf("expected 2 singleton clusters, got %d", len(cl))
	}
	if len(cl[0].MemberIDs) != 1 || cl[0].MemberIDs[0] != 1 {
		t.Fatalf("first singleton should own id 1, got %v", cl[0].MemberIDs)
	}
}

func TestFilterKnownIDs(t *testing.T) {
	known := map[int64]struct{}{1: {}, 2: {}, 3: {}}
	got := filterKnownIDs([]int64{2, 99, 2, 3, 1}, known) // 99 unknown, 2 dup
	want := []int64{2, 3, 1}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("filterKnownIDs = %v, want %v", got, want)
	}
}

func TestAppendMissingSingletons(t *testing.T) {
	members := []memorystore.SemanticMemory{
		{ID: 1, Statement: "a"},
		{ID: 2, Statement: "b"},
		{ID: 3, Statement: "c"},
	}
	// Agent only clustered id 1; 2 and 3 must survive as singletons.
	clusters := []clusterCluster{{Statement: "canon", MemberIDs: []int64{1}}}
	out := appendMissingSingletons(clusters, members)
	claimed := map[int64]bool{}
	for _, c := range out {
		for _, id := range c.MemberIDs {
			claimed[id] = true
		}
	}
	for _, id := range []int64{1, 2, 3} {
		if !claimed[id] {
			t.Errorf("member %d dropped after appendMissingSingletons", id)
		}
	}
}

func TestMergeClusterGroups(t *testing.T) {
	perShard := []clusterCluster{
		{Statement: "A", MemberIDs: []int64{1}},
		{Statement: "B", MemberIDs: []int64{2}},
		{Statement: "C", MemberIDs: []int64{3}},
		{Statement: "D", MemberIDs: []int64{4}},
	}
	// Merge 0+2, keep 1 alone, group 3 references out-of-range idx (ignored).
	groups := [][]int{{0, 2}, {1}, {3, 99}}
	out := mergeClusterGroups(perShard, groups)
	// Find the merged cluster (members 1 and 3).
	var merged *clusterCluster
	for i := range out {
		if len(out[i].MemberIDs) == 2 {
			merged = &out[i]
		}
	}
	if merged == nil {
		t.Fatalf("expected a 2-member merged cluster, got %v", out)
	}
	sort.Slice(merged.MemberIDs, func(i, j int) bool { return merged.MemberIDs[i] < merged.MemberIDs[j] })
	if !reflect.DeepEqual(merged.MemberIDs, []int64{1, 3}) {
		t.Fatalf("merged members = %v, want [1 3]", merged.MemberIDs)
	}
	// All four original ids must survive exactly once.
	seen := map[int64]int{}
	for _, c := range out {
		for _, id := range c.MemberIDs {
			seen[id]++
		}
	}
	for id := int64(1); id <= 4; id++ {
		if seen[id] != 1 {
			t.Errorf("id %d survives %d times, want 1", id, seen[id])
		}
	}
}

func TestMergeClusterGroupsUnreferencedSurvive(t *testing.T) {
	perShard := []clusterCluster{
		{Statement: "A", MemberIDs: []int64{1}},
		{Statement: "B", MemberIDs: []int64{2}},
	}
	// No group references index 1 — it must still survive.
	out := mergeClusterGroups(perShard, [][]int{{0}})
	if len(out) != 2 {
		t.Fatalf("unreferenced cluster should survive, got %d clusters", len(out))
	}
}

func TestResolveMembersAndFilterScope(t *testing.T) {
	byID := indexByID([]memorystore.SemanticMemory{
		{ID: 1, Scope: "doris"},
		{ID: 2, Scope: "gvisor"},
	})
	mems := resolveMembers([]int64{2, 99, 1}, byID) // 99 missing
	if len(mems) != 2 {
		t.Fatalf("resolveMembers should skip missing ids, got %d", len(mems))
	}

	all := []memorystore.SemanticMemory{{ID: 1, Scope: "doris"}, {ID: 2, Scope: "gvisor"}}
	if got := filterScope(all, "doris"); len(got) != 1 || got[0].ID != 1 {
		t.Fatalf("filterScope(doris) = %v, want [id 1]", got)
	}
}
