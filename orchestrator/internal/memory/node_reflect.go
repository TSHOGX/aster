package memory

// Node 0 — Reflect/Plan. The cheap single-agent gate that opens each context-
// evolution cycle. It looks at what changed since the last cycle (candidate pool
// per scope, the previous run's counts, the current KB manifest) and emits a
// plan: which scopes are worth spending agent calls on this cycle, which to skip,
// and a rough budget hint. An empty-delta tick lets the caller stop early, so
// steady state stays cheap.
//
// Reflect is advisory: its plan biases the cycle but never gates correctness —
// the downstream nodes still apply their own hard pre-gates. On any agent error
// it falls back to "process everything" (safe, just not economical).

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

// ReflectPlan is the cycle plan Node 0 emits.
type ReflectPlan struct {
	ActiveScopes []string `json:"active_scopes"` // scopes worth processing this cycle
	SkipScopes   []string `json:"skip_scopes"`   // scopes with nothing new
	BudgetHint   int      `json:"budget_hint"`   // rough agent-call budget for the cycle
	Reason       string   `json:"reason"`
	// ProcessAll is set by the deterministic fallback (agent unavailable or
	// errored): the caller should process every scope.
	ProcessAll bool `json:"-"`
}

// ReflectNode runs the reflect/plan step.
type ReflectNode struct {
	agent *AgentRunner
	store *memorystore.Store
	log   *slog.Logger
	runID int64
}

// NewReflectNode builds the node.
func NewReflectNode(agent *AgentRunner, st *memorystore.Store, log *slog.Logger, runID int64) *ReflectNode {
	return &ReflectNode{agent: agent, store: st, log: log, runID: runID}
}

const reflectSystemPrompt = `你是上下文进化循环的"规划器"。给你本轮的候选记忆分布（按 scope 统计的候选条数）、上一轮的处理结果，以及当前知识库已有的 scope。请判断：本轮哪些 scope 有足够多的新候选、值得花费 agent 调用去消化（active_scopes）；哪些几乎没有变化、可以跳过（skip_scopes）；并给出本轮大致的调用预算（budget_hint，整数）。宁可少跑也不要空跑。

只输出如下 JSON（不要多余文字）：
{"active_scopes":["doris","frontend"],"skip_scopes":["..."],"budget_hint":40,"reason":"一句话说明"}`

// Run produces the cycle plan. It never blocks the cycle: on any failure it
// returns a ProcessAll plan.
func (n *ReflectNode) Run(ctx context.Context) (ReflectPlan, error) {
	scopeCounts, err := n.candidateScopeCounts(ctx)
	if err != nil {
		return ReflectPlan{ProcessAll: true, Reason: "scope-count query failed: " + err.Error()}, nil
	}
	if len(scopeCounts) == 0 {
		return ReflectPlan{Reason: "no candidates this cycle"}, nil // empty delta → caller may stop
	}
	if !n.agent.Enabled() {
		return ReflectPlan{ProcessAll: true, Reason: "agent disabled; process all"}, nil
	}

	data := n.renderContext(ctx, scopeCounts)
	start := time.Now()
	payload, err := n.agent.Run(ctx, reflectSystemPrompt, data, false)
	latency := time.Since(start).Milliseconds()
	verdict := "ok"
	if err != nil {
		verdict = "error"
	}
	_ = n.store.RecordAgentCall(ctx, memorystore.AgentCall{
		RunID: n.runID, Node: "reflect", Ref: "cycle", PromptSHA: promptSHA(reflectSystemPrompt, data),
		Verdict: verdict, LatencyMS: latency, OK: err == nil, Error: errString(err),
	})
	if err != nil {
		n.log.Warn("reflect: agent failed, processing all scopes", "error", err)
		return ReflectPlan{ProcessAll: true, Reason: "agent error; process all"}, nil
	}

	var plan ReflectPlan
	if perr := parseReflectPlan(payload, &plan); perr != nil {
		n.log.Warn("reflect: unparseable plan, processing all scopes", "error", perr)
		return ReflectPlan{ProcessAll: true, Reason: "unparseable plan; process all"}, nil
	}
	return plan, nil
}

// candidateScopeCounts returns candidate-memory counts grouped by scope.
func (n *ReflectNode) candidateScopeCounts(ctx context.Context) (map[string]int, error) {
	cands, err := n.store.ListSemanticMemories(ctx, "candidate")
	if err != nil {
		return nil, err
	}
	counts := map[string]int{}
	for _, m := range cands {
		counts[defaultString(m.Scope, "global")]++
	}
	return counts, nil
}

// renderContext builds the (trusted-shape, but scope labels are user-derived so
// fenced by Run) planning context: scope→count, plus last-run summary and KB scopes.
func (n *ReflectNode) renderContext(ctx context.Context, scopeCounts map[string]int) string {
	var b strings.Builder
	b.WriteString("本轮候选记忆分布（scope: 候选数）:\n")
	for _, sc := range sortedByCountDesc(scopeCounts) {
		fmt.Fprintf(&b, "- %s: %d\n", sc, scopeCounts[sc])
	}
	if last, ok, _ := n.store.LastEvolutionRun(ctx); ok {
		fmt.Fprintf(&b, "\n上一轮结果 (digest=%s): %s\n", last.Digest, last.CountsJSON)
	}
	return b.String()
}

// sortedByCountDesc returns scope keys ordered by descending count then name, so
// the rendered context is deterministic.
func sortedByCountDesc(counts map[string]int) []string {
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if counts[keys[i]] != counts[keys[j]] {
			return counts[keys[i]] > counts[keys[j]]
		}
		return keys[i] < keys[j]
	})
	return keys
}

// ShouldProcess reports whether a given scope should be processed under this
// plan. ProcessAll or an empty active list means process everything (unless the
// scope is explicitly skipped).
func (p ReflectPlan) ShouldProcess(scope string) bool {
	scope = defaultString(scope, "global")
	for _, s := range p.SkipScopes {
		if s == scope {
			return false
		}
	}
	if p.ProcessAll || len(p.ActiveScopes) == 0 {
		return true
	}
	for _, s := range p.ActiveScopes {
		if s == scope {
			return true
		}
	}
	return false
}

// parseReflectPlan decodes the reflect agent's JSON reply. Separate from Run so
// it is unit-testable without a live agent.
func parseReflectPlan(payload string, out *ReflectPlan) error {
	return json.Unmarshal([]byte(strings.TrimSpace(payload)), out)
}
