package memory

// Node B — CONSOLIDATE (agentic canopy-clustering, NO vectors).
//
// See docs/agents-kb.md §"No vectors" and node
// B0/B1/B2/B3. This node replaces the exact-string dedup of
// memorystore.UpsertSemanticMemory (which pins every LLM statement at support=1) with
// blocking + in-context clustering, the classic scalable record-linkage shape:
//
//	B0 block-key  single agent: read the SET of distinct raw scope labels →
//	              canonical scope taxonomy + raw→canonical mapping (collapses
//	              synonym/bilingual scopes so equivalent statements share a block).
//	(bucket)      deterministic: group candidates by (canonical_scope, type).
//	B1 cluster    one agent PER bucket sees every candidate at once → equivalence
//	              clusters (attention does the N² compare in one window).
//	B2 reduce     buckets too big for one context window shard; a reduce agent
//	              merges the per-shard canonical statements into final clusters.
//	(write)       ReplaceWithConsolidated per cluster — deletes member rows,
//	              inserts the canonical, support = #distinct evidence sessions.
//	B3 kb-dedup   one agent per new canonical vs. existing accepted memories in
//	              the same scope → merge evidence when the agent says duplicate,
//	              so support accrues across nights, not just within a cycle.
//
// Every agent call is best-effort: on error/unparseable JSON we log-and-hold
// (candidates stay candidates, the sweep never crashes, nothing auto-promotes).
// B0 falls back to identity (canonical = raw); B1 falls back to singletons.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"harness-platform/orchestrator/internal/memorystore"
)

// clusterCharBudget bounds one B1 bucket's rendered candidates in a single
// context window. Matches slices.go's sliceCharBudget; over-budget buckets shard
// and take the B2 reduce path.
const clusterCharBudget = 12000

// consolidateAgentTimeout bounds one B0/B1/B2 call. Looser than
// defaultAgentTimeout because these are no-tool calls whose payload is a whole
// bucket of candidate statements: the largest scope in a real run was still
// generating past 6 minutes, so the ceiling only has to catch a true hang.
const consolidateAgentTimeout = 12 * time.Minute

// ConsolidateNode drives Node B.
type ConsolidateNode struct {
	agent *AgentRunner
	store *memorystore.Store
	log   *slog.Logger
	runID int64
}

// NewConsolidateNode builds the node. A nil/disabled agent is tolerated: Run
// then uses the deterministic fallbacks throughout (identity block-key, singleton
// clusters) so the node still executes without a live pi.
func NewConsolidateNode(agent *AgentRunner, st *memorystore.Store, log *slog.Logger, runID int64) *ConsolidateNode {
	if log == nil {
		log = slog.Default()
	}
	return &ConsolidateNode{agent: agent, store: st, log: log, runID: runID}
}

// ConsolidateReport summarizes one Node B pass.
type ConsolidateReport struct {
	Blocks       int      // distinct (canonical_scope, type) buckets processed
	ClustersIn   int      // candidate memories that entered clustering
	ClustersOut  int      // equivalence clusters produced
	Merged       int      // canonical memories written (member rows deleted)
	KBDuplicates int      // new canonicals folded into an existing accepted memory
	Notes        []string // human-readable trace of decisions/fallbacks
}

// ---- agent result contracts (PURE JSON in / out) ---------------------------

// blockKeyResult is B0's reply: a raw-scope → canonical-scope mapping.
type blockKeyResult struct {
	Canonical map[string]string `json:"canonical"`
}

// clusterResult is B1/B2's reply: equivalence clusters over a block.
type clusterResult struct {
	Clusters []struct {
		Canonical struct {
			Type        string `json:"type"`
			Scope       string `json:"scope"`
			Statement   string `json:"statement"`
			WhenToApply string `json:"when_to_apply"`
		} `json:"canonical"`
		MemberIDs []int64 `json:"member_ids"`
		Rationale string  `json:"rationale"`
	} `json:"clusters"`
}

// reduceResult is B2's reply: groups of per-shard cluster indices that are
// equivalent and should merge into one final cluster.
type reduceResult struct {
	Groups [][]int `json:"groups"`
}

