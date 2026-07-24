package memory

// Lenses are the extraction prompts, adapted from the local memory-lab harness
// (which in turn adapts Mem0, WDM, Hermes, ReasoningBank, and AWM). Each lens is
// a system prompt + a Go struct the model's JSON reply decodes into. Prompts are
// bilingual-aware (traces are mixed 中英文) and share a grounding clause: extract
// ONLY what the record supports, and prefer nothing over noise (宁缺毋滥).
//
// The three lens families map onto the three "agents" of the nightly loop:
//   - factsLens   → semantic memory (facts / preferences / heuristics)
//   - failureLens → episode + bug/correction memory (gated on a hard failure signal)
//   - skillLens   → reusable-workflow skill candidates (gated on turn count)

const grounding = "只从下面的会话记录中提取，不要臆测或补全记录之外的内容。宁缺毋滥：没有足够证据时返回空数组。所有输出用 JSON，且与记录语言保持一致。不要包含任何用户特定的密钥、令牌、密码、私有连接串或个人身份信息（PII）——这些内容一律省略或用占位符替代。"

// ---- facts lens (Mem0 FACT_RETRIEVAL + WDM reusable gate) ----

const factsSystemPrompt = grounding + `

你是一个记忆抽取器。从这段 AI 编程助手与用户的会话中，抽取"跨会话可复用"的语义记忆条目：稳定的用户偏好、明确的决策、可复用的事实与启发式经验。对每一条，问自己四个问题，四个都为"是"才保留：它是否会在未来持续成立？它是否足够具体可执行？它对未来会话是否有用？它是否独立于本次会话的一次性上下文？

只输出如下 JSON：
{"memories":[{"type":"fact|heuristic|procedure|preference","scope":"global 或某个领域标签如 doris/gvisor/frontend","statement":"陈述句，一句话","when_to_apply":"何时适用","confidence":0.0到1.0之间的数}]}`

// FactsResult decodes the facts lens reply.
type FactsResult struct {
	Memories []CandidateMemory `json:"memories"`
}

// CandidateMemory is one extracted semantic-memory candidate.
type CandidateMemory struct {
	Type        string  `json:"type"`
	Scope       string  `json:"scope"`
	Statement   string  `json:"statement"`
	WhenToApply string  `json:"when_to_apply"`
	Confidence  float64 `json:"confidence"`
}

// ---- failure lens (Hermes review + ReasoningBank FAILED_SI) ----

const failureSystemPrompt = grounding + `

这段会话包含明确的失败或用户不满信号（工具报错、反复纠正、任务未完成）。请做两件事：
1) 用一句话概括本次会话的任务与结局（outcome 取值：success/partial/failed/abandoned/unknown）。
2) 抽取至多 3 条"下次如何避免"的经验，每条包含根因与可执行的规避规则，彼此不重叠。同时区分这是一次纠正（correction）还是一个偏好（preference）。

只输出如下 JSON：
{"task":"...","outcome":"failed","user_frustration":"none|mild|high|unknown","lessons":[{"type":"bug|heuristic|procedure","scope":"...","statement":"根因 + 规避规则，一句话","when_to_apply":"触发条件","kind":"correction|preference","confidence":0.0到1.0}]}`

// FailureResult decodes the failure lens reply.
type FailureResult struct {
	Task            string            `json:"task"`
	Outcome         string            `json:"outcome"`
	UserFrustration string            `json:"user_frustration"`
	Lessons         []CandidateMemory `json:"lessons"`
}

// ---- skill lens (AWM workflow induction + ReasoningBank) ----

const skillSystemPrompt = grounding + `

从这段较长的、包含多步工具操作的会话中，归纳出可复用的"操作流程"（skill）。一个 skill 描述"在什么情况下、按什么有序步骤完成一类任务"，步骤中用语义化的变量名（如 <表名>、<日期范围>）代替本次的具体值。只在流程确实通用、可复用时才产出；否则返回空数组。

只输出如下 JSON：
{"skills":[{"name":"何时使用这个技能（作为标题，一句话）","scope":"领域标签","when_to_use":"触发条件","steps":["有序步骤1","有序步骤2","..."],"pitfalls":["已知坑点..."],"confidence":0.0到1.0}]}`

// SkillResult decodes the skill lens reply.
type SkillResult struct {
	Skills []CandidateSkill `json:"skills"`
}

// CandidateSkill is one induced reusable workflow.
type CandidateSkill struct {
	Name       string   `json:"name"`
	Scope      string   `json:"scope"`
	WhenToUse  string   `json:"when_to_use"`
	Steps      []string `json:"steps"`
	Pitfalls   []string `json:"pitfalls"`
	Confidence float64  `json:"confidence"`
}