// dedupResult is B3's reply: is a new canonical a duplicate of an accepted memory?
type dedupResult struct {
	Duplicate bool   `json:"duplicate"`
	MatchID   int64  `json:"match_id"`
	Canonical string `json:"canonical_statement"`
	Rationale string `json:"rationale"`
}

// ---- prompts (bilingual, PURE JSON) ----------------------------------------

const blockKeySystemPrompt = grounding + `

你在为记忆去重做"分块键(blocking key)"。下面数据块里是一组自由文本的 scope 标签(可能中英混杂、同义、粒度不一,例如 doris / analytics_example / doris/bms / f2 / 数据源选择)。请把语义等价或从属同一领域的标签归并到同一个规范(canonical)标签下。规范标签用简短的小写英文或原标签中最典型的一个。

只输出如下纯 JSON,不要任何解释或代码围栏:
{"canonical":{"原始标签A":"规范标签","原始标签B":"规范标签",...}}
必须为输入的每一个标签给出映射;拿不准就映射到它自身。`

const clusterSystemPrompt = grounding + `

下面数据块里是同一个领域、同一类型的一组候选记忆(每条含 id、statement、when_to_apply、evidence)。请判断哪些条目在语义上等价(表达同一条知识,允许中英文互译、措辞不同、粒度略异),把它们归为一个等价簇(cluster)。一个簇可以只含一条(独立知识)。对每个簇写一条融合后的规范陈述(canonical),并列出其成员 id。绝不把不同的知识强行合并;宁可多个小簇。

只输出如下纯 JSON,不要任何解释或代码围栏:
{"clusters":[{"canonical":{"type":"fact|heuristic|procedure|bug|preference","scope":"领域标签","statement":"融合后的一句话陈述","when_to_apply":"何时适用"},"member_ids":[12,88,140],"rationale":"为何等价"}]}
member_ids 只能取自输入中出现过的 id。`

const reduceSystemPrompt = grounding + `

下面数据块里是对同一个领域分片(shard)分别聚类后得到的多个"规范陈述",每个带一个 index。不同分片可能产生语义等价的规范陈述。请把等价的 index 归为一组,让它们最终合并为一个簇。每个 index 至少属于一组;不等价的自成一组。

只输出如下纯 JSON,不要任何解释或代码围栏:
{"groups":[[0,3],[1],[2,4]]}`

const kbDedupSystemPrompt = grounding + `

下面数据块里有一条"新的候选记忆"(new)和若干条"已被接受的记忆"(accepted,含 id 与 statement),它们同属一个领域。请判断这条新记忆是否与某一条已接受记忆表达同一知识(重复/精化)。

只输出如下纯 JSON,不要任何解释或代码围栏:
{"duplicate":true|false,"match_id":匹配到的accepted的id或0,"canonical_statement":"合并后更好的一句话陈述(可留空)","rationale":"理由"}`

// ---- Run --------------------------------------------------------------------

// Run executes the full Node B pass over the current `candidate` memories.
func (n *ConsolidateNode) Run(ctx context.Context) (ConsolidateReport, error) {
	var rep ConsolidateReport

	cands, err := n.store.ListSemanticMemories(ctx, "candidate")
	if err != nil {
		return rep, fmt.Errorf("consolidate: list candidates: %w", err)
	}
	if len(cands) == 0 {
		rep.Notes = append(rep.Notes, "no candidate memories; nothing to consolidate")
		return rep, nil
	}

	// B0 — blocking key: distinct raw scopes → canonical mapping (identity fallback).
	rawScopes := distinctScopes(cands)
	canonicalOf := n.blockKey(ctx, rawScopes, &rep)

	// Deterministic bucketing by (canonical_scope, type).
	buckets := groupCandidates(cands, canonicalOf)
	rep.Blocks = len(buckets)
	rep.ClustersIn = len(cands)

	// Stable bucket order for deterministic reporting/testing.
	keys := make([]bucketKey, 0, len(buckets))
	for k := range buckets {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].scope != keys[j].scope {
			return keys[i].scope < keys[j].scope
		}
		return keys[i].typ < keys[j].typ
	})

	byID := indexByID(cands)

	// B1/B2 — cluster each bucket, then write each cluster.
	for _, k := range keys {
		members := buckets[k]
		clusters := n.clusterBucket(ctx, k, members, &rep)
		rep.ClustersOut += len(clusters)
		for _, cl := range clusters {
			newID, ok := n.writeCluster(ctx, k, cl, byID, &rep)
			if !ok {
				continue
			}
			rep.Merged++
			// B3 — dedup the freshly written canonical against the KB.
			n.dedupAgainstKB(ctx, newID, k.scope, &rep)
		}
	}

	return rep, nil
}

// ---- B0: blocking key -------------------------------------------------------

// scopeShardBudget bounds how many scope labels go into one B0 call. Labels are
// short (a few words), so this is a count rather than a char budget; 120 keeps a
// shard well inside a single comfortable generation.
const scopeShardBudget = 120

// shardScopes splits scope labels into chunks of at most budget entries.
func shardScopes(scopes []string, budget int) [][]string {
	if budget < 1 {
		budget = 1
	}
	var out [][]string
	for start := 0; start < len(scopes); start += budget {
		end := start + budget
		if end > len(scopes) {
			end = len(scopes)
		}
		out = append(out, scopes[start:end])
	}
	return out
}

// sortedKeys returns a set's keys in deterministic order (agent prompts must not
// vary run to run for the same input).
func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// blockKeyShard runs one B0 call over a subset of scope labels and returns the
// raw→canonical pairs it produced. ok=false means the caller should keep identity
// for these labels.
func (n *ConsolidateNode) blockKeyShard(ctx context.Context, ref string, scopes []string) (map[string]string, bool) {
	data := "scopes:\n" + strings.Join(scopes, "\n")
	payload, err := n.callAgent(ctx, ref, blockKeySystemPrompt, data, "block-key")
	if err != nil {
		n.log.Warn("consolidate: B0 shard failed; identity fallback", "ref", ref, "error", err)
		return nil, false
	}
	var res blockKeyResult
	if err := json.Unmarshal([]byte(payload), &res); err != nil || len(res.Canonical) == 0 {
		n.log.Warn("consolidate: B0 shard unparseable; identity fallback", "ref", ref, "error", err)
		return nil, false
	}
	out := make(map[string]string, len(res.Canonical))
	for raw, canon := range res.Canonical {
		raw = strings.TrimSpace(raw)
		canon = strings.TrimSpace(canon)
		if raw == "" {
			continue
		}
		if canon == "" {
			canon = raw
		}
		out[raw] = canon
	}
	return out, true
}

// blockKey asks one agent to collapse the raw scope labels into canonical ones.
// On any failure it returns the identity map (canonical = raw), so bucketing
// still runs (just without synonym collapsing).
func (n *ConsolidateNode) blockKey(ctx context.Context, rawScopes []string, rep *ConsolidateReport) map[string]string {
	identity := identityMap(rawScopes)
	if n.agent == nil || !n.agent.Enabled() || len(rawScopes) == 0 {
		rep.Notes = append(rep.Notes, "B0: identity block-key (agent disabled or no scopes)")
		return identity
	}

	// B0 used to send every raw scope in ONE call. At real scale that is 686
	// labels, which blew past even a 12-minute ceiling and fell back to identity —
	// costing the whole cycle its synonym collapsing (doris / Doris SQL /
	// 车辆信号数据仓库 stayed separate buckets). Shard like B1/B2 do, then run one
	// reduce pass over the per-shard canonicals: that set is far smaller than the
	// raw input, so the cross-shard view still fits in a single call.
	shards := shardScopes(rawScopes, scopeShardBudget)
	out := identity
	perShardCanonicals := map[string]bool{}
	failed := 0
	for i, shard := range shards {
		res, ok := n.blockKeyShard(ctx, fmt.Sprintf("B0:%d/%d", i+1, len(shards)), shard)
		if !ok {
			failed++
			continue // that shard keeps identity
		}
		for raw, canon := range res {
			out[raw] = canon
			perShardCanonicals[canon] = true
		}
	}
	if failed > 0 {
		rep.Notes = append(rep.Notes, fmt.Sprintf("B0: %d/%d shards fell back to identity", failed, len(shards)))
	}
	// Reduce: shards cannot see each other, so two of them may have coined
	// different canonicals for the same concept. Collapse those once.
	if len(shards) > 1 && len(perShardCanonicals) > 1 {
		canons := sortedKeys(perShardCanonicals)
		if res, ok := n.blockKeyShard(ctx, "B0:reduce", canons); ok {
			for raw, canon := range out {
				if merged, hit := res[canon]; hit && merged != "" {
					out[raw] = merged
				}
			}
			rep.Notes = append(rep.Notes, fmt.Sprintf("B0: reduce merged %d shard canonicals", len(canons)))
		}
	}
	rep.Notes = append(rep.Notes, fmt.Sprintf("B0: %d raw scopes → %d canonical", len(rawScopes), countDistinctValues(out)))
	return out
}

// ---- B1/B2: cluster a bucket ------------------------------------------------

// clusterBucket clusters one (canonical_scope, type) bucket. Small buckets take
// one B1 agent call; oversized buckets shard and take the B2 reduce path. On
// agent failure it falls back to singleton clusters (one per candidate).
func (n *ConsolidateNode) clusterBucket(ctx context.Context, k bucketKey, members []memorystore.SemanticMemory, rep *ConsolidateReport) []clusterCluster {
	if len(members) == 0 {
		return nil
	}
	if n.agent == nil || !n.agent.Enabled() {
		rep.Notes = append(rep.Notes, fmt.Sprintf("B1[%s/%s]: singleton fallback (agent disabled)", k.scope, k.typ))
		return singletons(members)
	}

	shards := shardByBudget(members, clusterCharBudget)
	if len(shards) == 1 {
		return n.clusterOne(ctx, k, shards[0], rep)
	}

	// B2 reduce: cluster each shard, then merge equivalent per-shard canonicals.
	rep.Notes = append(rep.Notes, fmt.Sprintf("B2[%s/%s]: %d candidates → %d shards", k.scope, k.typ, len(members), len(shards)))
	var perShard []clusterCluster
	for _, sh := range shards {
		perShard = append(perShard, n.clusterOne(ctx, k, sh, rep)...)
	}
	return n.reduceClusters(ctx, k, perShard, rep)
}

// clusterOne runs one B1 agent call over a single shard/bucket.
func (n *ConsolidateNode) clusterOne(ctx context.Context, k bucketKey, members []memorystore.SemanticMemory, rep *ConsolidateReport) []clusterCluster {
	data := renderCandidates(members)
	payload, err := n.callAgent(ctx, "B1:"+k.scope+"/"+k.typ, clusterSystemPrompt, data, "cluster")
	if err != nil {
		n.log.Warn("consolidate: B1 cluster failed; singleton fallback", "scope", k.scope, "type", k.typ, "error", err)
		rep.Notes = append(rep.Notes, fmt.Sprintf("B1[%s/%s]: singleton fallback (%s)", k.scope, k.typ, errString(err)))
		return singletons(members)
	}
	var res clusterResult
	if err := json.Unmarshal([]byte(payload), &res); err != nil || len(res.Clusters) == 0 {
		n.log.Warn("consolidate: B1 unparseable; singleton fallback", "scope", k.scope, "type", k.typ, "error", err)
		rep.Notes = append(rep.Notes, fmt.Sprintf("B1[%s/%s]: singleton fallback (unparseable)", k.scope, k.typ))
		return singletons(members)
	}

	valid := idSet(members)
	var out []clusterCluster
	for _, c := range res.Clusters {
		ids := filterKnownIDs(c.MemberIDs, valid)
		if len(ids) == 0 {
			continue
		}
		out = append(out, clusterCluster{
			Type:        defaultString(c.Canonical.Type, k.typ),
			Scope:       defaultString(c.Canonical.Scope, k.scope),
			Statement:   strings.TrimSpace(c.Canonical.Statement),
			WhenToApply: strings.TrimSpace(c.Canonical.WhenToApply),
			MemberIDs:   ids,
			Rationale:   strings.TrimSpace(c.Rationale),
		})
	}
	// Candidates the agent omitted entirely stay as their own singletons so no
	// knowledge is silently dropped.
	out = appendMissingSingletons(out, members)
	if len(out) == 0 {
		return singletons(members)
	}
	return out
}

// reduceClusters merges equivalent per-shard canonicals (B2). Falls back to the
// identity grouping (each per-shard cluster kept as-is) on agent failure.
func (n *ConsolidateNode) reduceClusters(ctx context.Context, k bucketKey, perShard []clusterCluster, rep *ConsolidateReport) []clusterCluster {
	if len(perShard) <= 1 {
		return perShard
	}
	data := renderClustersForReduce(perShard)
	payload, err := n.callAgent(ctx, "B2:"+k.scope+"/"+k.typ, reduceSystemPrompt, data, "reduce")
	if err != nil {
		n.log.Warn("consolidate: B2 reduce failed; keep per-shard clusters", "scope", k.scope, "error", err)
		rep.Notes = append(rep.Notes, fmt.Sprintf("B2[%s/%s]: identity reduce (%s)", k.scope, k.typ, errString(err)))
		return perShard
	}
	var res reduceResult
	if err := json.Unmarshal([]byte(payload), &res); err != nil || len(res.Groups) == 0 {
		rep.Notes = append(rep.Notes, fmt.Sprintf("B2[%s/%s]: identity reduce (unparseable)", k.scope, k.typ))
		return perShard
	}
	return mergeClusterGroups(perShard, res.Groups)
}

// ---- write + B3 -------------------------------------------------------------

// writeCluster persists one cluster via ReplaceWithConsolidated. Returns the new
// canonical memory id. Skips (ok=false) clusters with no resolvable members.
func (n *ConsolidateNode) writeCluster(ctx context.Context, k bucketKey, cl clusterCluster, byID map[int64]memorystore.SemanticMemory, rep *ConsolidateReport) (int64, bool) {
	mems := resolveMembers(cl.MemberIDs, byID)
	if len(mems) == 0 {
		return 0, false
	}
	evidence := unionEvidence(mems)
	stmt := cl.Statement
	if stmt == "" {
		stmt = mems[0].Statement // fall back to a real member statement
	}
	when := cl.WhenToApply
	if when == "" {
		when = mems[0].WhenToApply
	}
	support := len(evidence)
	if support < 1 {
		support = 1
	}
	cm := memorystore.ConsolidatedMemory{
		Type:               defaultString(cl.Type, k.typ),
		Scope:              defaultString(cl.Scope, k.scope),
		Statement:          stmt,
		WhenToApply:        when,
		EvidenceSessionIDs: evidence,
		MemberIDs:          cl.MemberIDs,
		AgentRationale:     cl.Rationale,
		Confidence:         confidenceFromSupport(support),
	}
	newID, err := n.store.ReplaceWithConsolidated(ctx, cm)
	if err != nil {
		n.log.Warn("consolidate: ReplaceWithConsolidated failed", "scope", k.scope, "type", k.typ, "error", err)
		rep.Notes = append(rep.Notes, fmt.Sprintf("write[%s/%s]: %s", k.scope, k.typ, errString(err)))
		return 0, false
	}
	return newID, true
}

// dedupAgainstKB (B3) compares a freshly consolidated candidate against existing
// accepted memories in the same canonical scope. On a duplicate verdict it folds
// the two (union of evidence) via ReplaceWithConsolidated, so support accrues
// across nights. Note: folding an accepted row re-inserts it as a candidate with
// higher support — Node D re-promotes it. Best-effort; failures just leave the
// new candidate standing on its own.
func (n *ConsolidateNode) dedupAgainstKB(ctx context.Context, newID int64, scope string, rep *ConsolidateReport) {
	if n.agent == nil || !n.agent.Enabled() {
		return
	}
	accepted, err := n.store.ListSemanticMemories(ctx, "accepted")
	if err != nil {
		n.log.Warn("consolidate: B3 list accepted failed", "error", err)
		return
	}
	// The just-written memory + the accepted set live in the same table; re-read
	// candidates to find our new row (ReplaceWithConsolidated returned its id).
	cands, err := n.store.ListSemanticMemories(ctx, "candidate")
	if err != nil {
		return
	}
	newMem, ok := findByID(cands, newID)
	if !ok {
		return
	}
	sameScope := filterScope(accepted, scope)
	if len(sameScope) == 0 {
		return
	}

	data := renderKBDedup(newMem, sameScope)
	payload, err := n.callAgent(ctx, fmt.Sprintf("B3:%d", newID), kbDedupSystemPrompt, data, "kb-dedup")
	if err != nil {
		n.log.Warn("consolidate: B3 kb-dedup failed", "id", newID, "error", err)
		return
	}
	var res dedupResult
	if err := json.Unmarshal([]byte(payload), &res); err != nil {
		return
	}
	if !res.Duplicate || res.MatchID == 0 {
		return
	}
	match, ok := findByID(sameScope, res.MatchID)
	if !ok {
		return
	}

	evidence := unionEvidence([]memorystore.SemanticMemory{newMem, match})
	stmt := defaultString(res.Canonical, match.Statement)
	support := len(evidence)
	if support < 1 {
		support = 1
	}
	_, err = n.store.ReplaceWithConsolidated(ctx, memorystore.ConsolidatedMemory{
		Type:               defaultString(match.Type, newMem.Type),
		Scope:              defaultString(scope, match.Scope),
		Statement:          stmt,
		WhenToApply:        defaultString(match.WhenToApply, newMem.WhenToApply),
		EvidenceSessionIDs: evidence,
		MemberIDs:          []int64{newID, match.ID},
		AgentRationale:     "B3 kb-dedup: " + res.Rationale,
		Confidence:         confidenceFromSupport(support),
	})
	if err != nil {
		n.log.Warn("consolidate: B3 fold failed", "id", newID, "match", match.ID, "error", err)
		return
	}
	rep.KBDuplicates++
	rep.Notes = append(rep.Notes, fmt.Sprintf("B3: folded new #%d into accepted #%d (support→%d)", newID, match.ID, support))
}

// ---- agent call wrapper (audit) ---------------------------------------------

func (n *ConsolidateNode) callAgent(ctx context.Context, ref, sys, data, verdict string) (string, error) {
	// Bound the call like verify/curate/critic do — without a ceiling a hung B1/B2
	// generation waits on the sweep's context, i.e. effectively forever. The cap is
	// deliberately looser than defaultAgentTimeout: these are no-tool calls with a
	// large payload, and an observed run had legitimate doris blocks still
	// generating at ~6.5min, so a 5min cap would guillotine real work.
	callCtx, cancel := context.WithTimeout(ctx, consolidateAgentTimeout)
	defer cancel()
	start := time.Now()
	payload, err := n.agent.Run(callCtx, sys, data, false)
	ms := time.Since(start).Milliseconds()
	v := verdict
	if err != nil {
		v = "error"
	}
	_ = n.store.RecordAgentCall(ctx, memorystore.AgentCall{
		RunID: n.runID, Node: "consolidate", Ref: ref, Verdict: v,
		LatencyMS: ms, OK: err == nil, Error: errString(err),
	})
	return payload, err
}

// ---- PURE helpers (unit-tested) --------------------------------------------

// clusterCluster is a resolved equivalence cluster: a canonical statement + the
// raw candidate ids it replaces.
type clusterCluster struct {
	Type        string
	Scope       string
	Statement   string
	WhenToApply string
	MemberIDs   []int64
	Rationale   string
}

// bucketKey identifies one deterministic bucket.
type bucketKey struct {
	scope string
	typ   string
}

// distinctScopes returns the sorted set of raw scope strings among candidates.
func distinctScopes(cands []memorystore.SemanticMemory) []string {
	seen := map[string]struct{}{}
	for _, c := range cands {
		s := strings.TrimSpace(c.Scope)
		if s == "" {
			s = "global"
		}
		seen[s] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for s := range seen {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// identityMap maps every scope to itself (the B0 deterministic fallback).
func identityMap(scopes []string) map[string]string {
	m := make(map[string]string, len(scopes))
	for _, s := range scopes {
		m[s] = s
	}
	return m
}

// canonicalScope resolves a raw scope through the B0 mapping, defaulting to the
// raw (normalized) scope when unmapped.
func canonicalScope(raw string, mapping map[string]string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = "global"
	}
	if c, ok := mapping[raw]; ok && strings.TrimSpace(c) != "" {
		return strings.TrimSpace(c)
	}
	return raw
}

// groupCandidates buckets candidates by (canonical_scope, type).
func groupCandidates(cands []memorystore.SemanticMemory, mapping map[string]string) map[bucketKey][]memorystore.SemanticMemory {
	out := map[bucketKey][]memorystore.SemanticMemory{}
	for _, c := range cands {
		k := bucketKey{
			scope: canonicalScope(c.Scope, mapping),
			typ:   normalizeMemType(c.Type),
		}
		out[k] = append(out[k], c)
	}
	return out
}

// shardByBudget splits a bucket into shards each under the char budget. It never
// returns an empty result; a bucket already under budget is a single shard. A
// single candidate over budget still gets its own shard (it must be processed).
func shardByBudget(members []memorystore.SemanticMemory, budget int) [][]memorystore.SemanticMemory {
	if len(members) == 0 {
		return nil
	}
	// Fast path: the whole bucket fits.
	if renderedSize(members) <= budget {
		return [][]memorystore.SemanticMemory{members}
	}
	var shards [][]memorystore.SemanticMemory
	var cur []memorystore.SemanticMemory
	curSize := 0
	for _, m := range members {
		sz := candidateSize(m)
		if len(cur) > 0 && curSize+sz > budget {
			shards = append(shards, cur)
			cur = nil
			curSize = 0
		}
		cur = append(cur, m)
		curSize += sz
	}
	if len(cur) > 0 {
		shards = append(shards, cur)
	}
	return shards
}

// candidateSize approximates the rendered char cost of one candidate.
func candidateSize(m memorystore.SemanticMemory) int {
	return len(m.Statement) + len(m.WhenToApply) + len(strings.Join(m.EvidenceSessionIDs, ",")) + 48
}

func renderedSize(members []memorystore.SemanticMemory) int {
	total := 0
	for _, m := range members {
		total += candidateSize(m)
	}
	return total
}

// unionEvidence returns the DISTINCT union of member evidence session ids,
// sorted for determinism.
func unionEvidence(members []memorystore.SemanticMemory) []string {
	seen := map[string]struct{}{}
	for _, m := range members {
		for _, s := range m.EvidenceSessionIDs {
			s = strings.TrimSpace(s)
			if s == "" {
				continue
			}
			seen[s] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for s := range seen {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// confidenceFromSupport derives a stored confidence signal from real distinct-
// session support. The promotion GATE is Node D; this is just a signal.
func confidenceFromSupport(support int) float64 {
	c := 0.5 + 0.15*float64(support)
	if c > 0.95 {
		return 0.95
	}
	return c
}

// singletons turns each candidate into its own one-member cluster (the B1
// deterministic fallback).
func singletons(members []memorystore.SemanticMemory) []clusterCluster {
	out := make([]clusterCluster, 0, len(members))
	for _, m := range members {
		out = append(out, clusterCluster{
			Type:        normalizeMemType(m.Type),
			Scope:       m.Scope,
			Statement:   m.Statement,
			WhenToApply: m.WhenToApply,
			MemberIDs:   []int64{m.ID},
		})
	}
	return out
}

// appendMissingSingletons ensures every member id appears in some cluster; any
// candidate the agent omitted becomes its own singleton so knowledge is not lost.
func appendMissingSingletons(clusters []clusterCluster, members []memorystore.SemanticMemory) []clusterCluster {
	claimed := map[int64]struct{}{}
	for _, c := range clusters {
		for _, id := range c.MemberIDs {
			claimed[id] = struct{}{}
		}
	}
	for _, m := range members {
		if _, ok := claimed[m.ID]; ok {
			continue
		}
		clusters = append(clusters, clusterCluster{
			Type:        normalizeMemType(m.Type),
			Scope:       m.Scope,
			Statement:   m.Statement,
			WhenToApply: m.WhenToApply,
			MemberIDs:   []int64{m.ID},
		})
	}
	return clusters
}

// mergeClusterGroups merges per-shard clusters per the B2 reduce groups (indices
// into perShard). Indices not referenced by any group survive as-is.
func mergeClusterGroups(perShard []clusterCluster, groups [][]int) []clusterCluster {
	used := map[int]struct{}{}
	var out []clusterCluster
	for _, g := range groups {
		var merged *clusterCluster
		for _, idx := range g {
			if idx < 0 || idx >= len(perShard) {
				continue
			}
			if _, dup := used[idx]; dup {
				continue
			}
			used[idx] = struct{}{}
			c := perShard[idx]
			if merged == nil {
				cp := c
				merged = &cp
				continue
			}
			merged.MemberIDs = append(merged.MemberIDs, c.MemberIDs...)
			if merged.Rationale == "" {
				merged.Rationale = c.Rationale
			}
		}
		if merged != nil {
			out = append(out, *merged)
		}
	}
	// Any per-shard cluster no group referenced stays on its own.
	for i, c := range perShard {
		if _, ok := used[i]; ok {
			continue
		}
		out = append(out, c)
	}
	return out
}

func indexByID(cands []memorystore.SemanticMemory) map[int64]memorystore.SemanticMemory {
	m := make(map[int64]memorystore.SemanticMemory, len(cands))
	for _, c := range cands {
		m[c.ID] = c
	}
	return m
}

func idSet(members []memorystore.SemanticMemory) map[int64]struct{} {
	m := make(map[int64]struct{}, len(members))
	for _, x := range members {
		m[x.ID] = struct{}{}
	}
	return m
}

func filterKnownIDs(ids []int64, known map[int64]struct{}) []int64 {
	seen := map[int64]struct{}{}
	var out []int64
	for _, id := range ids {
		if _, ok := known[id]; !ok {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func resolveMembers(ids []int64, byID map[int64]memorystore.SemanticMemory) []memorystore.SemanticMemory {
	var out []memorystore.SemanticMemory
	for _, id := range ids {
		if m, ok := byID[id]; ok {
			out = append(out, m)
		}
	}
	return out
}

func findByID(mems []memorystore.SemanticMemory, id int64) (memorystore.SemanticMemory, bool) {
	for _, m := range mems {
		if m.ID == id {
			return m, true
		}
	}
	return memorystore.SemanticMemory{}, false
}

func filterScope(mems []memorystore.SemanticMemory, scope string) []memorystore.SemanticMemory {
	scope = strings.TrimSpace(scope)
	var out []memorystore.SemanticMemory
	for _, m := range mems {
		if strings.TrimSpace(m.Scope) == scope {
			out = append(out, m)
		}
	}
	return out
}

func countDistinctValues(m map[string]string) int {
	seen := map[string]struct{}{}
	for _, v := range m {
		seen[v] = struct{}{}
	}
	return len(seen)
}

// ---- data rendering (untrusted; fenced by AgentRunner.Run) -----------------

// renderCandidates renders a bucket/shard's candidates as JSON lines for B1.
func renderCandidates(members []memorystore.SemanticMemory) string {
	var b strings.Builder
	b.WriteString("candidates:\n")
	for _, m := range members {
		row := map[string]any{
			"id":            m.ID,
			"statement":     m.Statement,
			"when_to_apply": m.WhenToApply,
			"evidence":      m.EvidenceSessionIDs,
		}
		b.Write(mustJSONBytes(row))
		b.WriteByte('\n')
	}
	return b.String()
}

// renderClustersForReduce renders per-shard canonicals with their index for B2.
func renderClustersForReduce(clusters []clusterCluster) string {
	var b strings.Builder
	b.WriteString("canonical_statements:\n")
	for i, c := range clusters {
		row := map[string]any{
			"index":         i,
			"statement":     c.Statement,
			"when_to_apply": c.WhenToApply,
		}
		b.Write(mustJSONBytes(row))
		b.WriteByte('\n')
	}
	return b.String()
}

// renderKBDedup renders the new memory + accepted set in the same scope for B3.
func renderKBDedup(newMem memorystore.SemanticMemory, accepted []memorystore.SemanticMemory) string {
	var b strings.Builder
	b.WriteString("new:\n")
	b.Write(mustJSONBytes(map[string]any{"statement": newMem.Statement, "when_to_apply": newMem.WhenToApply}))
	b.WriteString("\naccepted:\n")
	for _, m := range accepted {
		b.Write(mustJSONBytes(map[string]any{"id": m.ID, "statement": m.Statement}))
		b.WriteByte('\n')
	}
	return b.String()
}

func mustJSONBytes(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte("{}")
	}
	return b
}
